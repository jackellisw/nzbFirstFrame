package probe

import (
	"cmp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/results"
)

type recorder struct {
	origin time.Time
	last   atomic.Int64

	mu    sync.Mutex
	ops   []results.Op
	marks []results.Mark
}

func newRecorder() *recorder { return &recorder{origin: time.Now()} }

func (r *recorder) stamp() time.Duration {
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

func (r *recorder) op(o results.Op) {
	r.mu.Lock()
	r.ops = append(r.ops, o)
	r.mu.Unlock()
}

func (r *recorder) mark(at time.Duration, pts float64) {
	r.mu.Lock()
	r.marks = append(r.marks, results.Mark{Phase: len(r.marks), At: at, PTS: pts})
	r.mu.Unlock()
}

func (r *recorder) snapshot() results.Timeline {
	r.mu.Lock()
	defer r.mu.Unlock()
	tl := results.Timeline{
		Ops:   append([]results.Op{}, r.ops...),
		Marks: append([]results.Mark{}, r.marks...),
		End:   r.stamp(),
	}
	slices.SortStableFunc(tl.Ops, func(a, b results.Op) int { return cmp.Compare(a.Start, b.Start) })
	orderMarksBeforeLaterRequests(tl)
	return tl
}

// ffprobe sends the next seek's request 0.1-0.5 ms after printing a frame
// line, and blocks on each request until the tap's first payload byte. So a
// request whose FirstByte (or End) comes after a mark was sent after that
// frame line, even when a stalled stdout reader stamped the line late.
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
			if at := op.Start - 1; k == 0 || at > tl.Marks[k-1].At {
				mark.At = at
			}
			break
		}
	}
}

func (r *recorder) marked() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.marks)
}

func (r *recorder) firstOpErr() string {
	for _, o := range r.snapshot().Ops {
		if o.Err != "" {
			return o.Err
		}
	}
	return ""
}
