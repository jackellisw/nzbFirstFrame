package results

import "time"

// Store is a directory of run files, one JSON file per run:
//
//	<dir>/20261116T101502Z-3f9a1c.json
//
// Concurrency model: per-actor files, merge at read. Each run is written by
// exactly one RunWriter (one process, one goroutine), so concurrent
// `nzbff run` invocations from cron never write the same file. Every write
// is tmp-file + fsync + rename in the same directory (then fsync the
// directory), so a reader (`nzbff serve`) sees either the previous complete
// JSON or the next one, never a torn file, and a power loss can't leave an
// empty file behind a valid name.
// IDs sort lexically by start time, so listing needs no index.
type Store struct {
	dir string
}

// Open returns the store at dir, creating it (0o700) if missing. It removes
// ".*.tmp" files older than a day left by crashed writers.
func Open(dir string) (*Store, error) { panic("not implemented") }

// Dir reports the store's directory.
func (s *Store) Dir() string { return s.dir }

// Create assigns run.ID, sets Schema and Status=running, and publishes the
// initial file. Publishing is exclusive: the tmp file is hard-linked to the
// final name (link fails if it exists), so two processes that pick the same
// ID cannot both win; the loser retries with a new random suffix.
func (s *Store) Create(run Run) (*RunWriter, error) {
	// TODO: id := run.Started.UTC().Format("20060102T150405Z") + "-" + hex(3 random bytes)
	panic("not implemented")
}

// RunInfo is the cheap listing entry (from the file name and stat only).
type RunInfo struct {
	ID       string
	Started  time.Time // parsed from ID
	Modified time.Time // file mtime; changes while a run is in progress
}

// List returns all runs, newest first. It reads no file contents.
func (s *Store) List() ([]RunInfo, error) { panic("not implemented") }

// Load reads and decodes one run. Unknown future schema → error.
func (s *Store) Load(id string) (*Run, error) { panic("not implemented") }

// RunWriter is the sole writer of one run file. It is not safe for
// concurrent use; the engine calls it from one goroutine.
type RunWriter struct {
	s   *Store
	run Run
}

// Run returns the in-memory run (the writer's copy; do not mutate).
func (w *RunWriter) Run() *Run { return &w.run }

// SetTarget records the resolved plan (or planning error) for a target.
func (w *RunWriter) SetTarget(t Target) error { panic("not implemented") }

// Add appends a sample to the series for key and rewrites the file
// atomically, so at most the in-flight sample is lost on a crash. Rewriting a
// whole run per sample is O(run size) and runs are small (KBs per sample).
func (w *RunWriter) Add(key SeriesKey, s Sample) error { panic("not implemented") }

// Finish sets the final status and finish time and writes the file a last
// time. Idempotent: calling it twice rewrites the same content.
func (w *RunWriter) Finish(status Status, at time.Time) error { panic("not implemented") }
