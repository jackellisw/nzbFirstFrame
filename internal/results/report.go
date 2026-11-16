package results

import (
	"slices"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

// Report is the analyzed view of one run. It is the -format json output and
// the body of GET /api/runs/{id}; the dashboard renders nothing it doesn't
// get from here.
type Report struct {
	Run     RunHeader `json:"run"`
	Targets []Target  `json:"targets"`
	Cells   []Cell    `json:"cells"` // target-major, layers top of stack first
}

// RunHeader is Run without its targets and series.
type RunHeader struct {
	ID       string     `json:"id"`
	Status   Status     `json:"status"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
	Host     string     `json:"host"`
	Version  string     `json:"version"`
	Mode     string     `json:"mode"`
	Settings Settings   `json:"settings"`
}

// Cell is one (target, layer) with statistics per phase and every sample.
type Cell struct {
	SeriesKey
	Phases  []PhaseStats   `json:"phases"`
	Samples []SampleReport `json:"samples"`
}

// PhaseStats summarizes one phase over measured (non-warmup), marked samples.
type PhaseStats struct {
	Phase  int    `json:"phase"`
	Label  string `json:"label"` // "startup", "seek 50%": the trend key across runs
	N      int    `json:"n"`
	Failed int    `json:"failed"`

	Min  time.Duration `json:"min_ns"`
	P50  time.Duration `json:"p50_ns"`
	P90  time.Duration `json:"p90_ns"`
	Max  time.Duration `json:"max_ns"`
	Mean time.Duration `json:"mean_ns"`

	Requests float64 `json:"requests_p50"`
	Bytes    int64   `json:"bytes_p50"`
	// TTFB is over the N samples whose phase has a request (read/article)
	// that got a byte, using the first such request.
	TTFB time.Duration `json:"first_ttfb_p50_ns"`
}

// SampleReport is one stored sample plus its analysis, for drill-down.
type SampleReport struct {
	Sample
	Analysis Analysis `json:"analysis"`
}

// Summarize derives the Report from a stored run. Pure.
//
// There is one Cell per stored Series, in stored order, even when it has no
// samples: the grid is the plan, so a layer that failed every sample (or a
// target that could not be planned) stays visible. Statistics cover measured
// (non-warmup) samples whose phase was marked, with nearest-rank
// percentiles; Failed counts measured samples whose phase was not marked.
// Layers are never subtracted from one another: they run different
// workloads (a video session versus whole NNTP articles).
func Summarize(run *Run) Report {
	plans := make(map[string]Plan, len(run.Targets))
	for _, t := range run.Targets {
		plans[t.Name] = t.Plan
	}
	rep := Report{
		Run: RunHeader{
			ID: run.ID, Status: run.Status, Started: run.Started, Finished: run.Finished,
			Host: run.Host, Version: run.Version, Mode: run.Mode, Settings: run.Settings,
		},
		Targets: run.Targets,
		Cells:   make([]Cell, 0, len(run.Series)),
	}
	for _, se := range run.Series {
		plan := plans[se.Target]
		cell := Cell{
			SeriesKey: se.SeriesKey,
			Phases:    make([]PhaseStats, plan.Len()),
			Samples:   make([]SampleReport, len(se.Samples)),
		}
		for i, s := range se.Samples {
			cell.Samples[i] = SampleReport{Sample: s, Analysis: Analyze(s.Timeline, plan)}
		}
		for k := range cell.Phases {
			cell.Phases[k] = phaseStats(k, plan.Label(k), cell.Samples)
		}
		rep.Cells = append(rep.Cells, cell)
	}
	return rep
}

func phaseStats(k int, label string, samples []SampleReport) PhaseStats {
	ps := PhaseStats{Phase: k, Label: label}
	var lat, ttfb []time.Duration
	var reqs, bytes []int64
	for _, s := range samples {
		if s.Warmup {
			continue
		}
		pr := s.Analysis.Phases[k]
		if !pr.OK {
			ps.Failed++
			continue
		}
		lat = append(lat, pr.Latency)
		reqs = append(reqs, int64(pr.Requests))
		bytes = append(bytes, pr.Bytes)
		if d, ok := firstTTFB(s.Timeline.Ops, pr); ok {
			ttfb = append(ttfb, d)
		}
	}
	ps.N = len(lat)
	if ps.N == 0 {
		return ps
	}
	slices.Sort(lat)
	var sum time.Duration
	for _, d := range lat {
		sum += d
	}
	ps.Min, ps.Max = lat[0], lat[len(lat)-1]
	ps.P50, ps.P90 = nearestRank(lat, 50), nearestRank(lat, 90)
	ps.Mean = sum / time.Duration(ps.N)
	slices.Sort(reqs)
	slices.Sort(bytes)
	slices.Sort(ttfb)
	ps.Requests = float64(nearestRank(reqs, 50))
	ps.Bytes = nearestRank(bytes, 50)
	ps.TTFB = nearestRank(ttfb, 50)
	return ps
}

func nearestRank[T int64 | time.Duration](xs []T, p int) T {
	if len(xs) == 0 {
		return 0
	}
	rank := (p*len(xs) + 99) / 100
	return xs[max(rank, 1)-1]
}

func firstTTFB(ops []Op, pr PhaseResult) (time.Duration, bool) {
	for _, i := range pr.Ops {
		op := ops[i]
		if (op.Kind == OpRead || op.Kind == OpArticle) && op.FirstByte > 0 {
			return op.FirstByte - op.Start, true
		}
	}
	return 0, false
}

// RunSummary is one point per trend line: GET /api/runs.
type RunSummary struct {
	RunHeader
	Points []TrendPoint `json:"points"`
}

// TrendPoint is the median of one phase label in one cell. Phases with no
// successful measured sample have no median and produce no point, so a
// trend line shows a gap rather than a false zero.
type TrendPoint struct {
	Target string        `json:"target"`
	Layer  config.Layer  `json:"layer"`
	Label  string        `json:"label"`
	P50    time.Duration `json:"p50_ns"`
	N      int           `json:"n"`
}

// Trend projects a report onto its trend points.
func (r Report) Trend() RunSummary {
	sum := RunSummary{RunHeader: r.Run, Points: []TrendPoint{}}
	for _, c := range r.Cells {
		for _, p := range c.Phases {
			if p.N == 0 {
				continue
			}
			sum.Points = append(sum.Points, TrendPoint{
				Target: c.Target, Layer: c.Layer, Label: p.Label, P50: p.P50, N: p.N,
			})
		}
	}
	return sum
}
