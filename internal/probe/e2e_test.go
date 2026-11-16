package probe_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
	"github.com/jackellisw/nzbfirstframe/internal/fakestack"
	"github.com/jackellisw/nzbfirstframe/internal/results"
)

const (
	gopLength   = 2 * time.Second
	segmentSize = 750_000
)

func startStack(t *testing.T, opts fakestack.Options) *fakestack.Stack {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	opts.Spec.Length = mediaLength
	opts.SegmentSize = segmentSize
	st, err := fakestack.Start(t.Context(), opts)
	if err != nil {
		t.Fatalf("fakestack.Start: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

var threeSeeks = planAt(0.25, 0.5, 0.75)

func hitsOf(hits []fakestack.Hit, layer config.Layer) []fakestack.Hit {
	var out []fakestack.Hit
	for _, h := range hits {
		if h.Layer == layer {
			out = append(out, h)
		}
	}
	return out
}

func TestVideoLatencyTracksInjectedTTFB(t *testing.T) {
	const (
		ttfb  = 150 * time.Millisecond
		slack = 150 * time.Millisecond
	)
	for _, container := range []string{"mkv", "mp4"} {
		st := startStack(t, fakestack.Options{
			Spec:           fakestack.MediaSpec{Container: container},
			JellyfinTTFB:   ttfb,
			InfinidyskTTFB: ttfb,
		})
		prober := newProber(st.Config())

		for _, layer := range []config.Layer{config.LayerJellyfin, config.LayerInfinidysk} {
			t.Run(container+"/"+string(layer), func(t *testing.T) {
				before := len(st.Hits())
				tl, err := prober.Sample(t.Context(), layer, st.Target, threeSeeks)
				if err != nil {
					t.Fatalf("Sample: %v", err)
				}
				checkTimelineInvariants(t, tl)
				if len(tl.Marks) != threeSeeks.Len() {
					t.Fatalf("got %d marks, want one per phase (%d)", len(tl.Marks), threeSeeks.Len())
				}

				for k, ph := range phases(tl) {
					n := time.Duration(len(ph.ops))
					var waited time.Duration
					for _, op := range ph.ops {
						waited += op.FirstByte - op.Start
					}
					lead := time.Duration(0)
					if k > 0 && n > 0 {
						lead = ph.ops[0].Start - tl.Marks[k-1].At
					}
					t.Logf("LATENCY %s %s phase=%d requests=%d latency=%.1fms over_floor=%.1fms upstream_wait=%.1fms lead=%.2fms",
						container, layer, k, n, ms(ph.latency), ms(ph.latency-n*ttfb), ms(waited), ms(lead))
					if n == 0 {
						t.Errorf("%s: no upstream request", threeSeeks.Label(k))
					}
					if ph.latency < n*ttfb || ph.latency > n*ttfb+slack {
						t.Errorf("%s: latency %v with %d requests, want within [%v, %v]",
							threeSeeks.Label(k), ph.latency, n, n*ttfb, n*ttfb+slack)
					}
					for _, op := range ph.ops {
						if wait := op.FirstByte - op.Start; wait < ttfb {
							t.Errorf("%s: op %s waited %v for its first byte, less than the injected %v",
								threeSeeks.Label(k), op.Ref, wait, ttfb)
						}
					}
				}
				for k, m := range tl.Marks[1:] {
					target := threeSeeks.Seeks[k].Offset.Seconds()
					if m.PTS > target+0.1 || m.PTS < target-gopLength.Seconds()-0.1 {
						t.Errorf("%s: first frame at %.3fs, want the keyframe at or before %.3fs",
							threeSeeks.Label(k+1), m.PTS, target)
					}
				}

				hits := st.Hits()[before:]
				if got := hitsOf(hits, layer); len(got) != len(hits) {
					t.Errorf("sampling %s reached other layers: %+v", layer, hits)
				}
				var asked, served []string
				var delivered int64
				for _, op := range tl.Ops {
					asked = append(asked, op.Ref)
					delivered += op.Bytes
					if op.Kind != results.OpRead {
						t.Errorf("op kind %q on a video layer", op.Kind)
					}
				}
				for _, h := range hits {
					served = append(served, h.Ref)
				}
				slices.Sort(asked)
				slices.Sort(served)
				if !slices.Equal(asked, served) {
					t.Errorf("timeline has %d ops %v, server saw %d requests %v", len(asked), asked, len(served), served)
				}
				if sent := eventuallySent(st, before, delivered); delivered == 0 || delivered > sent {
					t.Errorf("ops delivered %d bytes, server sent %d", delivered, sent)
				}
			})
		}
	}
}

func eventuallySent(st *fakestack.Stack, from int, atLeast int64) int64 {
	deadline := time.Now().Add(2 * time.Second)
	for {
		var sent int64
		for _, h := range st.Hits()[from:] {
			sent += h.Bytes
		}
		if sent >= atLeast || time.Now().After(deadline) {
			return sent
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUsenetTimelineTracksArticleTTFB(t *testing.T) {
	const ttfb = 80 * time.Millisecond
	st := startStack(t, fakestack.Options{ArticleTTFB: ttfb})
	media, err := os.Stat(filepath.Join(st.MountDir, filepath.FromSlash(st.Target.Path)))
	if err != nil {
		t.Fatal(err)
	}

	tl, err := newProber(st.Config()).Sample(t.Context(), config.LayerUsenet, st.Target, threeSeeks)
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	checkTimelineInvariants(t, tl)

	var kinds []results.OpKind
	for _, op := range tl.Ops {
		kinds = append(kinds, op.Kind)
	}
	wantKinds := []results.OpKind{results.OpConnect, results.OpAuth,
		results.OpArticle, results.OpArticle, results.OpArticle, results.OpArticle}
	if !slices.Equal(kinds, wantKinds) {
		t.Fatalf("op kinds %v, want %v", kinds, wantKinds)
	}
	connect, auth, articles := tl.Ops[0], tl.Ops[1], tl.Ops[2:]
	if connect.Ref != st.NNTPAddr || connect.Offset != -1 || auth.Offset != -1 {
		t.Errorf("connect %+v / auth %+v: want Ref %s and Offset -1", connect, auth, st.NNTPAddr)
	}

	wantParts := []int64{0}
	for _, s := range threeSeeks.Seeks {
		wantParts = append(wantParts, int64(s.Fraction*float64(media.Size()))/segmentSize)
	}
	hits := hitsOf(st.Hits(), config.LayerUsenet)
	if len(hits) != len(articles) || len(tl.Marks) != len(articles) {
		t.Fatalf("%d article ops, %d marks, server served %d", len(articles), len(tl.Marks), len(hits))
	}
	for k, op := range articles {
		wantRef := fmt.Sprintf("<part%05d@fakestack>", wantParts[k]+1)
		if op.Ref != wantRef || hits[k].Ref != wantRef {
			t.Errorf("%s: fetched %s (server saw %s), want %s", threeSeeks.Label(k), op.Ref, hits[k].Ref, wantRef)
		}
		if op.Offset != wantParts[k]*segmentSize {
			t.Errorf("%s: offset %d, want %d", threeSeeks.Label(k), op.Offset, wantParts[k]*segmentSize)
		}
		if wait := op.FirstByte - op.Start; wait < ttfb {
			t.Errorf("%s: TTFB %v, less than the injected %v", threeSeeks.Label(k), wait, ttfb)
		}
		if op.Bytes != hits[k].Bytes {
			t.Errorf("%s: body of %d bytes, server sent %d before dot-stuffing", threeSeeks.Label(k), op.Bytes, hits[k].Bytes)
		}
		if m := tl.Marks[k]; m.At != op.End || m.PTS != -1 {
			t.Errorf("%s: mark %+v, want it at the article's end %v with PTS -1", threeSeeks.Label(k), m, op.End)
		}
		t.Logf("LATENCY usenet phase=%d ttfb=%.1fms article=%.1fms bytes=%d", k, ms(op.FirstByte-op.Start), ms(op.End-op.Start), op.Bytes)
	}
	for k, ph := range phases(tl) {
		if ph.latency < ttfb {
			t.Errorf("%s: latency %v, less than one article's injected %v", threeSeeks.Label(k), ph.latency, ttfb)
		}
	}
	if startup := phases(tl)[0]; len(startup.ops) != 3 {
		t.Errorf("startup has %d ops, want connect, auth and the first article", len(startup.ops))
	}
}

func TestRcloneLayerReadsOnlyTheMount(t *testing.T) {
	for _, container := range []string{"mkv", "mp4"} {
		t.Run(container, func(t *testing.T) {
			st := startStack(t, fakestack.Options{Spec: fakestack.MediaSpec{Container: container}})
			tl, err := newProber(st.Config()).Sample(t.Context(), config.LayerRclone, st.Target, threeSeeks)
			if err != nil {
				t.Fatalf("Sample: %v", err)
			}
			checkTimelineInvariants(t, tl)
			if len(tl.Marks) != threeSeeks.Len() {
				t.Fatalf("got %d marks, want one per phase (%d)", len(tl.Marks), threeSeeks.Len())
			}
			for k, ph := range phases(tl) {
				if len(ph.ops) == 0 {
					t.Errorf("%s: no read recorded", threeSeeks.Label(k))
				}
				t.Logf("LATENCY %s rclone phase=%d requests=%d latency=%.1fms", container, k, len(ph.ops), ms(ph.latency))
			}
			for _, op := range tl.Ops {
				if op.Kind != results.OpRead || op.Dial != 0 || op.Err != "" {
					t.Errorf("mount read %+v: want a clean read op with no dial", op)
				}
			}
			if hits := st.Hits(); len(hits) != 0 {
				t.Errorf("the rclone layer reached a server: %+v", hits)
			}
		})
	}
}

func TestMediaUsesDeepestSelectedVideoLayer(t *testing.T) {
	st := startStack(t, fakestack.Options{})
	for _, tc := range []struct {
		layers []config.Layer
		via    config.Layer
	}{
		{config.AllLayers, config.LayerInfinidysk},
		{[]config.Layer{config.LayerJellyfin, config.LayerRclone, config.LayerUsenet}, config.LayerRclone},
		{[]config.Layer{config.LayerJellyfin, config.LayerUsenet}, config.LayerJellyfin},
	} {
		cfg := st.Config()
		cfg.Profile.Layers = tc.layers
		before := len(st.Hits())
		m, err := newProber(cfg).Media(t.Context(), st.Target)
		if err != nil {
			t.Fatalf("layers %v: %v", tc.layers, err)
		}
		if m.Via != tc.via {
			t.Errorf("layers %v: duration read via %s, want %s", tc.layers, m.Via, tc.via)
		}
		if diff := (m.Duration - mediaLength).Abs(); diff > 100*time.Millisecond {
			t.Errorf("layers %v: duration %v, want %v", tc.layers, m.Duration, mediaLength)
		}
		hits := st.Hits()[before:]
		if tc.via != config.LayerRclone && len(hitsOf(hits, tc.via)) == 0 || len(hitsOf(hits, tc.via)) != len(hits) {
			t.Errorf("layers %v: duration probe requests %+v, want only %s", tc.layers, hits, tc.via)
		}
	}

	cfg := st.Config()
	cfg.Profile.Layers = []config.Layer{config.LayerUsenet}
	if m, err := newProber(cfg).Media(t.Context(), st.Target); err == nil {
		t.Errorf("usenet-only: got %+v, want an error because no layer can report a duration", m)
	}

	cfg = st.Config()
	cfg.Infinidysk.URL = "http://" + closedPort(t)
	if m, err := newProber(cfg).Media(t.Context(), st.Target); err != nil || m.Via != config.LayerRclone {
		t.Errorf("infinidysk down: got %+v, %v; want the duration read via rclone, the next layer up", m, err)
	}
	cfg.Rclone.MountPath = t.TempDir()
	cfg.Jellyfin.APIKey = "wrong"
	_, err := newProber(cfg).Media(t.Context(), st.Target)
	for _, l := range []string{"via infinidysk", "via rclone", "via jellyfin"} {
		if err == nil || !strings.Contains(err.Error(), l) {
			t.Errorf("every video layer down: err = %v, want it to report the failure %s", err, l)
		}
	}
}

func TestColdCacheForgetsOnlyForLayersBehindRclone(t *testing.T) {
	st := startStack(t, fakestack.Options{})
	cfg := st.Config()
	cfg.Profile.ColdCache = true
	prober := newProber(cfg)
	forget := fakestack.Hit{Layer: config.LayerRclone, Ref: "vfs/forget file=" + st.Target.Path}

	for layer, want := range map[config.Layer]int{
		config.LayerJellyfin:   1,
		config.LayerRclone:     1,
		config.LayerInfinidysk: 0,
		config.LayerUsenet:     0,
	} {
		before := len(st.Hits())
		tl, err := prober.Sample(t.Context(), layer, st.Target, planAt())
		if err != nil {
			t.Fatalf("%s: %v", layer, err)
		}
		got := 0
		for _, h := range st.Hits()[before:] {
			if h.Layer == forget.Layer && h.Ref == forget.Ref {
				got++
			}
		}
		if got != want {
			t.Errorf("%s: %d vfs/forget calls for the target, want %d", layer, got, want)
		}
		if len(tl.Marks) != 1 {
			t.Errorf("%s: %d marks after a cold-cache sample, want 1", layer, len(tl.Marks))
		}
	}

	cfg.Rclone.RCPassword = "wrong"
	tl, err := prober.Sample(t.Context(), config.LayerRclone, st.Target, planAt())
	if err == nil || !strings.Contains(err.Error(), "vfs/forget") {
		t.Errorf("rc rejecting the password: err = %v, want a vfs/forget failure", err)
	}
	checkTimelineInvariants(t, tl)
	if len(tl.Ops) != 0 || len(tl.Marks) != 0 {
		t.Errorf("sample ran after the cold-cache flush failed: %+v", tl)
	}
}

func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestFailedSamplesReturnErrorAndPartialTimeline(t *testing.T) {
	st := startStack(t, fakestack.Options{InfinidyskTTFB: 200 * time.Millisecond})

	failed := func(t *testing.T, cfg *config.Config, layer config.Layer, target config.Target) (results.Timeline, string) {
		t.Helper()
		tl, err := newProber(cfg).Sample(t.Context(), layer, target, threeSeeks)
		if err == nil {
			t.Fatalf("Sample succeeded: %+v", tl)
		}
		checkTimelineInvariants(t, tl)
		if tl.End <= 0 {
			t.Errorf("Timeline.End = %v, want the sample's end", tl.End)
		}
		return tl, err.Error()
	}
	wantNoMarks := func(t *testing.T, tl results.Timeline, msg string) {
		t.Helper()
		if len(tl.Marks) != 0 {
			t.Errorf("%d marks, want none", len(tl.Marks))
		}
		if !strings.Contains(msg, `"startup"`) {
			t.Errorf("error %q does not name the startup phase", msg)
		}
	}

	t.Run("infinidysk unreachable", func(t *testing.T) {
		cfg := st.Config()
		cfg.Infinidysk.URL = "http://" + closedPort(t)
		tl, msg := failed(t, cfg, config.LayerInfinidysk, st.Target)
		wantNoMarks(t, tl, msg)
		if len(tl.Ops) == 0 {
			t.Fatal("no op records the failed upstream request")
		}
		for _, op := range tl.Ops {
			if op.Err == "" || op.Bytes != 0 {
				t.Errorf("op %+v: want a failed read with no bytes", op)
			}
		}
		if !strings.Contains(msg, "upstream: ") || !strings.Contains(msg, tl.Ops[0].Err) {
			t.Errorf("error %q does not carry the upstream failure %q", msg, tl.Ops[0].Err)
		}
	})

	t.Run("jellyfin rejects the api key", func(t *testing.T) {
		cfg := st.Config()
		cfg.Jellyfin.APIKey = "wrong"
		tl, msg := failed(t, cfg, config.LayerJellyfin, st.Target)
		wantNoMarks(t, tl, msg)
		if !strings.Contains(msg, "401") {
			t.Errorf("error %q does not report the 401", msg)
		}
	})

	t.Run("file missing from the mount", func(t *testing.T) {
		target := st.Target
		target.Path = "movies/missing.mkv"
		tl, msg := failed(t, st.Config(), config.LayerRclone, target)
		wantNoMarks(t, tl, msg)
		if !strings.Contains(msg, "missing.mkv") {
			t.Errorf("error %q does not name the missing file", msg)
		}
	})

	t.Run("usenet unreachable", func(t *testing.T) {
		cfg := st.Config()
		host, port, _ := net.SplitHostPort(closedPort(t))
		cfg.Usenet.Host = host
		fmt.Sscan(port, &cfg.Usenet.Port)
		tl, msg := failed(t, cfg, config.LayerUsenet, st.Target)
		wantNoMarks(t, tl, msg)
		if len(tl.Ops) != 1 || tl.Ops[0].Kind != results.OpConnect || tl.Ops[0].Err == "" {
			t.Errorf("ops %+v, want one failed connect", tl.Ops)
		}
	})

	t.Run("usenet rejects the password", func(t *testing.T) {
		cfg := st.Config()
		cfg.Usenet.Password = "wrong"
		tl, msg := failed(t, cfg, config.LayerUsenet, st.Target)
		wantNoMarks(t, tl, msg)
		if len(tl.Ops) != 2 || tl.Ops[1].Kind != results.OpAuth || !strings.Contains(tl.Ops[1].Err, "481") {
			t.Errorf("ops %+v, want a connect and an auth failed with 481", tl.Ops)
		}
		if strings.Contains(msg, "wrong") {
			t.Errorf("error %q leaks the password", msg)
		}
	})

	t.Run("ffprobe missing", func(t *testing.T) {
		cfg := st.Config()
		cfg.Tools.FFprobe = filepath.Join(t.TempDir(), "no-such-ffprobe")
		tl, msg := failed(t, cfg, config.LayerInfinidysk, st.Target)
		wantNoMarks(t, tl, msg)
	})

	t.Run("timeout mid-session", func(t *testing.T) {
		cfg := st.Config()
		cfg.Profile.Timeout = config.Duration(800 * time.Millisecond)
		start := time.Now()
		tl, msg := failed(t, cfg, config.LayerInfinidysk, st.Target)
		if took := time.Since(start); took > 3*time.Second {
			t.Errorf("sample took %v with an 800ms timeout", took)
		}
		if len(tl.Marks) == 0 || len(tl.Marks) >= threeSeeks.Len() {
			t.Fatalf("%d marks, want startup but not every seek", len(tl.Marks))
		}
		open := threeSeeks.Label(len(tl.Marks))
		if !strings.Contains(msg, "timeout after 800ms") || !strings.Contains(msg, fmt.Sprintf("%q", open)) {
			t.Errorf("error %q, want a timeout after 800ms in phase %q", msg, open)
		}
		if got := phases(tl)[0]; len(got.ops) == 0 || got.latency < 200*time.Millisecond {
			t.Errorf("startup before the timeout: %d ops, latency %v; want it measured as usual", len(got.ops), got.latency)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(300*time.Millisecond, cancel)
		tl, err := newProber(st.Config()).Sample(ctx, config.LayerInfinidysk, st.Target, threeSeeks)
		if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Errorf("err = %v, want it to report the cancellation", err)
		}
		checkTimelineInvariants(t, tl)
	})
}

func TestSecretsStayOffFFprobeCommandLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the argv-logging wrapper is a shell script")
	}
	st := startStack(t, fakestack.Options{})
	real, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not on PATH")
	}
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv")
	wrapper := filepath.Join(dir, "ffprobe")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" >> %q\nexec %q \"$@\"\n", argvLog, real)
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := st.Config()
	cfg.Tools.FFprobe = wrapper
	prober := newProber(cfg)
	for _, layer := range []config.Layer{config.LayerJellyfin, config.LayerInfinidysk} {
		cfg.Profile.Layers = []config.Layer{layer}
		if _, err := prober.Media(t.Context(), st.Target); err != nil {
			t.Fatalf("Media via %s: %v", layer, err)
		}
		if _, err := prober.Sample(t.Context(), layer, st.Target, threeSeeks); err != nil {
			t.Fatalf("Sample %s: %v", layer, err)
		}
	}

	logged, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("the wrapper logged no argv: %v", err)
	}
	argv := string(logged)
	if strings.Count(argv, "http://127.0.0.1:") != 4 {
		t.Fatalf("want 4 ffprobe runs against the local tap, argv log:\n%s", argv)
	}
	for name, secret := range map[string]string{
		"jellyfin API key":    st.APIKey,
		"infinidysk password": st.Pass,
		"jellyfin URL":        strings.TrimPrefix(st.JellyfinURL, "http://"),
		"infinidysk URL":      strings.TrimPrefix(st.InfinidyskURL, "http://"),
	} {
		if strings.Contains(argv, secret) {
			t.Errorf("%s %q is on ffprobe's command line:\n%s", name, secret, argv)
		}
	}
}

// A file cut by stream copy at a non-keyframe: ffprobe's startup interval
// reads one packet it cannot decode, so it prints no frame for startup and
// one frame for each seek.
func TestStreamStartingBetweenKeyframesFailsStartupInsteadOfShiftingFrames(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	src, err := fakestack.Generate(t.Context(), "ffmpeg", t.TempDir(), fakestack.MediaSpec{Length: mediaLength})
	if err != nil {
		t.Fatal(err)
	}
	mount := t.TempDir()
	cut := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-y", "-i", src,
		"-ss", (gopLength / 2).String(), "-c", "copy", "-copyinkf", filepath.Join(mount, "cut.mkv"))
	if out, err := cut.CombinedOutput(); err != nil {
		t.Fatalf("cut: %v: %s", err, out)
	}
	cfg := config.Default()
	cfg.Rclone.MountPath = mount
	cfg.Profile.Layers = []config.Layer{config.LayerRclone}
	target := config.Target{Name: "cut", Path: "cut.mkv"}
	prober := newProber(cfg)
	m, err := prober.Media(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	points, _ := config.ParseSeekPoints("10%,30%")
	plan, err := results.NewPlan(points, m.Duration)
	if err != nil {
		t.Fatal(err)
	}

	tl, err := prober.Sample(t.Context(), config.LayerRclone, target, plan)
	checkTimelineInvariants(t, tl)
	if err == nil || !strings.Contains(err.Error(), `phase "startup"`) {
		t.Errorf("err = %v, want startup reported as the phase without a frame", err)
	}
	for _, mk := range tl.Marks {
		t.Errorf("phase %s marked by the frame at %.3fs; no phase may be marked after startup failed", plan.Label(mk.Phase), mk.PTS)
	}
}
