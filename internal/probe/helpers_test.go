package probe_test

import (
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
	"github.com/jackellisw/nzbfirstframe/internal/probe"
	"github.com/jackellisw/nzbfirstframe/internal/results"
)

const mediaLength = time.Minute

func newProber(cfg *config.Config) *probe.Prober {
	return probe.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func planAt(fractions ...float64) results.Plan {
	var p results.Plan
	for _, f := range fractions {
		p.Seeks = append(p.Seeks, results.Seek{
			Point:    fmt.Sprintf("%g%%", f*100),
			Offset:   time.Duration(f * float64(mediaLength)),
			Fraction: f,
		})
	}
	return p
}

type phase struct {
	latency time.Duration
	ops     []results.Op
}

func phases(tl results.Timeline) []phase {
	out := make([]phase, len(tl.Marks))
	lo := time.Duration(-1)
	for k, m := range tl.Marks {
		for _, op := range tl.Ops {
			if lo < op.Start && op.Start <= m.At {
				out[k].ops = append(out[k].ops, op)
			}
		}
		from := lo
		if k == 0 && len(out[k].ops) > 0 {
			from = out[k].ops[0].Start
		}
		out[k].latency = m.At - from
		lo = m.At
	}
	return out
}

func checkTimelineInvariants(t *testing.T, tl results.Timeline) {
	t.Helper()
	if tl.Ops == nil || tl.Marks == nil {
		t.Errorf("Ops/Marks must be non-nil so they store as []: ops=%v marks=%v", tl.Ops, tl.Marks)
	}
	for i, op := range tl.Ops {
		if i > 0 && op.Start < tl.Ops[i-1].Start {
			t.Errorf("ops not sorted by Start at %d", i)
		}
		if op.Start > op.End || op.End > tl.End {
			t.Errorf("op %d: want Start <= End <= Timeline.End, got %v, %v, %v", i, op.Start, op.End, tl.End)
		}
		if op.FirstByte != 0 && (op.FirstByte <= op.Start || op.FirstByte > op.End) {
			t.Errorf("op %d: want Start < FirstByte <= End, got %v, %v, %v", i, op.Start, op.FirstByte, op.End)
		}
		if op.FirstByte == 0 && op.Bytes != 0 {
			t.Errorf("op %d delivered %d bytes without a FirstByte stamp", i, op.Bytes)
		}
	}
	for i, m := range tl.Marks {
		if m.Phase != i {
			t.Errorf("mark %d numbered %d", i, m.Phase)
		}
		if i > 0 && m.At <= tl.Marks[i-1].At {
			t.Errorf("mark %d at %v is not after mark %d at %v", i, m.At, i-1, tl.Marks[i-1].At)
		}
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
