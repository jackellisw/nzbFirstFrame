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
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
	"github.com/jackellisw/nzbfirstframe/internal/probe"
	"github.com/jackellisw/nzbfirstframe/internal/results"
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
	// TODO:
	//   cfg := opts.Config
	//   store, err := results.Open(cfg.Results.Dir)                        → return err
	//   w, err := store.Create(newRun(cfg, opts.Mode, time.Now()))         → return err
	//   p := probe.New(cfg, opts.Logger)
	//   for _, t := range cfg.Targets {
	//       tr := planTarget(ctx, p, cfg, opts.Mode, t); w.SetTarget(tr)
	//       if tr.Err != "" { log; continue }
	//       for _, s := range schedule(cfg.Profile) {                       // interleaved
	//           if ctx.Err() != nil { break out }
	//           if s.first == false { sleep cooldown (ctx-aware) }
	//           tl, err := p.Sample(ctx, s.layer, t, tr.Plan)
	//           w.Add(SeriesKey{t.Name, s.layer}, Sample{s.iteration, s.warmup, at, tl, errString(err)})
	//           log one line: layer, target, iteration, phase latencies via results.Analyze
	//       }
	//   }
	//   status := done | interrupted; w.Finish(status, now)
	//   rep := results.Summarize(w.Run()); results.Render(opts.Out, rep, cfg.Output.Format)
	//   return ctx.Err() | *FailedError | nil
	_ = ctx
	return ErrNotImplemented // sketch: keeps the existing CLI test meaningful
}

// ErrNotImplemented is kept for the CLI test until the engine lands.
var ErrNotImplemented = errors.New("profiling is not implemented yet (use -dry-run to inspect the plan)")

// planTarget resolves duration and the seek plan. Startup-only mode skips
// the duration probe entirely (no seeks to resolve, nothing warmed).
// Usenet-only runs have no video layer to probe; percent points then
// resolve with a nominal duration (Fraction is all usenet uses) and
// absolute points are a target error.
func planTarget(ctx context.Context, p *probe.Prober, cfg *config.Config, mode Mode, t config.Target) results.Target {
	_ = results.NewPlan
	panic("not implemented")
}

// step is one scheduled sample.
type step struct {
	layer     config.Layer
	iteration int
	warmup    bool
	first     bool // no cooldown before the first step of a target
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
func schedule(p config.ProfileConfig) []step { panic("not implemented") }

// newRun builds the stored run header from config.
func newRun(cfg *config.Config, mode Mode, started time.Time) results.Run {
	panic("not implemented")
}
