package results

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

var update = flag.Bool("update", false, "rewrite testdata/run.json from buildFixture")

const fixturePath = "testdata/run.json"

// The fixture is a realistic stored run for the dashboard and these tests:
// two targets (an MKV with lazy cues, an MP4 with moov at the end), all four
// layers, 2 warmups + 5 measured iterations, three seek points, and four
// failures. Phase latencies follow from the constants below, so expected
// statistics can be computed by hand:
//
//	video startup (MKV)  = ttfb + 18 + 2               one read
//	video startup (MP4)  = 3*ttfb + 1.5+0.4+24+3+10+2  ftyp, moov, mdat
//	MKV seek 1           = 3*ttfb + 0.3+4+0.5+2+0.4+12+2  cues, seek head, target
//	other video seeks    = ttfb + 0.3 + 12 + 2
//	usenet startup       = 38.6 + 0.1 + 41.2 + 0.2 + ttfb + 86  connect, auth, BODY
//	usenet seeks         = 0.2 + ttfb + 86
//
// where ttfb = layer base + iteration jitter (ms).
var (
	fixtureBaseTTFB = map[config.Layer]float64{
		config.LayerJellyfin: 262, config.LayerRclone: 228, config.LayerInfinidysk: 183, config.LayerUsenet: 118,
	}
	fixtureDial          = map[config.Layer]float64{config.LayerJellyfin: 1.8, config.LayerInfinidysk: 0.6}
	fixtureWarmupJitter  = []float64{140, 55}
	fixtureMeasureJitter = []float64{0, 14, -6, 31, 8}
)

type fixtureMedia struct {
	target config.Target
	mp4    bool
	size   int64
	dur    time.Duration
}

type fault int

const (
	noFault fault = iota
	faultTimeoutSeek1
	faultTruncatedSeek1
	faultHTTP503
	faultArticle430
)

type sampleID struct {
	target string
	layer  config.Layer
	warmup bool
	iter   int
}

var fixtureFaults = map[sampleID]fault{
	{"foo", config.LayerJellyfin, false, 3}:          faultTimeoutSeek1,
	{"bar-s01e02", config.LayerInfinidysk, false, 1}: faultTruncatedSeek1,
	{"foo", config.LayerUsenet, false, 4}:            faultArticle430,
	{"bar-s01e02", config.LayerJellyfin, true, 0}:    faultHTTP503,
}

const (
	segmentSize  = 716800 // decoded bytes per article
	articleBytes = 739870 // yEnc-encoded body as delivered
)

func buildFixture() Run {
	started := time.Date(2026, 11, 16, 3, 15, 2, 0, time.UTC)
	points, err := config.ParseSeekPoints("10%,50%,00:45:00")
	if err != nil {
		panic(err)
	}
	media := []fixtureMedia{
		{target: config.Target{Name: "foo", Path: "movies/Foo (2024)/Foo (2024).mkv", JellyfinItemID: "a1b2c3d4e5f60718"},
			size: 4_509_715_660, dur: 2*time.Hour + 5*time.Minute + 12*time.Second},
		{target: config.Target{Name: "bar-s01e02", Path: "tv/Bar/Season 01/Bar - S01E02.mp4", JellyfinItemID: "f6e5d4c3b2a10987"},
			mp4: true, size: 1_395_864_371, dur: 41*time.Minute + 37*time.Second},
	}
	run := Run{
		Schema: SchemaVersion, ID: "20261116T031502Z-7c41e9", Status: StatusDone, Started: started,
		Host: "mediabox", Version: "v0.3.0", Mode: "all",
		Settings: Settings{
			Layers: config.AllLayers, Iterations: 5, Warmup: 2, Timeout: time.Minute, Cooldown: 2 * time.Second,
			ColdCache: true, SeekPoints: []string{"10%", "50%", "00:45:00"},
		},
	}
	seq := 0
	for _, m := range media {
		plan, err := NewPlan(points, m.dur)
		if err != nil {
			panic(err)
		}
		run.Targets = append(run.Targets, Target{
			Name: m.target.Name, Path: m.target.Path, ItemID: m.target.JellyfinItemID, Duration: m.dur, Plan: plan,
		})
		first := len(run.Series)
		for _, l := range config.AllLayers {
			run.Series = append(run.Series, Series{SeriesKey: SeriesKey{m.target.Name, l}})
		}
		type step struct {
			warmup bool
			iter   int
			jitter float64
		}
		var steps []step
		for i, j := range fixtureWarmupJitter {
			steps = append(steps, step{true, i, j})
		}
		for i, j := range fixtureMeasureJitter {
			steps = append(steps, step{false, i, j})
		}
		for _, st := range steps {
			for li, l := range config.AllLayers {
				id := sampleID{m.target.Name, l, st.warmup, st.iter}
				ttfb := fixtureBaseTTFB[l] + st.jitter
				f := fixtureFaults[id]
				var tl Timeline
				var serr string
				if l == config.LayerUsenet {
					tl, serr = usenetSession(m, plan, ttfb, f)
				} else {
					slowExit := st.warmup && st.iter == 0 || !st.warmup && st.iter == 3
					tl, serr = videoSession(m, plan, ttfb, fixtureDial[l], f, slowExit)
				}
				s := &run.Series[first+li]
				s.Samples = append(s.Samples, Sample{
					Iteration: st.iter, Warmup: st.warmup, At: started.Add(time.Duration(seq) * 9 * time.Second),
					Timeline: tl, Err: serr,
				})
				seq++
			}
		}
	}
	fin := started.Add(time.Duration(seq)*9*time.Second + 4*time.Second)
	run.Finished = &fin
	return run
}

// keyframePTS is where a seek to off lands: the keyframe at or before it,
// with a 48-frame GOP at 23.976 fps.
func keyframePTS(off time.Duration) float64 {
	const gop = 48 / 23.976
	return math.Round(math.Floor(off.Seconds()/gop)*gop*1000) / 1000
}

func videoSession(m fixtureMedia, plan Plan, ttfb, dial float64, f fault, slowExit bool) (Timeline, string) {
	var tl Timeline
	now := 24.8 // ffprobe's own startup before its first request
	read := func(off int64, gap, transfer float64, n int64) int {
		start := now + gap
		fb := start + ttfb
		tl.Ops = append(tl.Ops, Op{Kind: OpRead, Start: ms(start), FirstByte: ms(fb), End: ms(fb + transfer),
			Offset: off, Bytes: n, Ref: fmt.Sprintf("bytes=%d-", off)})
		now = fb + transfer
		return len(tl.Ops) - 1
	}
	stall := func(off int64, gap, until float64, err string) {
		tl.Ops = append(tl.Ops, Op{Kind: OpRead, Start: ms(now + gap), End: ms(until), Offset: off,
			Ref: fmt.Sprintf("bytes=%d-", off), Err: err})
		now = until
	}
	mark := func(pts float64) {
		now += 2 // decode one frame
		tl.Marks = append(tl.Marks, Mark{Phase: len(tl.Marks), At: ms(now), PTS: pts})
	}
	abandon := func(i int, at float64) {
		tl.Ops[i].End = ms(at)
		tl.Ops[i].Abandoned = true
	}

	if f == faultHTTP503 {
		stall(0, 0, now+41, "upstream status 503 Service Unavailable")
		tl.Ops[0].Dial = ms(dial)
		tl.End = ms(now + 6)
		return tl, "ffprobe: exit status 1: http://127.0.0.1:41873/Videos/" + m.target.JellyfinItemID +
			"/stream: Server returned 5XX Server Error reply"
	}

	var open int
	if m.mp4 {
		ftyp := read(0, 0, 1.5, 32768)
		abandon(ftyp, now)
		read(m.size-3_145_728, 0.4, 24, 3_145_728) // moov at the end
		open = read(48, 3, 10, 851968)
		tl.Ops[ftyp].Dial = ms(dial)
	} else {
		open = read(0, 0, 18, 851968)
		tl.Ops[open].Dial = ms(dial)
	}
	mark(0)

	for k, s := range plan.Seeks {
		abandon(open, now+0.3)
		off := int64(s.Fraction * float64(m.size))
		gap := 0.3
		if k == 0 {
			switch {
			case f == faultTimeoutSeek1:
				stall(m.size-2_874_112, 0.3, 60_000, "context deadline exceeded")
				tl.End = ms(now + 0.4)
				return tl, fmt.Sprintf("timeout after 1m0s in phase %q (ffprobe: signal: killed)", plan.Label(1))
			case f == faultTruncatedSeek1:
				i := read(off, 0.3, 1.2, 16384)
				tl.Ops[i].Err = "unexpected EOF"
				tl.End = ms(now + 3.1)
				return tl, fmt.Sprintf("ffprobe: exit status 1: [mov,mp4,m4a,3gp,3g2,mj2 @ 0x6000038f0000] "+
					"stream 0, offset 0x%x: partial file", off)
			case !m.mp4:
				read(m.size-2_874_112, 0.3, 4, 262144) // lazy MKV cues at the end of the file
				sh := read(104213, 0.5, 2, 65536)
				abandon(sh, now)
				gap = 0.4
			}
		}
		open = read(off, gap, 12, 917504)
		mark(keyframePTS(s.Offset))
	}

	tailStart := now + 0.6
	if slowExit {
		tl.Ops = append(tl.Ops, Op{Kind: OpRead, Start: ms(tailStart), FirstByte: ms(tailStart + ttfb),
			End: ms(tailStart + ttfb + 5), Offset: tl.Ops[open].Offset + 917504, Bytes: 393216, Abandoned: true,
			Ref: fmt.Sprintf("bytes=%d-", tl.Ops[open].Offset+917504)})
		now = tailStart + ttfb + 5
	} else {
		tl.Ops = append(tl.Ops, Op{Kind: OpRead, Start: ms(tailStart), End: ms(now + 9),
			Offset: tl.Ops[open].Offset + 917504, Abandoned: true,
			Ref: fmt.Sprintf("bytes=%d-", tl.Ops[open].Offset+917504)})
		now += 9
	}
	abandon(open, now)
	tl.End = ms(now + 1)
	return tl, ""
}

func usenetSession(m fixtureMedia, plan Plan, ttfb float64, f fault) (Timeline, string) {
	var tl Timeline
	segs := int((m.size + segmentSize - 1) / segmentSize)
	ref := func(seg int) string { return fmt.Sprintf("<part%dof%d.Xq3vT9@news.example>", seg+1, segs) }
	now := 0.4
	tl.Ops = append(tl.Ops,
		Op{Kind: OpConnect, Start: ms(now), End: ms(now + 38.6), Offset: -1, Ref: "news.example.net:563"},
		Op{Kind: OpAuth, Start: ms(now + 38.7), End: ms(now + 79.9), Offset: -1, Ref: "AUTHINFO USER"})
	now += 79.9
	article := func(seg int) {
		start := now + 0.2
		tl.Ops = append(tl.Ops, Op{Kind: OpArticle, Start: ms(start), FirstByte: ms(start + ttfb),
			End: ms(start + ttfb + 86), Offset: int64(seg) * segmentSize, Bytes: articleBytes, Ref: ref(seg)})
		now = start + ttfb + 86
		tl.Marks = append(tl.Marks, Mark{Phase: len(tl.Marks), At: ms(now), PTS: -1})
	}
	article(0)
	serr := ""
	for k, s := range plan.Seeks {
		seg := min(int(s.Fraction*float64(segs)), segs-1)
		if f == faultArticle430 && k == len(plan.Seeks)-1 {
			tl.Ops = append(tl.Ops, Op{Kind: OpArticle, Start: ms(now + 0.2), End: ms(now + 108.2),
				Offset: int64(seg) * segmentSize, Ref: ref(seg), Err: "430 No Such Article"})
			now += 108.2
			serr = "nntp: BODY " + ref(seg) + ": 430 No Such Article"
			break
		}
		article(seg)
	}
	tl.End = ms(now + 0.8)
	return tl, serr
}

func encodeFixture(t *testing.T) []byte {
	t.Helper()
	b, err := json.MarshalIndent(buildFixture(), "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func TestFixtureIsUpToDate(t *testing.T) {
	want := encodeFixture(t)
	if *update {
		if err := os.WriteFile(fixturePath, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale: run go test ./internal/results -run TestFixtureIsUpToDate -update", fixturePath)
	}
}

func loadFixture(t *testing.T) *Run {
	t.Helper()
	b, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var run Run
	if err := json.Unmarshal(b, &run); err != nil {
		t.Fatal(err)
	}
	return &run
}

func TestFixtureHonorsTimelineInvariants(t *testing.T) {
	for _, se := range loadFixture(t).Series {
		for _, s := range se.Samples {
			tl := s.Timeline
			name := fmt.Sprintf("%s/%s warmup=%v #%d", se.Target, se.Layer, s.Warmup, s.Iteration)
			if !slices.IsSortedFunc(tl.Ops, func(a, b Op) int { return int(a.Start - b.Start) }) {
				t.Errorf("%s: ops not sorted by Start", name)
			}
			for i, op := range tl.Ops {
				if op.Start > op.End || (op.FirstByte != 0 && (op.FirstByte <= op.Start || op.FirstByte > op.End)) || op.End > tl.End {
					t.Errorf("%s: op %d violates Start < FirstByte <= End <= tl.End: %+v", name, i, op)
				}
			}
			for i, mk := range tl.Marks {
				if mk.Phase != i || (i > 0 && mk.At <= tl.Marks[i-1].At) {
					t.Errorf("%s: mark %d = %+v", name, i, mk)
				}
			}
		}
	}
}

func TestSummarizeFixture(t *testing.T) {
	run := loadFixture(t)
	failed := 0
	for _, se := range run.Series {
		for _, s := range se.Samples {
			if s.Err != "" {
				failed++
			}
		}
	}
	if failed != len(fixtureFaults) {
		t.Fatalf("%d failed samples, want one per fixtureFaults entry (%d)", failed, len(fixtureFaults))
	}
	rep := Summarize(run)

	if len(rep.Cells) != 8 {
		t.Fatalf("cells = %d, want 2 targets x 4 layers", len(rep.Cells))
	}
	cells := map[SeriesKey]Cell{}
	for i, c := range rep.Cells {
		if want := config.AllLayers[i%4]; c.Layer != want {
			t.Errorf("cell %d layer = %s, want %s (stack order)", i, c.Layer, want)
		}
		if len(c.Samples) != 7 || len(c.Phases) != 4 {
			t.Errorf("%v: %d samples, %d phases; want 7 and 4", c.SeriesKey, len(c.Samples), len(c.Phases))
		}
		cells[c.SeriesKey] = c
	}
	phase := func(target string, l config.Layer, k int) PhaseStats { return cells[SeriesKey{target, l}].Phases[k] }

	// infinidysk ttfb = 183 + {0,14,-6,31,8}
	startup := phase("foo", config.LayerInfinidysk, 0)
	want := PhaseStats{Phase: 0, Label: "startup", N: 5,
		Min: ms(197), P50: ms(211), P90: ms(234), Max: ms(234), Mean: ms(212.4),
		Requests: 1, Bytes: 851968, TTFB: ms(191)}
	if !closeStats(startup, want) {
		t.Errorf("foo/infinidysk startup = %+v\nwant                       %+v", startup, want)
	}
	cues := phase("foo", config.LayerInfinidysk, 1)
	if cues.Label != "seek 10%" || cues.N != 5 || !near(cues.P50, ms(3*191+21.2)) || cues.Requests != 3 || cues.Bytes != 262144+65536+917504 {
		t.Errorf("foo/infinidysk seek 10%% (MKV cues) = %+v", cues)
	}
	if s := phase("foo", config.LayerInfinidysk, 2); !near(s.P50, ms(191+14.3)) || s.Requests != 1 {
		t.Errorf("foo/infinidysk seek 50%% = %+v", s)
	}
	if s := phase("bar-s01e02", config.LayerInfinidysk, 0); !near(s.P50, ms(3*191+40.9)) || s.Requests != 3 {
		t.Errorf("bar/infinidysk startup (MP4 moov at end) = %+v", s)
	}
	if s := phase("foo", config.LayerUsenet, 0); !near(s.P50, ms(166.1+126)) || s.Requests != 1 || s.Bytes != articleBytes || !near(s.TTFB, ms(126)) {
		t.Errorf("foo/usenet startup = %+v", s)
	}

	for _, c := range []struct {
		target    string
		layer     config.Layer
		k, n, bad int
	}{
		{"foo", config.LayerJellyfin, 0, 5, 0},          // timeout came after startup
		{"foo", config.LayerJellyfin, 1, 4, 1},          // ... in seek 10%
		{"foo", config.LayerJellyfin, 3, 4, 1},          // later phases never ran
		{"foo", config.LayerUsenet, 2, 5, 0},            // 430 only on the last seek
		{"foo", config.LayerUsenet, 3, 4, 1},            //
		{"bar-s01e02", config.LayerInfinidysk, 1, 4, 1}, // truncated after startup
		{"bar-s01e02", config.LayerJellyfin, 0, 5, 0},   // failed warmup is not counted
		{"bar-s01e02", config.LayerRclone, 3, 5, 0},
	} {
		if p := phase(c.target, c.layer, c.k); p.N != c.n || p.Failed != c.bad {
			t.Errorf("%s/%s phase %d: N=%d Failed=%d, want %d/%d", c.target, c.layer, c.k, p.N, p.Failed, c.n, c.bad)
		}
	}

	bar := rep.Targets[1].Plan.Seeks[2]
	if bar.Point != "00:45:00" || bar.Offset != 41*time.Minute+36*time.Second {
		t.Errorf("bar 00:45:00 = %+v, want clamped to duration-1s", bar)
	}
}

// near tolerates ms()'s rounding of expected values computed in float.
func near(a, b time.Duration) bool { return (a - b).Abs() <= time.Microsecond }

func closeStats(a, b PhaseStats) bool {
	return a.Phase == b.Phase && a.Label == b.Label && a.N == b.N && a.Failed == b.Failed &&
		near(a.Min, b.Min) && near(a.P50, b.P50) && near(a.P90, b.P90) && near(a.Max, b.Max) && near(a.Mean, b.Mean) &&
		a.Requests == b.Requests && a.Bytes == b.Bytes && near(a.TTFB, b.TTFB)
}
