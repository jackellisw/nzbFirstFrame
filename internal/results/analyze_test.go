package results

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

func ms(v float64) time.Duration { return time.Duration(v * float64(time.Millisecond)) }

func seekPoints(t *testing.T, s string) []config.SeekPoint {
	t.Helper()
	pts, err := config.ParseSeekPoints(s)
	if err != nil {
		t.Fatal(err)
	}
	return pts
}

func TestNewPlanKnownDuration(t *testing.T) {
	plan, err := NewPlan(seekPoints(t, "50%,30s,100%,02:00"), 100*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	want := []Seek{
		{Point: "50%", Offset: 50 * time.Second, Fraction: 0.5},
		{Point: "30s", Offset: 30 * time.Second, Fraction: 0.3},
		{Point: "100%", Offset: 99 * time.Second, Fraction: 0.99},  // clamped: no frame past EOF
		{Point: "02:00", Offset: 99 * time.Second, Fraction: 0.99}, // clamped
	}
	if !slices.Equal(plan.Seeks, want) {
		t.Fatalf("seeks = %+v\nwant   %+v", plan.Seeks, want)
	}
	if plan.Len() != 5 || plan.Label(0) != "startup" || plan.Label(2) != "seek 30s" {
		t.Fatalf("Len=%d Label(0)=%q Label(2)=%q", plan.Len(), plan.Label(0), plan.Label(2))
	}
}

func TestNewPlanUnknownDurationPercentOnly(t *testing.T) {
	plan, err := NewPlan(seekPoints(t, "25%,90%"), 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []Seek{{Point: "25%", Fraction: 0.25}, {Point: "90%", Fraction: 0.9}}
	if !slices.Equal(plan.Seeks, want) {
		t.Fatalf("seeks = %+v, want %+v", plan.Seeks, want)
	}
}

func TestNewPlanUnknownDurationAbsoluteErrors(t *testing.T) {
	_, err := NewPlan(seekPoints(t, "25%,90s"), 0)
	if !errors.Is(err, ErrNoDuration) {
		t.Fatalf("err = %v, want ErrNoDuration", err)
	}
}

func TestNewPlanNoSeeksIsStartupOnly(t *testing.T) {
	plan, err := NewPlan(nil, 0)
	if err != nil || plan.Len() != 1 {
		t.Fatalf("plan=%+v err=%v, want startup only", plan, err)
	}
}

func durEq(t *testing.T, what string, got, want time.Duration) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// The prototype's MKV session: one read to first frame, then the first seek
// pays three round trips (cues at the end of the file, seek head, target).
func TestAnalyzeStartupAndSeeks(t *testing.T) {
	tl := Timeline{
		Ops: []Op{
			{Kind: OpRead, Start: ms(25), FirstByte: ms(225), End: ms(300), Bytes: 851968, Abandoned: true},
			{Kind: OpRead, Start: ms(243.5), FirstByte: ms(443.5), End: ms(445), Bytes: 262144},
			{Kind: OpRead, Start: ms(445.5), FirstByte: ms(645.5), End: ms(647), Bytes: 65536},
			{Kind: OpRead, Start: ms(647.5), FirstByte: ms(847.5), End: ms(900), Bytes: 1048576, Abandoned: true},
			{Kind: OpRead, Start: ms(855), FirstByte: ms(1055), End: ms(1060), Bytes: 4096},
		},
		Marks: []Mark{{Phase: 0, At: ms(243)}, {Phase: 1, At: ms(854)}, {Phase: 2, At: ms(1062)}},
	}
	plan := Plan{Seeks: []Seek{{Point: "30s"}, {Point: "60s"}}}
	a := Analyze(tl, plan)
	if len(a.Phases) != 3 {
		t.Fatalf("phases = %d, want 3", len(a.Phases))
	}

	st := a.Phases[0]
	if !st.OK || !slices.Equal(st.Ops, []int{0}) || st.Requests != 1 || st.Bytes != 851968 {
		t.Fatalf("startup = %+v", st)
	}
	durEq(t, "startup latency (mark0 - first request)", st.Latency, ms(218))
	durEq(t, "startup wait", st.Wait, ms(200))
	durEq(t, "startup transfer (clipped at mark)", st.Transfer, ms(18))
	durEq(t, "startup client", st.Client, 0)

	s1 := a.Phases[1]
	if !s1.OK || !slices.Equal(s1.Ops, []int{1, 2, 3}) || s1.Requests != 3 || s1.Bytes != 262144+65536+1048576 {
		t.Fatalf("seek 1 = %+v", s1)
	}
	durEq(t, "seek 1 latency (mark1 - mark0)", s1.Latency, ms(611))
	durEq(t, "seek 1 wait", s1.Wait, ms(600))
	durEq(t, "seek 1 transfer", s1.Transfer, ms(1.5+1.5+6.5))
	durEq(t, "seek 1 client", s1.Client, ms(611-600-9.5))

	s2 := a.Phases[2]
	durEq(t, "seek 2 latency", s2.Latency, ms(208))
	if !slices.Equal(s2.Ops, []int{4}) {
		t.Fatalf("seek 2 ops = %v, want [4]", s2.Ops)
	}
	if a.TailBytes != 0 || a.Bytes != 851968+262144+65536+1048576+4096 {
		t.Fatalf("tail=%d bytes=%d", a.TailBytes, a.Bytes)
	}
}

func TestAnalyzeOpExactlyAtMarkBelongsToClosingPhase(t *testing.T) {
	tl := Timeline{
		Ops: []Op{
			{Kind: OpRead, Start: ms(10), FirstByte: ms(20), End: ms(30), Bytes: 1},
			{Kind: OpRead, Start: ms(30), FirstByte: ms(40), End: ms(45), Bytes: 2},
			{Kind: OpRead, Start: ms(30) + 1, FirstByte: ms(50), End: ms(60), Bytes: 4},
		},
		Marks: []Mark{{Phase: 0, At: ms(30)}, {Phase: 1, At: ms(60)}},
	}
	a := Analyze(tl, Plan{Seeks: []Seek{{Point: "50%"}}})
	if !slices.Equal(a.Phases[0].Ops, []int{0, 1}) || !slices.Equal(a.Phases[1].Ops, []int{2}) {
		t.Fatalf("attribution = %v / %v, want [0 1] / [2]", a.Phases[0].Ops, a.Phases[1].Ops)
	}
}

func TestAnalyzeTailBytes(t *testing.T) {
	tl := Timeline{
		Ops: []Op{
			{Kind: OpRead, Start: ms(10), FirstByte: ms(20), End: ms(80), Bytes: 1000, Abandoned: true},
			{Kind: OpRead, Start: ms(51), FirstByte: ms(70), End: ms(80), Bytes: 300, Abandoned: true},
			{Kind: OpRead, Start: ms(60), End: ms(80), Abandoned: true},
		},
		Marks: []Mark{{Phase: 0, At: ms(50)}},
	}
	a := Analyze(tl, Plan{})
	if a.TailBytes != 300 || a.Bytes != 1300 || a.Phases[0].Bytes != 1000 {
		t.Fatalf("tail=%d bytes=%d phase0=%d, want 300/1300/1000", a.TailBytes, a.Bytes, a.Phases[0].Bytes)
	}
}

func TestAnalyzeOverlappingOpsTransferWins(t *testing.T) {
	tl := Timeline{
		Ops: []Op{
			{Kind: OpRead, Start: ms(1), FirstByte: ms(2), End: ms(3), Bytes: 1},
			{Kind: OpRead, Start: ms(10), FirstByte: ms(50), End: ms(80), Bytes: 1},
			{Kind: OpRead, Start: ms(30), FirstByte: ms(60), End: ms(70), Bytes: 1},
			{Kind: OpRead, Start: ms(85), End: ms(95), Err: "reset"}, // no byte ever: waits throughout
		},
		Marks: []Mark{{Phase: 0, At: ms(5)}, {Phase: 1, At: ms(100)}},
	}
	p := Analyze(tl, Plan{Seeks: []Seek{{Point: "10%"}}}).Phases[1]
	durEq(t, "latency", p.Latency, ms(95))
	durEq(t, "transfer = |[50,80)|", p.Transfer, ms(30))
	durEq(t, "wait = |[10,50)| + |[85,95)|", p.Wait, ms(50))
	durEq(t, "client", p.Client, ms(15))
	if p.Requests != 3 {
		t.Fatalf("requests = %d, want 3", p.Requests)
	}
}

func TestAnalyzeUsenetConnectAuthCountTowardStartupButNotRequests(t *testing.T) {
	tl := Timeline{
		Ops: []Op{
			{Kind: OpConnect, Start: ms(1), End: ms(41), Offset: -1},
			{Kind: OpAuth, Start: ms(41), End: ms(81), Offset: -1},
			{Kind: OpArticle, Start: ms(81), FirstByte: ms(201), End: ms(286), Bytes: 768000},
		},
		Marks: []Mark{{Phase: 0, At: ms(286), PTS: -1}},
	}
	p := Analyze(tl, Plan{}).Phases[0]
	durEq(t, "latency", p.Latency, ms(285))
	durEq(t, "wait", p.Wait, ms(200))
	durEq(t, "transfer", p.Transfer, ms(85))
	durEq(t, "client", p.Client, 0)
	if p.Requests != 1 || p.Bytes != 768000 || len(p.Ops) != 3 {
		t.Fatalf("requests=%d bytes=%d ops=%v", p.Requests, p.Bytes, p.Ops)
	}
}

func TestAnalyzeFailedSampleOnlyStartupMarked(t *testing.T) {
	tl := Timeline{
		Ops: []Op{
			{Kind: OpRead, Start: ms(20), FirstByte: ms(220), End: ms(240), Bytes: 500},
			{Kind: OpRead, Start: ms(241), End: ms(60000), Err: "context deadline exceeded"},
		},
		Marks: []Mark{{Phase: 0, At: ms(238)}},
	}
	a := Analyze(tl, Plan{Seeks: []Seek{{Point: "10%"}, {Point: "50%"}}})
	if len(a.Phases) != 3 || !a.Phases[0].OK {
		t.Fatalf("phases = %+v", a.Phases)
	}
	durEq(t, "startup latency", a.Phases[0].Latency, ms(218))
	for _, p := range a.Phases[1:] {
		if p.OK || p.Latency != 0 || len(p.Ops) != 0 || p.Phase == 0 {
			t.Fatalf("unmarked phase = %+v, want !OK, zero, no ops", p)
		}
	}
}

func TestAnalyzeEmptyTimeline(t *testing.T) {
	a := Analyze(Timeline{}, Plan{Seeks: []Seek{{Point: "10%"}}})
	if len(a.Phases) != 2 || a.Phases[0].OK || a.Phases[1].OK || a.Phases[1].Phase != 1 || a.Bytes != 0 {
		t.Fatalf("analysis = %+v", a)
	}
}
