package dashboard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
	"github.com/jackellisw/nzbfirstframe/internal/results"
)

type fakeStore struct {
	infos   []results.RunInfo
	runs    map[string]*results.Run
	errs    map[string]error
	listErr error
	loads   map[string]int
}

func newFakeStore(infos ...results.RunInfo) *fakeStore {
	return &fakeStore{infos: infos, runs: map[string]*results.Run{}, errs: map[string]error{}, loads: map[string]int{}}
}

func (f *fakeStore) List() ([]results.RunInfo, error) { return f.infos, f.listErr }

func (f *fakeStore) Load(id string) (*results.Run, error) {
	f.loads[id]++
	if err := f.errs[id]; err != nil {
		return nil, err
	}
	return f.runs[id], nil
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func errorBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct{ Error string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
		t.Fatalf("want a JSON error body, got %q", rec.Body.String())
	}
	return body.Error
}

func info(id string, modified time.Time) results.RunInfo {
	started, _ := time.Parse("20060102T150405Z", strings.SplitN(id, "-", 2)[0])
	return results.RunInfo{ID: id, Started: started, Modified: modified}
}

func run(id string) *results.Run {
	return &results.Run{
		Schema:   results.SchemaVersion,
		ID:       id,
		Status:   results.StatusDone,
		Settings: results.Settings{Layers: []config.Layer{config.LayerInfinidysk}, Iterations: 1},
		Targets:  []results.Target{{Name: "t"}},
		Series:   []results.Series{{SeriesKey: results.SeriesKey{Target: "t", Layer: config.LayerInfinidysk}}},
	}
}

func TestEmptyStoreListsNoRuns(t *testing.T) {
	rec := get(t, New(newFakeStore()), "/api/runs")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("got %d %q, want 200 [] (the UI's empty state keys on an empty list)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q: a running run must never be served from a cache", cc)
	}
}

func TestLatestWithoutRunsIsNotFound(t *testing.T) {
	rec := get(t, New(newFakeStore()), "/api/runs/latest")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
	errorBody(t, rec)
}

func TestOnlyListedRunsAreLoaded(t *testing.T) {
	st := newFakeStore(info("20261116T101502Z-3f9a1c", time.Now()))
	h := New(st)
	for _, path := range []string{"/api/runs/20261116T000000Z-000000", "/api/runs/..%2F..%2Fetc%2Fpasswd"} {
		rec := get(t, h, path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404", path, rec.Code)
		}
	}
	if len(st.loads) != 0 {
		t.Errorf("Load called for unlisted ids: %v", st.loads)
	}
}

func TestUnreadableRunIsListedWithItsError(t *testing.T) {
	const id = "20261116T120000Z-0badf0"
	st := newFakeStore(info(id, time.Now()))
	st.errs[id] = errors.New("decode: unexpected end of JSON input")
	h := New(st)

	rec := get(t, h, "/api/runs")
	var entries []runEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("got %d %q: %v", rec.Code, rec.Body.String(), err)
	}
	if len(entries) != 1 || entries[0].ID != id || entries[0].Err != "decode: unexpected end of JSON input" {
		t.Fatalf("want the run listed with its load error, got %+v", entries)
	}
	if want := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC); !entries[0].Started.Equal(want) {
		t.Errorf("started %v, want %v from the listing", entries[0].Started, want)
	}

	rec = get(t, h, "/api/runs/"+id)
	if rec.Code != http.StatusInternalServerError || errorBody(t, rec) != "decode: unexpected end of JSON input" {
		t.Errorf("got %d %q, want 500 with the load error", rec.Code, rec.Body.String())
	}
}

func TestListFailureIsServerError(t *testing.T) {
	st := newFakeStore()
	st.listErr = errors.New("open results: permission denied")
	h := New(st)
	for _, path := range []string{"/api/runs", "/api/runs/latest", "/api/runs/x"} {
		rec := get(t, h, path)
		if rec.Code != http.StatusInternalServerError || errorBody(t, rec) != "open results: permission denied" {
			t.Errorf("%s: got %d %q", path, rec.Code, rec.Body.String())
		}
	}
}

func TestServesEmbeddedUI(t *testing.T) {
	h := New(newFakeStore())
	for path, want := range map[string]string{
		"/":         "text/html",
		"/app.js":   "javascript",
		"/app.css":  "text/css",
		"/icon.svg": "image/svg+xml",
	} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), want) {
			t.Errorf("%s: got %d %q, want 200 %s", path, rec.Code, rec.Header().Get("Content-Type"), want)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
			t.Errorf("%s: CSP %q", path, csp)
		}
	}
	if body := get(t, h, "/").Body.String(); !strings.Contains(body, `src="app.js"`) || !strings.Contains(body, `href="app.css"`) {
		t.Errorf("index.html does not load the embedded script and stylesheet by relative path")
	}
}

func TestAssetsFetchNothingRemote(t *testing.T) {
	url := regexp.MustCompile(`(https?:)?//[a-zA-Z0-9.-]+\.[a-z]{2,}[^\s"'<>)]*`)
	err := fs.WalkDir(assets, "assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := assets.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range url.FindAll(b, -1) {
			if string(m) != "http://www.w3.org/2000/svg" { // XML namespace name, never fetched
				t.Errorf("%s references %s", path, m)
			}
		}
		if bytes.Contains(b, []byte("@import")) {
			t.Errorf("%s uses @import", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFixturesMatchTheAPIShapes(t *testing.T) {
	var rep results.Report
	roundTrip(t, "testdata/report.json", &rep)
	if len(rep.Targets) != 2 || len(rep.Cells) != 8 {
		t.Errorf("report fixture: %d targets, %d cells; want 2 targets × 4 layers", len(rep.Targets), len(rep.Cells))
	}
	var runs []runEntry
	roundTrip(t, "testdata/runs.json", &runs)
	if len(runs) == 0 || runs[0].ID != rep.Run.ID {
		t.Errorf("runs fixture must list the report's run first")
	}
}

func roundTrip(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	again, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(again, &got); err != nil {
		t.Fatal(err)
	}
	if d := firstDiff(want, got, ""); d != "" {
		t.Errorf("%s does not survive a round trip through the Go types: %s", path, d)
	}
}

func firstDiff(want, got any, at string) string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return at + ": not an object after the round trip"
		}
		for k, v := range w {
			gv, ok := g[k]
			if !ok {
				return at + "." + k + ": dropped by the Go types"
			}
			if d := firstDiff(v, gv, at+"."+k); d != "" {
				return d
			}
		}
		for k := range g {
			if _, ok := w[k]; !ok {
				return at + "." + k + ": added by the Go types"
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return at + ": array changed"
		}
		for i := range w {
			if d := firstDiff(w[i], g[i], fmt.Sprintf("%s[%d]", at, i)); d != "" {
				return d
			}
		}
	default:
		if !reflect.DeepEqual(want, got) {
			return fmt.Sprintf("%s: %v became %v", at, want, got)
		}
	}
	return ""
}

func TestReportIsAnalyzedOncePerModTime(t *testing.T) {
	const id = "20261116T101502Z-3f9a1c"
	t0 := time.Date(2026, 11, 16, 10, 16, 0, 0, time.UTC)
	st := newFakeStore(info(id, t0))
	st.runs[id] = run(id)
	h := New(st)

	want, err := json.Marshal(results.Summarize(run(id)))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		rec := get(t, h, "/api/runs/"+id)
		if rec.Code != http.StatusOK || !bytes.Equal(bytes.TrimSpace(rec.Body.Bytes()), want) {
			t.Fatalf("got %d %s, want Summarize(run)", rec.Code, rec.Body.String())
		}
	}
	get(t, h, "/api/runs")
	if st.loads[id] != 1 {
		t.Fatalf("unchanged file loaded %d times, want 1", st.loads[id])
	}

	st.infos[0].Modified = t0.Add(3 * time.Second)
	get(t, h, "/api/runs/"+id)
	if st.loads[id] != 2 {
		t.Fatalf("rewritten file loaded %d times in total, want 2", st.loads[id])
	}
}

func TestRunListDoesNotReloadViewedHistory(t *testing.T) {
	ids := []string{"20261116T101502Z-3f9a1c", "20261115T030005Z-1a2b3c", "20261114T030005Z-4d5e6f"}
	var infos []results.RunInfo
	for _, id := range ids {
		infos = append(infos, info(id, time.Now()))
	}
	st := newFakeStore(infos...)
	for _, id := range ids {
		st.runs[id] = run(id)
	}
	h := New(st)

	get(t, h, "/api/runs")
	for _, id := range ids {
		get(t, h, "/api/runs/"+id)
	}
	before := maps.Clone(st.loads)
	get(t, h, "/api/runs")
	if !maps.Equal(st.loads, before) {
		t.Fatalf("listing unchanged runs reloaded files: loads %v, before %v", st.loads, before)
	}
}

func TestRunsAndLatestFollowTheListing(t *testing.T) {
	newer, older := "20261116T101502Z-3f9a1c", "20261115T030005Z-1a2b3c"
	st := newFakeStore(info(newer, time.Now()), info(older, time.Now()))
	st.runs[newer], st.runs[older] = run(newer), run(older)
	h := New(st)

	var entries []runEntry
	if err := json.Unmarshal(get(t, h, "/api/runs").Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != newer || entries[1].ID != older {
		t.Fatalf("want newest first as listed, got %+v", entries)
	}
	if want := results.Summarize(run(newer)).Trend(); !reflect.DeepEqual(entries[0].RunSummary, want) {
		t.Errorf("entry %+v, want the run's trend projection %+v", entries[0].RunSummary, want)
	}

	var rep results.Report
	if err := json.Unmarshal(get(t, h, "/api/runs/latest").Body.Bytes(), &rep); err != nil || rep.Run.ID != newer {
		t.Errorf("latest = %q (%v), want %q", rep.Run.ID, err, newer)
	}
}
