package probe_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
	"github.com/jackellisw/nzbfirstframe/internal/results"
)

func scriptedFFprobe(t *testing.T, script string) (*config.Config, config.Target) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in ffprobe is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.mkv"), []byte("not media"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Rclone.MountPath = dir
	cfg.Tools.FFprobe = bin
	cfg.Targets = []config.Target{{Name: "a", Path: "a.mkv"}}
	if err := cfg.Finalize(); err != nil {
		t.Fatal(err)
	}
	return cfg, cfg.Targets[0]
}

func TestDriverTurnsFrameLinesIntoMarksAndIgnoresTheRest(t *testing.T) {
	cfg, target := scriptedFFprobe(t, `
echo 'pts_time=0.000000'
echo '[matroska @ 0x1] some warning on stdout'
echo ''
echo 'pts_time=14.041667'
echo 'pts_time=N/A'
echo 'pts_time=99.000000'
exit 1
`)
	plan := planAt(0.25, 0.5)
	tl, err := newProber(cfg).Sample(t.Context(), config.LayerRclone, target, plan)
	if err != nil {
		t.Fatalf("a non-zero exit after every phase's frame must not fail the sample: %v", err)
	}
	checkTimelineInvariants(t, tl)
	var pts []float64
	for _, m := range tl.Marks {
		pts = append(pts, m.PTS)
	}
	if want := []float64{0, 14.041667, -1}; len(pts) != 3 || pts[0] != want[0] || pts[1] != want[1] || pts[2] != want[2] {
		t.Errorf("mark PTS %v, want %v (one per phase, the extra frame line dropped)", pts, want)
	}
}

func TestDriverStampsEachLineWhenItArrives(t *testing.T) {
	const gap = 300 * time.Millisecond
	cfg, target := scriptedFFprobe(t, `
echo 'pts_time=0.000000'
sleep 0.3
echo 'pts_time=15.000000'
sleep 0.3
`)
	tl, err := newProber(cfg).Sample(t.Context(), config.LayerRclone, target, planAt(0.25))
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Marks) != 2 {
		t.Fatalf("%d marks, want 2", len(tl.Marks))
	}
	if between := tl.Marks[1].At - tl.Marks[0].At; between < gap || between > gap+150*time.Millisecond {
		t.Errorf("marks %v apart, want the %v between the two lines", between, gap)
	}
	if after := tl.End - tl.Marks[1].At; after < gap {
		t.Errorf("last mark only %v before the process exited; it was stamped at exit, not when its line arrived", after)
	}
}

func TestDriverReportsThePhaseWithoutAFrameAndStderrTail(t *testing.T) {
	cfg, target := scriptedFFprobe(t, `
echo 'pts_time=0.000000'
i=0; while [ $i -lt 400 ]; do echo "noise line $i padding padding padding" >&2; i=$((i+1)); done
echo 'moov atom not found' >&2
exit 1
`)
	plan := planAt(0.25, 0.5)
	tl, err := newProber(cfg).Sample(t.Context(), config.LayerRclone, target, plan)
	if err == nil {
		t.Fatal("sample succeeded with one frame for three phases")
	}
	checkTimelineInvariants(t, tl)
	if len(tl.Marks) != 1 {
		t.Errorf("%d marks, want the startup phase that did complete", len(tl.Marks))
	}
	msg := err.Error()
	for _, want := range []string{`"seek 25%"`, "exit status 1", "moov atom not found"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %s", msg, want)
		}
	}
	if strings.Contains(msg, "noise line 0 ") || len(msg) > 5000 {
		t.Errorf("error carries %d bytes of stderr, want only its tail", len(msg))
	}
}

func TestDriverReturnsAtTheTimeoutWhenFFprobeHangs(t *testing.T) {
	cfg, target := scriptedFFprobe(t, `
echo 'pts_time=0.000000'
sleep 30
`)
	cfg.Profile.Timeout = config.Duration(300 * time.Millisecond)
	start := time.Now()
	tl, err := newProber(cfg).Sample(t.Context(), config.LayerRclone, target, planAt(0.5))
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("sample returned after %v with a 300ms timeout", took)
	}
	if err == nil || !strings.Contains(err.Error(), `timeout after 300ms in phase "seek 50%"`) {
		t.Errorf("err = %v, want a timeout in the open phase", err)
	}
	checkTimelineInvariants(t, tl)
	if want := []results.Mark{{Phase: 0, PTS: 0}}; len(tl.Marks) != 1 || tl.Marks[0].Phase != want[0].Phase {
		t.Errorf("marks %+v, want startup only", tl.Marks)
	}
}
