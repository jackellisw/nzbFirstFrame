package results

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

// Render writes a report in the configured format.
//
//	table  one row per (target, layer, phase): N, p50, p90, min, max, reqs,
//	       bytes; failures listed under the table with their errors; a
//	       footer naming the stored run and dir (omitted when dir is "")
//	json   the Report, indented (same shape as GET /api/runs/{id})
//	csv    one row per (sample, phase): run_id, target, layer, iteration,
//	       warmup, phase, label, ok, latency_ms, requests, bytes,
//	       first_ttfb_ms, wait_ms, transfer_ms, client_ms, err
func Render(w io.Writer, rep Report, format config.OutputFormat, dir string) error {
	switch format {
	case config.FormatTable:
		return renderTable(w, rep, dir)
	case config.FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	case config.FormatCSV:
		return renderCSV(w, rep)
	}
	return fmt.Errorf("unknown output format %q", format)
}

// Column caps keep a table row within 100 columns.
const (
	maxTargetWidth = 18
	maxLabelWidth  = 16
)

func renderTable(w io.Writer, rep Report, dir string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TARGET\tLAYER\tPHASE\tN\tP50\tP90\tMIN\tMAX\tREQS\tBYTES")
	for _, c := range rep.Cells {
		for _, p := range c.Phases {
			row := []string{truncate(c.Target, maxTargetWidth), string(c.Layer), truncate(p.Label, maxLabelWidth), strconv.Itoa(p.N)}
			if p.N == 0 {
				row = append(row, "-", "-", "-", "-", "-", "-")
			} else {
				row = append(row, fmtDuration(p.P50), fmtDuration(p.P90), fmtDuration(p.Min), fmtDuration(p.Max),
					strconv.FormatFloat(p.Requests, 'f', -1, 64), fmtBytes(p.Bytes))
			}
			fmt.Fprintln(tw, strings.Join(row, "\t"))
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	failures := failureLines(rep)
	if len(failures) > 0 {
		fmt.Fprintf(w, "%d failed:\n", len(failures))
		for _, f := range failures {
			fmt.Fprintf(w, "  %s\n", f)
		}
	}
	if dir != "" {
		status := ""
		if rep.Run.Status != StatusDone {
			status = " (" + string(rep.Run.Status) + ")"
		}
		fmt.Fprintf(w, "stored run %s%s in %s\n", rep.Run.ID, status, dir)
	}
	return nil
}

// failureLines lists unplanned targets and every sample (warmups included)
// that errored or left a phase unmarked, naming the first unreached phase.
func failureLines(rep Report) []string {
	var out []string
	for _, t := range rep.Targets {
		if t.Err != "" {
			out = append(out, fmt.Sprintf("%s: not planned: %s", t.Name, t.Err))
		}
	}
	plans := make(map[string]Plan, len(rep.Targets))
	for _, t := range rep.Targets {
		plans[t.Name] = t.Plan
	}
	for _, c := range rep.Cells {
		for _, s := range c.Samples {
			phase := -1
			for _, p := range s.Analysis.Phases {
				if !p.OK {
					phase = p.Phase
					break
				}
			}
			if phase < 0 && s.Err == "" {
				continue
			}
			line := fmt.Sprintf("%s/%s ", c.Target, c.Layer)
			if s.Warmup {
				line += "warmup "
			}
			line += "#" + strconv.Itoa(s.Iteration)
			if phase >= 0 {
				line += " " + plans[c.Target].Label(phase)
			}
			msg := s.Err
			if msg == "" {
				msg = "phase not reached"
			}
			out = append(out, line+": "+msg)
		}
	}
	return out
}

func renderCSV(w io.Writer, rep Report) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"run_id", "target", "layer", "iteration", "warmup", "phase", "label", "ok",
		"latency_ms", "requests", "bytes", "first_ttfb_ms", "wait_ms", "transfer_ms", "client_ms", "err"})
	plans := make(map[string]Plan, len(rep.Targets))
	for _, t := range rep.Targets {
		plans[t.Name] = t.Plan
	}
	for _, c := range rep.Cells {
		plan := plans[c.Target]
		for _, s := range c.Samples {
			for _, p := range s.Analysis.Phases {
				var latency, ttfb, wait, transfer, client string
				if p.OK {
					latency, wait, transfer, client = csvMS(p.Latency), csvMS(p.Wait), csvMS(p.Transfer), csvMS(p.Client)
					if d, ok := firstTTFB(s.Timeline.Ops, p); ok {
						ttfb = csvMS(d)
					}
				}
				_ = cw.Write([]string{rep.Run.ID, c.Target, string(c.Layer), strconv.Itoa(s.Iteration),
					strconv.FormatBool(s.Warmup), strconv.Itoa(p.Phase), plan.Label(p.Phase), strconv.FormatBool(p.OK),
					latency, strconv.Itoa(p.Requests), strconv.FormatInt(p.Bytes, 10), ttfb, wait, transfer, client, s.Err})
			}
		}
	}
	cw.Flush()
	return cw.Error()
}

func csvMS(d time.Duration) string {
	return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 3, 64)
}

// fmtDuration is milliseconds below 10s, so values that are compared side
// by side keep one unit.
func fmtDuration(d time.Duration) string {
	ms := float64(d) / float64(time.Millisecond)
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%.2fms", ms)
	case d < 10*time.Millisecond:
		return fmt.Sprintf("%.1fms", ms)
	case d < 10*time.Second:
		return fmt.Sprintf("%.0fms", ms)
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func fmtBytes(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%dB", n)
	case n < 1<<20:
		return fmt.Sprintf("%.0fK", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimRight(string(r[:n-1]), " ") + "…"
}
