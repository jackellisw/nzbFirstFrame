// Package profiler runs first-frame and seek measurements across the stack.
//
// Run is the engine: plan each target, take samples layer-interleaved,
// persist every sample as it lands, then render the derived report. How a
// layer is reached lives in package probe; what is stored and derived lives
// in package results.
package profiler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
	"github.com/jackellisw/nzbfirstframe/internal/probe"
	"github.com/jackellisw/nzbfirstframe/internal/results"
	"github.com/jackellisw/nzbfirstframe/internal/version"
)

// Mode selects which measurements to run.
type Mode string

const (
	ModeAll     Mode = "all"     // startup, then seeks
	ModeStartup Mode = "startup" // time to first frame from a cold open
	ModeSeek    Mode = "seek"    // time to first frame after seeking to each seek point
)

func (m Mode) Startup() bool { return m == ModeAll || m == ModeStartup }
func (m Mode) Seek() bool    { return m == ModeAll || m == ModeSeek }

// Options configures a profiling run.
type Options struct {
	Config *config.Config // finalized
	Mode   Mode
	Logger *slog.Logger
	Out    io.Writer // where the report is rendered
}

// FailedError reports that the run completed and was stored but some
// samples or targets failed. The report (with the errors) was rendered
// before it is returned, so cron gets both output and a non-zero exit.
//
// Total is every measured (non-warmup) sample the run scheduled. Failed
// counts the measured samples that failed, plus all measured samples of a
// target that could not be planned.
type FailedError struct {
	RunID         string
	Failed, Total int
}

func (e *FailedError) Error() string {
	return fmt.Sprintf("run %s: %d of %d samples failed (see report)", e.RunID, e.Failed, e.Total)
}

// Run executes the profile described by opts.
//
// Failure policy: a sample error is data (stored on the sample); a target
// whose duration can't be resolved is data (stored on the target, its
// series stay empty); neither stops the run. Only store I/O errors and
// cancellation stop it. On cancellation the partial run is finished as
// "interrupted" and rendered, then ctx.Err() is returned.
func Run(ctx context.Context, opts Options) error {
	cfg, log := opts.Config, opts.Logger
	store, err := results.Open(cfg.Results.Dir)
	if err != nil {
		return err
	}
	w, err := store.Create(newRun(cfg, opts.Mode, time.Now()))
	if err != nil {
		return err
	}
	id := w.Run().ID
	log.Info("run started", "id", id, "dir", store.Dir(), "mode", opts.Mode)

	p := probe.New(cfg, log)
	steps := schedule(cfg.Profile)
	perTarget := cfg.Profile.Iterations * len(cfg.Profile.Layers)
	failed := 0
targets:
	for _, t := range cfg.Targets {
		tr := planTarget(ctx, p, cfg, opts.Mode, t)
		if ctx.Err() != nil {
			break
		}
		if err := w.SetTarget(tr); err != nil {
			return err
		}
		if tr.Err != "" {
			log.Warn("target not planned", "target", t.Name, "err", tr.Err)
			failed += perTarget
			continue
		}
		log.Info("target planned", "target", t.Name, "duration", tr.Duration, "phases", tr.Plan.Len())

		for i, s := range steps {
			pause := cfg.Profile.Cooldown.D()
			if i == 0 {
				pause = 0
			}
			if !sleep(ctx, pause) {
				break targets
			}
			at := time.Now().UTC()
			tl, err := p.Sample(ctx, s.layer, t, tr.Plan)
			if err != nil && ctx.Err() != nil {
				// Cut short by our own cancellation, not by the stack:
				// stored, it would read as a failure of the layer.
				break targets
			}
			sample := results.Sample{Iteration: s.iteration, Warmup: s.warmup, At: at, Timeline: tl}
			if err != nil {
				sample.Err = err.Error()
				if !s.warmup {
					failed++
				}
			}
			if err := w.Add(results.SeriesKey{Target: t.Name, Layer: s.layer}, sample); err != nil {
				return err
			}
			logSample(log, t.Name, s, tr.Plan, sample)
		}
	}

	status := results.StatusDone
	if ctx.Err() != nil {
		status = results.StatusInterrupted
	}
	if err := w.Finish(status, time.Now().UTC()); err != nil {
		return err
	}
	if err := results.Render(opts.Out, results.Summarize(w.Run()), cfg.Output.Format, store.Dir()); err != nil {
		return fmt.Errorf("render report: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if failed > 0 {
		return &FailedError{RunID: id, Failed: failed, Total: perTarget * len(cfg.Targets)}
	}
	return nil
}

// sleep waits d, or reports false as soon as ctx is done.
func sleep(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// logSample writes one line per sample with each phase's latency.
func logSample(log *slog.Logger, target string, s step, plan results.Plan, sample results.Sample) {
	a := results.Analyze(sample.Timeline, plan)
	phases := make([]string, len(a.Phases))
	for k, ph := range a.Phases {
		latency := "-"
		if ph.OK {
			latency = ph.Latency.Round(100 * time.Microsecond).String()
		}
		phases[k] = plan.Label(k) + " " + latency
	}
	attrs := []any{"target", target, "layer", s.layer, "iteration", s.iteration, "warmup", s.warmup,
		"phases", strings.Join(phases, ", ")}
	if sample.Err != "" {
		log.Warn("sample", append(attrs, "err", sample.Err)...)
		return
	}
	log.Info("sample", attrs...)
}

// planTarget resolves duration and the seek plan. Startup-only mode skips
// the duration probe entirely (no seeks to resolve, nothing warmed). Without
// a video layer the duration stays unknown: percent points then resolve to a
// fraction (all usenet uses) and absolute points are a target error.
func planTarget(ctx context.Context, p *probe.Prober, cfg *config.Config, mode Mode, t config.Target) results.Target {
	tr := results.Target{Name: t.Name, Path: t.Path, ItemID: t.JellyfinItemID}
	points := seekPoints(cfg, mode)
	if len(points) > 0 {
		m, err := p.Media(ctx, t)
		if err != nil && !errors.Is(err, probe.ErrNoVideoLayer) {
			tr.Err = err.Error()
			return tr
		}
		tr.Duration = m.Duration
	}
	plan, err := results.NewPlan(points, tr.Duration)
	if err != nil {
		tr.Err = err.Error() + " (no video layer is selected to read it from; use percent seek points)"
		return tr
	}
	tr.Plan = plan
	return tr
}

// seekPoints are the points a run in mode seeks to.
func seekPoints(cfg *config.Config, mode Mode) []config.SeekPoint {
	if !mode.Seek() {
		return nil
	}
	return cfg.Profile.SeekPoints
}

// step is one scheduled sample.
type step struct {
	layer     config.Layer
	iteration int
	warmup    bool
}

// schedule orders a target's samples iteration-major: all warmups (every
// layer), then iteration 0 of every layer, iteration 1 of every layer, ...
// Within an iteration layers run in config.AllLayers order.
//
// Interleaving keeps layers comparable side by side: provider congestion
// that drifts over a long run hits every layer of iteration i alike instead
// of landing on whichever layer's series happened to run during it.
//
// Order cannot make lower caches cold: after the warmup every layer has read
// the same byte ranges, so infinidysk/provider caches are warm for all
// measured samples whatever the order. What is measured is the warm path
// plus (with cold_cache) cold rclone metadata; see RATIONALE open questions.
func schedule(p config.ProfileConfig) []step {
	var steps []step
	add := func(n int, warmup bool) {
		for i := range n {
			for _, l := range stackOrder(p.Layers) {
				steps = append(steps, step{layer: l, iteration: i, warmup: warmup})
			}
		}
	}
	add(p.Warmup, true)
	add(p.Iterations, false)
	return steps
}

// stackOrder returns the selected layers, top of the stack first.
func stackOrder(selected []config.Layer) []config.Layer {
	return slices.DeleteFunc(slices.Clone(config.AllLayers), func(l config.Layer) bool {
		return !slices.Contains(selected, l)
	})
}

// newRun builds the stored run header from config, with every target and
// (target, layer) series in place so a partial file still shows the plan.
func newRun(cfg *config.Config, mode Mode, started time.Time) results.Run {
	p := cfg.Profile
	host, _ := os.Hostname()
	layers := stackOrder(p.Layers)
	run := results.Run{
		Started: started.UTC(),
		Host:    host,
		Version: version.Version,
		Mode:    string(mode),
		Settings: results.Settings{
			Layers:     layers,
			Iterations: p.Iterations,
			Warmup:     p.Warmup,
			Timeout:    p.Timeout.D(),
			Cooldown:   p.Cooldown.D(),
			ColdCache:  p.ColdCache,
		},
	}
	for _, sp := range seekPoints(cfg, mode) {
		run.Settings.SeekPoints = append(run.Settings.SeekPoints, sp.String())
	}
	for _, t := range cfg.Targets {
		run.Targets = append(run.Targets, results.Target{Name: t.Name, Path: t.Path, ItemID: t.JellyfinItemID})
		for _, l := range layers {
			run.Series = append(run.Series, results.Series{SeriesKey: results.SeriesKey{Target: t.Name, Layer: l}})
		}
	}
	return run
}
