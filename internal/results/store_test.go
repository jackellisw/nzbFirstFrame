package results

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

var t0 = time.Date(2026, 11, 16, 10, 15, 2, 0, time.UTC)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "results"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func create(t *testing.T, s *Store, started time.Time) *RunWriter {
	t.Helper()
	w, err := s.Create(Run{
		Started: started, Host: "mediabox", Mode: "all",
		Series: []Series{{SeriesKey: SeriesKey{"foo", config.LayerInfinidysk}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestStoreRoundTrip(t *testing.T) {
	s := openStore(t)
	if fi, err := os.Stat(s.Dir()); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("store dir: %v %v, want 0700 dir", fi, err)
	}
	w := create(t, s, t0)
	id := w.Run().ID
	if !strings.HasPrefix(id, "20261116T101502.000Z-") || len(id) != len("20261116T101502.000Z-3f9a1c") {
		t.Fatalf("id = %q, want <start UTC to the millisecond>-<6 hex>", id)
	}

	first, err := s.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if first.Schema != SchemaVersion || first.Status != StatusRunning || first.Host != "mediabox" || len(first.Series) != 1 {
		t.Fatalf("initial file = %+v", first)
	}

	if err := w.SetTarget(Target{Name: "foo", Duration: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := w.SetTarget(Target{Name: "foo", Duration: 2 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := w.Add(SeriesKey{"foo", config.LayerInfinidysk}, Sample{Iteration: 0, At: t0, Timeline: session(200, 600)}); err != nil {
		t.Fatal(err)
	}
	if err := w.Add(SeriesKey{"foo", config.LayerUsenet}, Sample{Iteration: 0, At: t0, Err: "nntp: 430"}); err != nil {
		t.Fatal(err)
	}
	mid, err := s.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(mid.Targets) != 1 || mid.Targets[0].Duration != 2*time.Minute {
		t.Fatalf("targets = %+v, want foo replaced", mid.Targets)
	}
	if len(mid.Series) != 2 || len(mid.Series[0].Samples) != 1 || mid.Series[1].Layer != config.LayerUsenet || mid.Series[1].Samples[0].Err != "nntp: 430" {
		t.Fatalf("series = %+v", mid.Series)
	}

	end := t0.Add(3 * time.Minute)
	if err := w.Finish(StatusDone, end); err != nil {
		t.Fatal(err)
	}
	once, _ := os.ReadFile(filepath.Join(s.Dir(), id+".json"))
	if err := w.Finish(StatusDone, end); err != nil {
		t.Fatal(err)
	}
	twice, _ := os.ReadFile(filepath.Join(s.Dir(), id+".json"))
	if !bytes.Equal(once, twice) {
		t.Fatal("second Finish changed the file")
	}
	done, err := s.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != StatusDone || done.Finished == nil || !done.Finished.Equal(end) {
		t.Fatalf("finished run = %+v", done)
	}
	if !bytes.Equal(mustJSON(t, done), mustJSON(t, w.Run())) {
		t.Fatal("loaded run differs from the writer's run")
	}
	if left, _ := filepath.Glob(filepath.Join(s.Dir(), ".*.tmp")); len(left) != 0 {
		t.Fatalf("tmp files left behind: %v", left)
	}
}

func TestStoreCreateDoesNotShareCallerSlices(t *testing.T) {
	s := openStore(t)
	series := make([]Series, 1, 4)
	series[0].SeriesKey = SeriesKey{"foo", config.LayerRclone}
	w, err := s.Create(Run{Started: t0, Series: series})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Add(SeriesKey{"bar", config.LayerRclone}, Sample{})
	if extra := series[:2][1]; extra.Target != "" {
		t.Fatalf("writer appended into the caller's array: %+v", extra)
	}
}

func TestStoreCreateNeverClaimsAnExistingID(t *testing.T) {
	s := openStore(t)
	taken := filepath.Join(s.Dir(), "20261116T101502.000Z-aaaaaa.json")
	if err := os.WriteFile(taken, []byte("someone else's run"), 0o600); err != nil {
		t.Fatal(err)
	}
	suffixes := []string{"aaaaaa", "bbbbbb"}
	orig := randSuffix
	randSuffix = func() string { s := suffixes[0]; suffixes = suffixes[1:]; return s }
	defer func() { randSuffix = orig }()

	w := create(t, s, t0)
	if w.Run().ID != "20261116T101502.000Z-bbbbbb" {
		t.Fatalf("id = %q, want retry with a new suffix", w.Run().ID)
	}
	if b, _ := os.ReadFile(taken); string(b) != "someone else's run" {
		t.Fatalf("existing run file was overwritten: %q", b)
	}
}

func TestStoreList(t *testing.T) {
	s := openStore(t)
	mid := create(t, s, t0).Run().ID
	newest := create(t, s, t0.Add(time.Hour)).Run().ID
	oldest := create(t, s, t0.Add(-time.Hour)).Run().ID
	garbage := "20251231T235959Z-00ff00"
	for name, body := range map[string]string{
		garbage + ".json":       "not json: List must not read contents",
		"notes.txt":             "x",
		".20261116T101502Z.tmp": "x",
		"20261116T101502Z.json": "x",
	} {
		if err := os.WriteFile(filepath.Join(s.Dir(), name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range runs {
		ids = append(ids, r.ID)
		if r.Modified.IsZero() {
			t.Errorf("%s: zero mtime", r.ID)
		}
	}
	if want := []string{newest, mid, oldest, garbage}; fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("ids = %v, want newest first %v", ids, want)
	}
	if !runs[0].Started.Equal(t0.Add(time.Hour)) || !runs[3].Started.Equal(time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC)) {
		t.Fatalf("started = %v / %v", runs[0].Started, runs[3].Started)
	}
}

func TestStoreLoadRejects(t *testing.T) {
	s := openStore(t)
	write := func(id, body string) {
		if err := os.WriteFile(filepath.Join(s.Dir(), id+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("20261116T101502Z-000002", `{"schema": 2, "id": "20261116T101502Z-000002", "series": "a future shape"}`)
	if _, err := s.Load("20261116T101502Z-000002"); !errors.Is(err, ErrNewerSchema) {
		t.Errorf("newer schema: err = %v, want ErrNewerSchema", err)
	}
	write("20261116T101502Z-000000", `{"id": "20261116T101502Z-000000"}`)
	if _, err := s.Load("20261116T101502Z-000000"); err == nil {
		t.Error("missing schema: want error")
	}
	write("20261116T101502Z-0000ff", `{"schema": 1, "id": "20261116T101502Z-000001"}`)
	if _, err := s.Load("20261116T101502Z-0000ff"); err == nil {
		t.Error("id mismatch: want error")
	}
	for _, id := range []string{"../results", "20261116T101502Z-ffffff", ""} {
		if _, err := s.Load(id); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Load(%q) err = %v, want fs.ErrNotExist", id, err)
		}
	}
}

func TestOpenRemovesStaleTmpFiles(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, ".20261114T000000Z-abcdef.123.tmp")
	fresh := filepath.Join(dir, ".20261116T000000Z-abcdef.456.tmp")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stale tmp still present: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh tmp removed (a writer may own it): %v", err)
	}
}

func TestStoreParallelWriters(t *testing.T) {
	s := openStore(t)
	const n = 20
	ids := make([]string, 2)
	var wg sync.WaitGroup
	for g, layer := range []config.Layer{config.LayerJellyfin, config.LayerUsenet} {
		wg.Go(func() {
			w, err := s.Create(Run{Started: t0})
			if err != nil {
				t.Error(err)
				return
			}
			ids[g] = w.Run().ID
			for i := range n {
				if err := w.Add(SeriesKey{"foo", layer}, Sample{Iteration: i, Timeline: session(100, 200)}); err != nil {
					t.Error(err)
					return
				}
			}
			if err := w.Finish(StatusDone, t0); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	if ids[0] == ids[1] {
		t.Fatalf("both writers got id %s", ids[0])
	}
	for g, layer := range []config.Layer{config.LayerJellyfin, config.LayerUsenet} {
		run, err := s.Load(ids[g])
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != StatusDone || len(run.Series) != 1 || run.Series[0].Layer != layer || len(run.Series[0].Samples) != n {
			t.Fatalf("run %s = status %s, series %+v", ids[g], run.Status, run.Series)
		}
	}
	if runs, _ := s.List(); len(runs) != 2 {
		t.Fatalf("listed %d runs, want 2", len(runs))
	}
}

func TestStoreReaderNeverSeesTornFile(t *testing.T) {
	s := openStore(t)
	w := create(t, s, t0)
	id := w.Run().ID
	key := SeriesKey{"foo", config.LayerInfinidysk}
	const n = 60

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range n {
			if err := w.Add(key, Sample{Iteration: i, Timeline: session(200, 600, 300, 250)}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	reads, last := 0, 0
	for {
		select {
		case <-done:
		default:
			run, err := s.Load(id)
			if err != nil {
				t.Fatalf("read %d: %v", reads, err)
			}
			got := len(run.Series[0].Samples)
			if got < last {
				t.Fatalf("read %d: %d samples after seeing %d", reads, got, last)
			}
			last = got
			reads++
			continue
		}
		break
	}
	if run, err := s.Load(id); err != nil || len(run.Series[0].Samples) != n {
		t.Fatalf("final read: %v", err)
	}
	t.Logf("%d concurrent reads, all complete", reads)
}

func TestStoreListsRunsInStartOrderWithinOneSecondAndAcrossIDFormats(t *testing.T) {
	s := openStore(t)
	suffixes := []string{"ffffff", "000000"}
	orig := randSuffix
	randSuffix = func() string { s := suffixes[0]; suffixes = suffixes[1:]; return s }
	defer func() { randSuffix = orig }()
	second := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	earlier := create(t, s, second.Add(100*time.Millisecond)).Run().ID
	later := create(t, s, second.Add(200*time.Millisecond)).Run().ID

	// Runs stored before IDs carried milliseconds.
	legacySame, legacyNext := "20261116T120000Z-aaaaaa", "20261116T120001Z-bbbbbb"
	for _, id := range []string{legacySame, legacyNext} {
		body := fmt.Sprintf(`{"schema": 1, "id": %q, "status": "done"}`, id)
		if err := os.WriteFile(filepath.Join(s.Dir(), id+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	runs, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range runs {
		ids = append(ids, r.ID)
	}
	if want := []string{legacyNext, later, earlier, legacySame}; fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Errorf("ids = %v, want newest start first %v", ids, want)
	}
	if len(runs) == 4 && !runs[1].Started.Equal(second.Add(200*time.Millisecond)) {
		t.Errorf("started %v, want the run's start to the millisecond", runs[1].Started)
	}
	for _, id := range []string{later, legacySame} {
		if _, err := s.Load(id); err != nil {
			t.Errorf("Load(%s): %v", id, err)
		}
	}
}
