package probe

import (
	"cmp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/results"
)

// recorder is the one clock and event sink for a sample. The tap's handlers
// (one goroutine per HTTP request, overlapping when ffprobe abandons a read)
// and the ffprobe stdout reader write to it concurrently.
type recorder struct {
	origin time.Time    // monotonic origin; all stamps are time.Since(origin)
	last   atomic.Int64 // the latest stamp handed out

	mu    sync.Mutex
	ops   []results.Op
	marks []results.Mark
}

func newRecorder() *recorder { return &recorder{origin: time.Now()} }

// now stamps an instant on the sample clock. Callers stamp first and do
// work after, so a stamp is never delayed by the bookkeeping it triggers.
//
// Stamps are strictly increasing across goroutines, even on a clock coarser
// than the gap between two calls. That is what makes Op's
// Start < FirstByte <= End and Mark's strictly increasing At hold by
// construction.
func (r *recorder) now() time.Duration {
	for {
		t := int64(time.Since(r.origin))
		last := r.last.Load()
		if t <= last {
			t = last + 1
		}
		if r.last.CompareAndSwap(last, t) {
			return time.Duration(t)
		}
	}
}

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
// Ops and Marks are never nil, so an empty sample stores as [] and not null.
func (r *recorder) snapshot() results.Timeline {
	r.mu.Lock()
	defer r.mu.Unlock()
	tl := results.Timeline{
		Ops:   append([]results.Op{}, r.ops...),
		Marks: append([]results.Mark{}, r.marks...),
		End:   r.now(),
	}
	slices.SortStableFunc(tl.Ops, func(a, b results.Op) int { return cmp.Compare(a.Start, b.Start) })
	orderMarksBeforeLaterRequests(tl)
	return tl
}

// orderMarksBeforeLaterRequests pulls a late mark stamp back in front of the
// request the client sent after it.
//
// ffprobe prints a frame line and then sends the next seek's request, a
// measured 0.1-0.5 ms later. The line is stamped when the stdout reader
// wakes and the request when its handler starts, so a short stall of this
// process can stamp them in the wrong order, and the request would then
// count toward the phase that had already ended.
//
// The order is recoverable. ffprobe blocks on each request until the tap
// answers, and the tap answers with the first payload byte (or an error).
// So a request sent before a frame line has its FirstByte (or its End)
// before that line. One that reached that point only after the mark was
// sent after the line, and the mark's true instant is before its Start.
func orderMarksBeforeLaterRequests(tl results.Timeline) {
	for k := range tl.Marks {
		mark := &tl.Marks[k]
		for _, op := range tl.Ops {
			if op.Start > mark.At {
				break
			}
			answered := op.FirstByte
			if answered == 0 {
				answered = op.End
			}
			if answered <= mark.At {
				continue
			}
			// Marks stay strictly increasing even if the reader stalled
			// across a whole phase.
			if at := op.Start - 1; k == 0 || at > tl.Marks[k-1].At {
				mark.At = at
			}
			break
		}
	}
}

// marked is how many phases have closed so far.
func (r *recorder) marked() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.marks)
}

// firstOpErr returns the earliest recorded upstream failure, or "".
func (r *recorder) firstOpErr() string {
	for _, o := range r.snapshot().Ops {
		if o.Err != "" {
			return o.Err
		}
	}
	return ""
}
