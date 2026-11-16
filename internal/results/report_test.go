package results

import (
	"slices"
	"testing"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

// session builds a one-read-per-phase timeline whose phase latencies are
// exactly lat (ms). Each read's TTFB is its phase latency minus 2ms.
func session(lat ...float64) Timeline {
	var tl Timeline
	prev := ms(10)
	for k, l := range lat {
		start := prev
		if k > 0 {
			start = prev + ms(0.5)
		}
		end := prev + ms(l)
		if k == 0 {
			end = start + ms(l)
		}
		tl.Ops = append(tl.Ops, Op{Kind: OpRead, Start: start, FirstByte: end - ms(2), End: end, Bytes: 1000})
		tl.Marks = append(tl.Marks, Mark{Phase: k, At: end, PTS: -1})
		prev = end
	}
	tl.End = prev + ms(5)
	return tl
}

func TestSummarize(t *testing.T) {
	plan := Plan{Seeks: []Seek{{Point: "50%", Offset: 50 * time.Second, Fraction: 0.5}}}
	run := &Run{
		ID: "20261116T101502Z-3f9a1c", Status: StatusDone,
		Targets: []Target{{Name: "foo", Plan: plan}, {Name: "bar", Err: "duration probe: timeout"}},
		Series: []Series{
			{SeriesKey: SeriesKey{"foo", config.LayerRclone}, Samples: []Sample{
				{Iteration: 0, Warmup: true, Timeline: session(9000, 9000)},
				{Iteration: 1, Warmup: true, Timeline: session(9000), Err: "boom"},
				{Iteration: 0, Timeline: session(300, 50)},
				{Iteration: 1, Timeline: session(100, 70)},
				{Iteration: 2, Timeline: session(500), Err: "timeout after 60s"},
				{Iteration: 3, Timeline: session(200, 60)},
				{Iteration: 4, Timeline: session(400, 80)},
			}},
			{SeriesKey: SeriesKey{"foo", config.LayerUsenet}},
			{SeriesKey: SeriesKey{"bar", config.LayerRclone}},
		},
	}
	rep := Summarize(run)

	if rep.Run.ID != run.ID || len(rep.Targets) != 2 {
		t.Fatalf("header/targets = %+v / %d", rep.Run, len(rep.Targets))
	}
	var keys []SeriesKey
	for _, c := range rep.Cells {
		keys = append(keys, c.SeriesKey)
	}
	if want := []SeriesKey{{"foo", config.LayerRclone}, {"foo", config.LayerUsenet}, {"bar", config.LayerRclone}}; !slices.Equal(keys, want) {
		t.Fatalf("cells = %v, want stored series order %v", keys, want)
	}

	foo := rep.Cells[0]
	if len(foo.Samples) != 7 || len(foo.Samples[2].Analysis.Phases) != 2 {
		t.Fatalf("samples = %d, want all 7 with analysis", len(foo.Samples))
	}
	startup := foo.Phases[0]
	want := PhaseStats{
		Phase: 0, Label: "startup", N: 5, Failed: 0,
		Min: ms(100), P50: ms(300), P90: ms(500), Max: ms(500), Mean: ms(300),
		Requests: 1, Bytes: 1000, TTFB: ms(298),
	}
	if startup != want {
		t.Errorf("startup = %+v\nwant      %+v", startup, want)
	}
	seek := foo.Phases[1]
	want = PhaseStats{
		Phase: 1, Label: "seek 50%", N: 4, Failed: 1,
		Min: ms(50), P50: ms(60), P90: ms(80), Max: ms(80), Mean: ms(65),
		Requests: 1, Bytes: 1000, TTFB: ms(57.5),
	}
	if seek != want {
		t.Errorf("seek = %+v\nwant   %+v", seek, want)
	}

	empty := rep.Cells[1]
	if len(empty.Phases) != 2 || empty.Phases[1].Label != "seek 50%" || empty.Phases[0].N != 0 || len(empty.Samples) != 0 {
		t.Errorf("empty series cell = %+v, want the plan's phases with N=0", empty)
	}
	if unplanned := rep.Cells[2]; len(unplanned.Phases) != 1 || unplanned.Phases[0].Label != "startup" {
		t.Errorf("unplanned target cell = %+v, want startup only", unplanned)
	}

	tr := rep.Trend()
	wantPts := []TrendPoint{
		{Target: "foo", Layer: config.LayerRclone, Label: "startup", P50: ms(300), N: 5},
		{Target: "foo", Layer: config.LayerRclone, Label: "seek 50%", P50: ms(60), N: 4},
	}
	if tr.ID != run.ID || !slices.Equal(tr.Points, wantPts) {
		t.Errorf("trend = %+v, want %+v", tr, wantPts)
	}
}

func TestNearestRank(t *testing.T) {
	xs := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	for _, c := range []struct{ p, want int64 }{{50, 5}, {90, 9}, {91, 10}, {100, 10}, {1, 1}} {
		if got := nearestRank(xs, int(c.p)); got != c.want {
			t.Errorf("p%d = %d, want %d", c.p, got, c.want)
		}
	}
	if got := nearestRank([]int64{7}, 90); got != 7 {
		t.Errorf("single p90 = %d, want 7", got)
	}
}
