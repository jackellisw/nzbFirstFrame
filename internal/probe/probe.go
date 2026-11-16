// Package probe takes one measurement of one layer of the stack.
//
// It owns everything that knows how to reach a hop: the local HTTP tap, the
// per-layer sources behind it (Jellyfin stream API, the rclone mount,
// infinidysk), the ffprobe driver, the NNTP client and NZB parser, and the
// rclone rc cold-cache call. None of that is exported: callers see
// Sample/Media and get back a results.Timeline.
//
// Adding a layer: add the config.Layer constant (config.AllLayers) and one
// entry in the layers table below. A test asserts the table's keys equal
// config.AllLayers, so the two lists cannot drift silently.
package probe

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
	"github.com/jackellisw/nzbfirstframe/internal/results"
)

// Prober measures layers described by one finalized config. It is safe to
// reuse across samples but samples must not run concurrently: concurrent
// samples would contend on the same upstream and corrupt each other's timing.
type Prober struct {
	cfg *config.Config
	log *slog.Logger
}

// New returns a Prober for a finalized (validated) config.
func New(cfg *config.Config, log *slog.Logger) *Prober {
	return &Prober{cfg: cfg, log: log}
}

// Media is what the engine needs to know about a target before measuring.
type Media struct {
	Duration time.Duration
	Via      config.Layer // which layer answered the duration probe
}

// Media resolves the target's runtime with a quick
// `ffprobe -show_entries format=duration` through the deepest configured
// video layer (infinidysk, then rclone, then jellyfin), so the probe warms as
// little of the stack above it as possible. Returns an error when no video
// layer is selected; usenet-only runs can still use percent seek points (see
// the engine).
func (p *Prober) Media(ctx context.Context, t config.Target) (Media, error) {
	// TODO: for l in reverse(config.AllLayers) if p.cfg.HasLayer(l) && layers[l].video != nil:
	//   start a tap on layers[l].video(p, t) with a throwaway recorder,
	//   run ffprobe -v error -show_entries format=duration -of default=nw=1:nk=1 <url>,
	//   parse float seconds. Bounded by profile.timeout.
	panic("not implemented")
}

// Sample runs one measurement of plan on layer for target and returns what it
// observed. The returned Timeline is valid (possibly partial) even when err
// != nil: a sample that timed out after startup still reports startup.
//
// Sample owns the per-sample policy so the engine doesn't have to:
//   - cold cache: when profile.cold_cache is set and the layer reads through
//     rclone, POST vfs/forget for the target path first (not on the timeline);
//   - isolation: a fresh upstream transport per sample, so each sample pays
//     its own connects, as a fresh player session does;
//   - timeout: profile.timeout bounds the whole session.
func (p *Prober) Sample(ctx context.Context, layer config.Layer, t config.Target, plan results.Plan) (results.Timeline, error) {
	// TODO:
	//   m := layers[layer]   // validated config guarantees presence
	//   if p.cfg.Profile.ColdCache && m.viaRclone { if err := p.forget(ctx, t.Path); err != nil { return {}, err } }
	//   ctx, cancel := context.WithTimeout(ctx, p.cfg.Profile.Timeout.D())
	//   rec := newRecorder()
	//   var err error
	//   if m.video != nil { err = p.runVideo(ctx, rec, m.video(p, t), plan) } else { err = m.custom(p, ctx, rec, t, plan) }
	//   return rec.snapshot(), err
	panic("not implemented")
}

// method is how one layer is measured: either as a byte source behind the
// tap (ffprobe does the reading) or with a custom client (usenet).
type method struct {
	video     func(p *Prober, t config.Target) source
	custom    func(p *Prober, ctx context.Context, rec *recorder, t config.Target, plan results.Plan) error
	viaRclone bool // cold-cache flush applies
}

// layers is the single list of how each layer is measured.
var layers = map[config.Layer]method{
	config.LayerJellyfin:   {video: jellyfinSource, viaRclone: true},
	config.LayerRclone:     {video: mountSource, viaRclone: true},
	config.LayerInfinidysk: {video: infinidyskSource},
	config.LayerUsenet:     {custom: (*Prober).sampleUsenet},
}

// runVideo serves src on a local tap and drives ffprobe against it.
func (p *Prober) runVideo(ctx context.Context, rec *recorder, src source, plan results.Plan) error {
	// TODO:
	//   tap, err := startTap(src, rec); defer tap.close()
	//   (tap.close waits for in-flight handlers so their ops are recorded)
	//   return p.ffprobe(ctx, rec, tap.url, plan)
	panic("not implemented")
}
