package results

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Store is a directory of run files, one JSON file per run:
//
//	<dir>/20261116T101502.348Z-3f9a1c.json
//
// Concurrency model: per-actor files, merge at read. Each run is written by
// exactly one RunWriter (one process, one goroutine), so concurrent
// `nzbff run` invocations from cron never write the same file. Every write
// is tmp-file + fsync + rename in the same directory (then fsync the
// directory), so a reader (`nzbff serve`) sees either the previous complete
// JSON or the next one, never a torn file, and a power loss can't leave an
// empty file behind a valid name.
// An ID starts with the run's start time, so listing needs no index.
type Store struct {
	dir string
}

// Open returns the store at dir, creating it (0o700) if missing. It removes
// ".*.tmp" files older than a day left by crashed writers.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("results dir: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("results dir: %w", err)
	}
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, ".") || !strings.HasSuffix(n, ".tmp") {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 24*time.Hour {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
	return &Store{dir: dir}, nil
}

// ErrNewerSchema is returned by Load for a run written by a newer nzbff.
var ErrNewerSchema = errors.New("run file schema is newer than this nzbff supports")

// idTimeLayout formats an ID's start time to the millisecond, so runs
// started within the same second still list in start order. IDs written
// before that have no fraction ("20261116T101502Z-3f9a1c"); they still
// load, and parsing with the layout's whole-second form reads both.
const idTimeLayout = "20060102T150405.000Z"

var idPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}(\.[0-9]{3})?Z-[0-9a-f]{6}$`)

func idStarted(id string) (time.Time, error) {
	stamp, _, _ := strings.Cut(id, "-")
	return time.Parse("20060102T150405Z", stamp)
}

var randSuffix = func() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *Store) writeTmp(run *Run) (string, error) {
	data, err := json.MarshalIndent(run, "", " ")
	if err != nil {
		return "", fmt.Errorf("encode run %s: %w", run.ID, err)
	}
	f, err := os.CreateTemp(s.dir, "."+run.ID+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("write run %s: %w", run.ID, err)
	}
	_, err = f.Write(append(data, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write run %s: %w", run.ID, err)
	}
	return f.Name(), nil
}

func (s *Store) replace(run *Run) error {
	tmp, err := s.writeTmp(run)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path(run.ID)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write run %s: %w", run.ID, err)
	}
	return s.syncDir()
}

func (s *Store) syncDir() error {
	d, err := os.Open(s.dir)
	if err != nil {
		return fmt.Errorf("sync results dir: %w", err)
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("sync results dir: %w", err)
	}
	return nil
}

// Dir reports the store's directory.
func (s *Store) Dir() string { return s.dir }

// Create assigns run.ID, sets Schema and Status=running, and publishes the
// initial file. Publishing is exclusive: the tmp file is hard-linked to the
// final name (link fails if it exists), so two processes that pick the same
// ID cannot both win; the loser retries with a new random suffix.
func (s *Store) Create(run Run) (*RunWriter, error) {
	run.Schema = SchemaVersion
	run.Status = StatusRunning
	run.Targets = slices.Clone(run.Targets)
	run.Series = slices.Clone(run.Series)
	for i := range run.Series {
		run.Series[i].Samples = slices.Clip(run.Series[i].Samples)
	}
	stamp := run.Started.UTC().Format(idTimeLayout)
	for range 10 {
		run.ID = stamp + "-" + randSuffix()
		tmp, err := s.writeTmp(&run)
		if err != nil {
			return nil, err
		}
		err = os.Link(tmp, s.path(run.ID))
		_ = os.Remove(tmp)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("publish run %s: %w", run.ID, err)
		}
		if err := s.syncDir(); err != nil {
			return nil, err
		}
		return &RunWriter{s: s, run: run}, nil
	}
	return nil, fmt.Errorf("publish run: no free ID for %s after 10 attempts", stamp)
}

// RunInfo is the cheap listing entry (from the file name and stat only).
type RunInfo struct {
	ID       string
	Started  time.Time // parsed from ID
	Modified time.Time // file mtime; changes while a run is in progress
}

// List returns all runs, newest first. It reads no file contents.
func (s *Store) List() ([]RunInfo, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	var out []RunInfo
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !idPattern.MatchString(id) {
			continue
		}
		started, err := idStarted(id)
		if err != nil {
			continue
		}
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("list runs: %w", err)
		}
		out = append(out, RunInfo{ID: id, Started: started, Modified: info.ModTime()})
	}
	// By time, not text: '.' sorts before 'Z', so an old ID would list
	// after a newer one from the same second.
	slices.SortFunc(out, func(a, b RunInfo) int {
		if c := b.Started.Compare(a.Started); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	return out, nil
}

// Load reads and decodes one run. Unknown future schema → ErrNewerSchema.
// An id that is not a run ID (e.g. from a URL) is fs.ErrNotExist.
func (s *Store) Load(id string) (*Run, error) {
	if !idPattern.MatchString(id) {
		return nil, fmt.Errorf("run %q: %w", id, fs.ErrNotExist)
	}
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		return nil, fmt.Errorf("load run: %w", err)
	}
	var head struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("load run %s: %w", id, err)
	}
	switch {
	case head.Schema > SchemaVersion:
		return nil, fmt.Errorf("load run %s: schema %d > %d: %w", id, head.Schema, SchemaVersion, ErrNewerSchema)
	case head.Schema < 1:
		return nil, fmt.Errorf("load run %s: missing schema version", id)
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, fmt.Errorf("load run %s: %w", id, err)
	}
	if run.ID != id {
		return nil, fmt.Errorf("load run %s: file holds run %q", id, run.ID)
	}
	return &run, nil
}

// RunWriter is the sole writer of one run file. It is not safe for
// concurrent use; the engine calls it from one goroutine.
type RunWriter struct {
	s   *Store
	run Run
}

// Run returns the in-memory run (the writer's copy; do not mutate).
func (w *RunWriter) Run() *Run { return &w.run }

// SetTarget records the resolved plan (or planning error) for a target.
// A target already recorded under the same name is replaced.
func (w *RunWriter) SetTarget(t Target) error {
	i := slices.IndexFunc(w.run.Targets, func(x Target) bool { return x.Name == t.Name })
	if i < 0 {
		w.run.Targets = append(w.run.Targets, t)
	} else {
		w.run.Targets[i] = t
	}
	return w.s.replace(&w.run)
}

// Add appends a sample to the series for key and rewrites the file
// atomically, so at most the in-flight sample is lost on a crash. Rewriting a
// whole run per sample is O(run size) and runs are small (KBs per sample).
// A key with no series yet gets one appended.
func (w *RunWriter) Add(key SeriesKey, s Sample) error {
	i := slices.IndexFunc(w.run.Series, func(x Series) bool { return x.SeriesKey == key })
	if i < 0 {
		w.run.Series = append(w.run.Series, Series{SeriesKey: key})
		i = len(w.run.Series) - 1
	}
	w.run.Series[i].Samples = append(w.run.Series[i].Samples, s)
	return w.s.replace(&w.run)
}

// Finish sets the final status and finish time and writes the file a last
// time. Idempotent: calling it twice rewrites the same content.
func (w *RunWriter) Finish(status Status, at time.Time) error {
	w.run.Status = status
	w.run.Finished = &at
	return w.s.replace(&w.run)
}
