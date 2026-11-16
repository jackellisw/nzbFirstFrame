// Package results owns the stored record of profiling runs and everything
// derived from it: the on-disk store, the analyzed Report the CLI and the
// dashboard both show, and rendering to table/json/csv.
package results

import (
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

// SchemaVersion is bumped on any incompatible change to the stored shape.
// Load rejects files with a newer version and upgrades older ones in memory.
const SchemaVersion = 1

// Status is a run's lifecycle state as last written.
type Status string

const (
	StatusRunning     Status = "running" // in progress, or the process died (no heartbeat; see dashboard)
	StatusDone        Status = "done"
	StatusInterrupted Status = "interrupted" // context cancelled (Ctrl-C, SIGTERM)
)

// Run is one `nzbff run|startup|seek` invocation, as stored.
type Run struct {
	Schema   int        `json:"schema"`
	ID       string     `json:"id"` // assigned by Store.Create; also the file name
	Status   Status     `json:"status"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
	Host     string     `json:"host"`
	Version  string     `json:"version"`
	Mode     string     `json:"mode"`
	Settings Settings   `json:"settings"`

	Targets []Target `json:"targets"`
	// Series has one entry per (target, layer) in plan order, created up
	// front so a partial file still shows what was planned.
	Series []Series `json:"series"`
}

// Settings snapshots the profile config that shaped the run. No endpoints,
// no secrets.
type Settings struct {
	Layers     []config.Layer `json:"layers"`
	Iterations int            `json:"iterations"`
	Warmup     int            `json:"warmup"`
	Timeout    time.Duration  `json:"timeout_ns"`
	Cooldown   time.Duration  `json:"cooldown_ns"`
	ColdCache  bool           `json:"cold_cache"`
	SeekPoints []string       `json:"seek_points"`
}

// Target is one profiled media item and the plan every layer ran for it.
type Target struct {
	Name     string        `json:"name"`
	Path     string        `json:"path,omitempty"`
	ItemID   string        `json:"jellyfin_item_id,omitempty"`
	Duration time.Duration `json:"duration_ns"`
	Plan     Plan          `json:"plan"`
	// Err is set when the target could not be planned (duration probe
	// failed); its series then have no samples.
	Err string `json:"err,omitempty"`
}

// SeriesKey identifies a series within a run.
type SeriesKey struct {
	Target string       `json:"target"`
	Layer  config.Layer `json:"layer"`
}

// Series is every sample of one layer for one target.
type Series struct {
	SeriesKey
	Samples []Sample `json:"samples"`
}

// Sample is one session: open plus the plan's seeks in one client process.
type Sample struct {
	Iteration int       `json:"iteration"` // 0-based within its kind (warmup or measured)
	Warmup    bool      `json:"warmup"`    // excluded from statistics, kept for inspection
	At        time.Time `json:"at"`        // wall clock at start, for trends within a run
	Timeline  Timeline  `json:"timeline"`
	// Err is the session-level failure (ffprobe exit + stderr tail, timeout,
	// NNTP error). Phases marked before it still count.
	Err string `json:"err,omitempty"`
}
