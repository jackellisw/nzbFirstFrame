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
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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

	// nntpRoots verifies the provider's TLS certificate; nil means the
	// system roots. Tests point it at their own CA.
	nntpRoots *x509.CertPool
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

// ErrNoVideoLayer is returned by Media when no selected layer can be read by
// ffprobe, so the duration cannot be known.
var ErrNoVideoLayer = errors.New("no video layer selected to read the media duration from")

// Media resolves the target's runtime with a quick
// `ffprobe -show_entries format=duration` through the deepest configured
// video layer (infinidysk, then rclone, then jellyfin), so the probe warms as
// little of the stack above it as possible.
func (p *Prober) Media(ctx context.Context, t config.Target) (Media, error) {
	for _, l := range slices.Backward(config.AllLayers) {
		if m := layers[l]; p.cfg.HasLayer(l) && m.video != nil {
			d, err := p.durationVia(ctx, m.video(p, t))
			if err != nil {
				return Media{}, fmt.Errorf("duration via %s: %w", l, err)
			}
			return Media{Duration: d, Via: l}, nil
		}
	}
	return Media{}, ErrNoVideoLayer
}

func (p *Prober) durationVia(ctx context.Context, src source) (time.Duration, error) {
	defer src.close()
	ctx, cancel := context.WithTimeout(ctx, p.cfg.Profile.Timeout.D())
	defer cancel()
	rec := newRecorder()
	tap, err := startTap(src, rec)
	if err != nil {
		return 0, err
	}
	d, err := p.mediaDuration(ctx, tap.url)
	tap.close()
	if err != nil {
		return 0, withUpstream(err, rec)
	}
	return d, nil
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
//   - timeout: profile.timeout bounds the whole session, the cold-cache call
//     included.
func (p *Prober) Sample(ctx context.Context, layer config.Layer, t config.Target, plan results.Plan) (results.Timeline, error) {
	m := layers[layer]
	ctx, cancel := context.WithTimeout(ctx, p.cfg.Profile.Timeout.D())
	defer cancel()
	if p.cfg.Profile.ColdCache && m.viaRclone {
		if err := p.forget(ctx, t.Path); err != nil {
			return newRecorder().snapshot(), fmt.Errorf("cold cache: %w", err)
		}
	}
	rec := newRecorder()
	var err error
	if m.video != nil {
		err = p.runVideo(ctx, rec, m.video(p, t), plan)
	} else {
		err = m.custom(p, ctx, rec, t, plan)
	}
	return rec.snapshot(), err
}

// phaseError words a sample failure the same way for every layer: which
// phase was open, and whether the per-sample timeout or a cancellation
// ended it.
func (p *Prober) phaseError(ctx context.Context, phase, detail string) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("timeout after %s in phase %q (%s)", p.cfg.Profile.Timeout, phase, detail)
	case ctx.Err() != nil:
		return fmt.Errorf("phase %q: %w (%s)", phase, ctx.Err(), detail)
	default:
		return fmt.Errorf("phase %q: %s", phase, detail)
	}
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
	defer src.close()
	tap, err := startTap(src, rec)
	if err != nil {
		return err
	}
	err = p.ffprobe(ctx, rec, tap.url, plan)
	tap.close()
	if err != nil {
		return withUpstream(err, rec)
	}
	return nil
}

// withUpstream adds the first upstream failure to err. ffprobe only sees
// the tap's 502, so its own message cannot say why the stack failed.
func withUpstream(err error, rec *recorder) error {
	if cause := rec.firstOpErr(); cause != "" {
		return fmt.Errorf("%w; upstream: %s", err, cause)
	}
	return err
}
