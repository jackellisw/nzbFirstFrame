package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jackellisw/nzbfirstframe/internal/config"
	"github.com/jackellisw/nzbfirstframe/internal/dashboard"
	"github.com/jackellisw/nzbfirstframe/internal/fakestack"
	"github.com/jackellisw/nzbfirstframe/internal/results"
)

func startStack(t *testing.T, opts fakestack.Options) *fakestack.Stack {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	opts.Spec.Length = time.Minute
	st, err := fakestack.Start(t.Context(), opts)
	if err != nil {
		t.Fatalf("fakestack.Start: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func configFile(t *testing.T, cfg *config.Config) string {
	t.Helper()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "nzbff.yaml")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func seekPoints(t *testing.T, s string) []config.SeekPoint {
	t.Helper()
	pts, err := config.ParseSeekPoints(s)
	if err != nil {
		t.Fatal(err)
	}
	return pts
}

func onlyRun(t *testing.T, dir string) (*results.Store, *results.Run) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".json") {
		t.Fatalf("results dir holds %v, want exactly one run file", entries)
	}
	store, err := results.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Load(strings.TrimSuffix(entries[0].Name(), ".json"))
	if err != nil {
		t.Fatal(err)
	}
	return store, run
}

type taken struct {
	at     time.Time
	target string
	layer  config.Layer
	warmup bool
	iter   int
}

func samplesInOrder(run *results.Run) []taken {
	var out []taken
	for _, se := range run.Series {
		for _, s := range se.Samples {
			out = append(out, taken{s.At, se.Target, se.Layer, s.Warmup, s.Iteration})
		}
	}
	slices.SortFunc(out, func(a, b taken) int { return a.at.Compare(b.at) })
	return out
}

func schedule(target string, layers []config.Layer, warmup, iterations int) []taken {
	var out []taken
	for _, w := range []struct {
		warmup bool
		n      int
	}{{true, warmup}, {false, iterations}} {
		for i := range w.n {
			for _, l := range layers {
				out = append(out, taken{target: target, layer: l, warmup: w.warmup, iter: i})
			}
		}
	}
	return out
}

func withoutTimes(ts []taken) []taken {
	out := slices.Clone(ts)
	for i := range out {
		out[i].at = time.Time{}
	}
	return out
}

func TestRunMeasuresEveryLayerEndToEnd(t *testing.T) {
	const ttfb = 150 * time.Millisecond
	st := startStack(t, fakestack.Options{JellyfinTTFB: ttfb, InfinidyskTTFB: ttfb, ArticleTTFB: ttfb})
	cfg := st.Config()
	cfg.Profile.Warmup, cfg.Profile.Iterations = 1, 2
	cfg.Profile.SeekPoints = seekPoints(t, "25%,75%")
	dir := filepath.Join(t.TempDir(), "results")

	code, out, errOut := run(t, "run", "-c", configFile(t, cfg), "-results", dir)
	if code != exitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	labels := []string{"startup", "seek 25%", "seek 75%"}
	for _, l := range config.AllLayers {
		for _, label := range labels {
			row := regexp.MustCompile(`(?m)^fake\s+` + string(l) + `\s+` + regexp.QuoteMeta(label) + `\s+2\s+\d`)
			if !row.MatchString(out) {
				t.Errorf("table has no measured row for %s %s:\n%s", l, label, out)
			}
		}
	}

	store, stored := onlyRun(t, dir)
	if stored.Status != results.StatusDone || stored.Finished == nil {
		t.Errorf("status %s, finished %v; want done with a finish time", stored.Status, stored.Finished)
	}
	if !strings.Contains(out, "stored run "+stored.ID+" in "+dir) {
		t.Errorf("table does not name the stored run %s in %s:\n%s", stored.ID, dir, out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, stored.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for name, secret := range map[string]string{"api key": st.APIKey, "password": st.Pass} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("the run file stores the %s", name)
		}
	}

	rep := results.Summarize(stored)
	if len(rep.Cells) != len(config.AllLayers) {
		t.Fatalf("%d cells, want one per layer", len(rep.Cells))
	}
	for i, c := range rep.Cells {
		if c.Layer != config.AllLayers[i] {
			t.Errorf("cell %d is %s, want %s (top of the stack first)", i, c.Layer, config.AllLayers[i])
		}
		for _, p := range c.Phases {
			if p.N != 2 || p.Failed != 0 {
				t.Errorf("%s %s: N=%d failed=%d, want 2 measured and none failed", c.Layer, p.Label, p.N, p.Failed)
			}
		}
		if c.Layer == config.LayerJellyfin || c.Layer == config.LayerInfinidysk {
			if p50 := c.Phases[0].P50; p50 < ttfb {
				t.Errorf("%s startup p50 %v, below one injected TTFB of %v", c.Layer, p50, ttfb)
			}
		}
	}

	got := samplesInOrder(stored)
	if want := schedule("fake", config.AllLayers, 1, 2); !slices.Equal(withoutTimes(got), want) {
		t.Errorf("samples ran in order\n%v\nwant\n%v", withoutTimes(got), want)
	}

	res := httptest.NewRecorder()
	dashboard.New(store).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/runs/latest", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("GET /api/runs/latest: %d %s", res.Code, res.Body)
	}
	var served, want any
	if err := json.Unmarshal(res.Body.Bytes(), &served); err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(rep)
	_ = json.Unmarshal(wantJSON, &want)
	if !reflect.DeepEqual(served, want) {
		t.Errorf("dashboard's latest run differs from the stored run's report:\n%s", res.Body)
	}
}

type cancelAfterSamples struct {
	n      int
	after  time.Duration
	cancel func()

	mu   sync.Mutex
	log  bytes.Buffer
	seen int
}

func (w *cancelAfterSamples) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.log.Write(p)
	if bytes.Contains(p, []byte("msg=sample")) {
		if w.seen++; w.seen == w.n {
			time.AfterFunc(w.after, w.cancel)
		}
	}
	return len(p), nil
}

func (w *cancelAfterSamples) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.log.String()
}

func TestRunInterruptedKeepsTheSamplesTaken(t *testing.T) {
	const ttfb = 150 * time.Millisecond
	st := startStack(t, fakestack.Options{JellyfinTTFB: ttfb, InfinidyskTTFB: ttfb})
	cfg := st.Config()
	layers := []config.Layer{config.LayerJellyfin, config.LayerInfinidysk}
	cfg.Profile.Layers = layers
	cfg.Profile.Warmup, cfg.Profile.Iterations = 1, 2
	cfg.Profile.SeekPoints = seekPoints(t, "50%")
	dir := filepath.Join(t.TempDir(), "results")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stderr := &cancelAfterSamples{n: 3, after: ttfb / 2, cancel: cancel}
	var stdout bytes.Buffer
	code := Main(ctx, []string{"run", "-c", configFile(t, cfg), "-results", dir}, &stdout, stderr)
	if code != exitFail || !strings.Contains(stderr.String(), "nzbff run: interrupted") {
		t.Errorf("exit %d, want %d with an interrupted message; stderr:\n%s", code, exitFail, stderr)
	}

	_, stored := onlyRun(t, dir)
	if stored.Status != results.StatusInterrupted {
		t.Errorf("status %s, want interrupted", stored.Status)
	}
	if !strings.Contains(stdout.String(), "stored run "+stored.ID+" (interrupted)") {
		t.Errorf("no report of the interrupted run on stdout:\n%s", stdout.String())
	}
	got := samplesInOrder(stored)
	all := schedule("fake", layers, 1, 2)
	if len(got) < 3 || len(got) >= len(all) || !slices.Equal(withoutTimes(got), all[:len(got)]) {
		t.Fatalf("stored samples %v, want the first 3 or more of %v and not all", withoutTimes(got), all)
	}
	for _, se := range stored.Series {
		for _, s := range se.Samples {
			if s.Err != "" || len(s.Timeline.Marks) != 2 {
				t.Errorf("%s #%d: err %q, %d marks; want only complete samples kept", se.Layer, s.Iteration, s.Err, len(s.Timeline.Marks))
			}
		}
	}
}

func TestRunWithAMissingTargetMeasuresTheOther(t *testing.T) {
	const cooldown = 150 * time.Millisecond
	st := startStack(t, fakestack.Options{InfinidyskTTFB: 50 * time.Millisecond})
	cfg := st.Config()
	cfg.Profile.Layers = []config.Layer{config.LayerRclone, config.LayerInfinidysk}
	cfg.Profile.Warmup, cfg.Profile.Iterations = 0, 2
	cfg.Profile.Cooldown = config.Duration(cooldown)
	missing := st.Target
	missing.Name, missing.Path = "missing", "movies/missing.mkv"
	cfg.Targets = []config.Target{missing, st.Target}
	dir := filepath.Join(t.TempDir(), "results")

	code, out, errOut := run(t, "startup", "-c", configFile(t, cfg), "-results", dir)
	if code != exitFail {
		t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitFail, out, errOut)
	}
	_, stored := onlyRun(t, dir)
	summary := "nzbff startup: run " + stored.ID + ": 4 of 8 samples failed (see report)\n"
	if !strings.HasSuffix(errOut, summary) {
		t.Errorf("stderr does not end with %q:\n%s", summary, errOut)
	}
	for _, want := range []string{
		"4 failed:",
		"missing/rclone #0 startup:", "missing/rclone #1 startup:",
		"missing/infinidysk #0 startup:", "missing/infinidysk #1 startup:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	for _, l := range []string{"rclone", "infinidysk"} {
		if !regexp.MustCompile(`(?m)^fake\s+` + l + `\s+startup\s+2\s+\d`).MatchString(out) {
			t.Errorf("table has no measured row for fake %s:\n%s", l, out)
		}
		if !regexp.MustCompile(`(?m)^missing\s+` + l + `\s+startup\s+0\s+-`).MatchString(out) {
			t.Errorf("table has no empty row for missing %s:\n%s", l, out)
		}
	}

	if stored.Status != results.StatusDone {
		t.Errorf("status %s, want done: a failed target does not stop the run", stored.Status)
	}
	for _, se := range stored.Series {
		for _, s := range se.Samples {
			if failed := s.Err != ""; failed != (se.Target == "missing") {
				t.Errorf("%s/%s #%d: err %q", se.Target, se.Layer, s.Iteration, s.Err)
			}
		}
		if se.Layer == config.LayerRclone && se.Target == "missing" && !strings.Contains(se.Samples[0].Err, "missing.mkv") {
			t.Errorf("rclone error %q does not name the missing file", se.Samples[0].Err)
		}
	}

	got := samplesInOrder(stored)
	if len(got) != 8 {
		t.Fatalf("%d samples stored, want 8", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].target != got[i-1].target {
			continue
		}
		if gap := got[i].at.Sub(got[i-1].at); gap < cooldown {
			t.Errorf("%s: sample %d started %v after the previous one, want at least the %v cooldown", got[i].target, i, gap, cooldown)
		}
	}
}

func TestRunWithAnUnreachableLayerMeasuresTheOthers(t *testing.T) {
	st := startStack(t, fakestack.Options{})
	cfg := st.Config()
	cfg.Infinidysk.URL = "http://127.0.0.1:1"
	cfg.Profile.Warmup, cfg.Profile.Iterations = 0, 1
	cfg.Profile.Cooldown = 0
	cfg.Profile.SeekPoints = seekPoints(t, "50%")
	dir := filepath.Join(t.TempDir(), "results")

	code, out, errOut := run(t, "run", "-c", configFile(t, cfg), "-results", dir)
	if code != exitFail {
		t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitFail, out, errOut)
	}
	_, stored := onlyRun(t, dir)
	if tr := stored.Targets[0]; tr.Err != "" || tr.Plan.Len() != 2 {
		t.Errorf("target %+v; want it planned through a layer that answers", tr)
	}
	if summary := "run " + stored.ID + ": 1 of 4 samples failed"; !strings.Contains(errOut, summary) {
		t.Errorf("stderr lacks %q:\n%s", summary, errOut)
	}
	for _, se := range stored.Series {
		if len(se.Samples) != 1 {
			t.Errorf("%s: %d samples, want 1", se.Layer, len(se.Samples))
			continue
		}
		s := se.Samples[0]
		if broken := se.Layer == config.LayerInfinidysk; (s.Err != "") != broken || !broken && len(s.Timeline.Marks) != 2 {
			t.Errorf("%s: err %q, %d marks; want only infinidysk to fail and every other layer measured", se.Layer, s.Err, len(s.Timeline.Marks))
		}
	}
}

type cancelWhenWritten struct {
	text   string
	cancel func()

	mu  sync.Mutex
	log bytes.Buffer
}

func (w *cancelWhenWritten) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.log.Write(p)
	if strings.Contains(w.log.String(), w.text) {
		w.cancel()
	}
	return len(p), nil
}

func TestDevStackConfigWorksFromAnyDirectory(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	t.Chdir(t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stderr := &cancelWhenWritten{text: "fake stack is up", cancel: cancel}
	if code := Main(ctx, []string{"dev-stack", "-dir", "ds", "-o", "ds.yaml"}, io.Discard, stderr); code != exitOK {
		t.Fatalf("dev-stack exit %d:\n%s", code, stderr.log.String())
	}
	raw, err := os.ReadFile("ds.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())
	for name, p := range map[string]string{
		"rclone.mount_path": cfg.Rclone.MountPath, "targets[0].nzb": cfg.Targets[0].NZB, "results.dir": cfg.Results.Dir,
	} {
		if !filepath.IsAbs(p) {
			t.Errorf("%s is %q, which resolves against the reader's working directory", name, p)
		}
	}
	for _, p := range []string{cfg.Rclone.MountPath, cfg.Targets[0].NZB} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("from another directory: %v", err)
		}
	}
}
