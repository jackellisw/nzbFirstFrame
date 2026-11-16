package probe

import (
	"sync"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/results"
)

// recorder is the one clock and event sink for a sample. The tap's handlers
// (one goroutine per HTTP request, overlapping when ffprobe abandons a read)
// and the ffprobe stdout reader write to it concurrently.
type recorder struct {
	origin time.Time // monotonic origin; all stamps are time.Since(origin)

	mu    sync.Mutex
	ops   []results.Op
	marks []results.Mark
}

func newRecorder() *recorder { return &recorder{origin: time.Now()} }

// now stamps an instant on the sample clock. Callers stamp first and do
// work after, so a stamp is never delayed by the bookkeeping it triggers.
func (r *recorder) now() time.Duration { return time.Since(r.origin) }

// op records a completed operation. Safe for concurrent use.
func (r *recorder) op(o results.Op) {
	r.mu.Lock()
	r.ops = append(r.ops, o)
	r.mu.Unlock()
}

// mark records that the next phase completed at `at`. Phase numbering is
// assigned here (len(marks)), so callers cannot skip or repeat a phase.
func (r *recorder) mark(at time.Duration, pts float64) {
	r.mu.Lock()
	r.marks = append(r.marks, results.Mark{Phase: len(r.marks), At: at, PTS: pts})
	r.mu.Unlock()
}

// snapshot returns the Timeline with ops sorted by Start and End set to now.
func (r *recorder) snapshot() results.Timeline {
	// TODO: lock; copy; slices.SortStableFunc(ops, by Start); End = r.now()
	panic("not implemented")
}
