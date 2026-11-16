package probe

import (
	"sync"
	"testing"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/results"
)

const msec = time.Millisecond

func TestSnapshotPutsALateMarkBeforeTheRequestSentAfterIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ops   []results.Op
		marks []time.Duration
		want  []time.Duration
	}{
		{
			name: "mark stamped after the next request started",
			ops: []results.Op{
				{Start: 20 * msec, FirstByte: 170 * msec, End: 180 * msec},
				{Start: 200 * msec, FirstByte: 350 * msec, End: 360 * msec},
			},
			marks: []time.Duration{201 * msec, 365 * msec},
			want:  []time.Duration{200*msec - 1, 365 * msec},
		},
		{
			name: "marks already before the next request are kept",
			ops: []results.Op{
				{Start: 20 * msec, FirstByte: 170 * msec, End: 400 * msec},
				{Start: 200 * msec, FirstByte: 350 * msec, End: 360 * msec},
			},
			marks: []time.Duration{199 * msec, 365 * msec},
			want:  []time.Duration{199 * msec, 365 * msec},
		},
		{
			name: "request that never got a byte",
			ops: []results.Op{
				{Start: 20 * msec, FirstByte: 170 * msec, End: 180 * msec},
				{Start: 200 * msec, End: 500 * msec, Abandoned: true},
			},
			marks: []time.Duration{203 * msec},
			want:  []time.Duration{200*msec - 1},
		},
		{
			name: "request that failed before the mark belongs to its phase",
			ops: []results.Op{
				{Start: 20 * msec, End: 30 * msec, Err: "416"},
				{Start: 40 * msec, FirstByte: 170 * msec, End: 180 * msec},
			},
			marks: []time.Duration{190 * msec},
			want:  []time.Duration{190 * msec},
		},
		{
			name: "a mark is never moved to or before the previous one",
			ops: []results.Op{
				{Start: 20 * msec, FirstByte: 170 * msec, End: 180 * msec},
				{Start: 200 * msec, FirstByte: 350 * msec, End: 360 * msec},
			},
			marks: []time.Duration{210 * msec, 211 * msec},
			want:  []time.Duration{200*msec - 1, 211 * msec},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newRecorder()
			for _, op := range tc.ops {
				rec.op(op)
			}
			for _, at := range tc.marks {
				rec.mark(at, 0)
			}
			tl := rec.snapshot()
			for k, m := range tl.Marks {
				if m.At != tc.want[k] || m.Phase != k {
					t.Errorf("mark %d: phase %d at %v, want phase %d at %v", k, m.Phase, m.At, k, tc.want[k])
				}
			}
		})
	}
}

func TestSnapshotSortsOpsAndStampsAreStrictlyIncreasing(t *testing.T) {
	rec := newRecorder()
	var wg sync.WaitGroup
	stamps := make([][]time.Duration, 8)
	for g := range stamps {
		wg.Go(func() {
			for range 2000 {
				stamps[g] = append(stamps[g], rec.stamp())
			}
		})
	}
	wg.Wait()
	seen := map[time.Duration]bool{}
	for _, s := range stamps {
		for i, at := range s {
			if seen[at] || (i > 0 && at <= s[i-1]) {
				t.Fatalf("stamp %v handed out twice or out of order", at)
			}
			seen[at] = true
		}
	}

	rec.op(results.Op{Start: 30, End: 40})
	rec.op(results.Op{Start: 10, End: 50})
	rec.op(results.Op{Start: 20, End: 25})
	tl := rec.snapshot()
	if len(tl.Ops) != 3 || tl.Ops[0].Start != 10 || tl.Ops[1].Start != 20 || tl.Ops[2].Start != 30 {
		t.Errorf("ops %+v, want them sorted by Start", tl.Ops)
	}
	if tl.End <= stamps[0][len(stamps[0])-1] {
		t.Errorf("End %v is not after the last stamp", tl.End)
	}
	if empty := newRecorder().snapshot(); empty.Ops == nil || empty.Marks == nil {
		t.Error("an empty snapshot has nil Ops or Marks; it would store as null")
	}
}
