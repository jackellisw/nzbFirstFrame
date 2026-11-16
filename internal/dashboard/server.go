// Package dashboard serves the embedded web UI and its JSON API over a
// results store. It never writes the store.
//
//	GET /                       embedded index.html, app.js, app.css (no CDN; homelab)
//	GET /api/runs               []runEntry (results.RunSummary + err), newest first
//	GET /api/runs/latest        results.Report of the newest run
//	GET /api/runs/{id}          results.Report (distributions, drill-down)
//
// The UI renders three views from those payloads, all as hand-written SVG:
//   - Latest run: per target, a strip plot per phase with one row per layer
//     (top of stack first), dots per sample (warmups hollow, failures as ×),
//     the p50 tick, layers in stack order so the hops read side by side.
//   - Trends: per target and phase label, one line per layer of p50 over runs.
//   - Sample: a waterfall of the sample's ops (wait bar from Start to
//     FirstByte, transfer bar to End, abandoned reads hatched), vertical
//     lines at marks, phases shaded, with offset, bytes and TTFB per row.
//     This is the "why is this number what it is" view.
//
// The UI fetches relative URLs, so it also works behind a reverse proxy
// that mounts it under a sub-path.
package dashboard

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/results"
)

//go:embed assets
var assets embed.FS

// Store is the read side of *results.Store. Taking only these two methods
// is what guarantees the dashboard cannot write runs.
type Store interface {
	List() ([]results.RunInfo, error)
	Load(id string) (*results.Run, error)
}

// New returns the dashboard handler.
func New(store Store) http.Handler {
	static, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err) // the embed directive guarantees the directory exists
	}
	s := &server{store: store, cache: map[string]cached{}}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/runs", s.handleRuns)
	mux.HandleFunc("GET /api/runs/latest", s.handleLatest)
	mux.HandleFunc("GET /api/runs/{id}", s.handleRun)
	return withHeaders(mux)
}

// server memoizes Summarize per run file. Key includes mtime: finished runs
// never change so they are analyzed once; a running run is re-analyzed when
// its file is rewritten. Correctness never depends on the cache (it is keyed
// by the only thing that can change), so it can be dropped at any time.
type server struct {
	store Store

	mu    sync.Mutex
	cache map[string]cached // by run ID
}

type cached struct {
	modified time.Time
	report   results.Report
}

// report returns the analyzed run, loading it if the file changed.
//
// Load happens after List's stat, and run files are replaced by rename, so
// the loaded content is never older than info.Modified. A report cached
// under a stale mtime is only reloaded once more, never served stale.
func (s *server) report(info results.RunInfo) (results.Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.cache[info.ID]; ok && c.modified.Equal(info.Modified) {
		return c.report, nil
	}
	run, err := s.store.Load(info.ID)
	if err != nil {
		return results.Report{}, err
	}
	rep := results.Summarize(run)
	s.cache[info.ID] = cached{modified: info.Modified, report: rep}
	return rep, nil
}

// runEntry is one row of GET /api/runs. A run file that cannot be loaded is
// still listed, with Err set, so it shows up in the run picker with the
// reason instead of silently vanishing.
type runEntry struct {
	results.RunSummary
	Err string `json:"err,omitempty"`
}

func (s *server) handleRuns(w http.ResponseWriter, r *http.Request) {
	infos, err := s.store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]runEntry, 0, len(infos))
	for _, info := range infos {
		rep, err := s.report(info)
		if err != nil {
			out = append(out, runEntry{
				RunSummary: results.RunSummary{RunHeader: results.RunHeader{ID: info.ID, Started: info.Started}},
				Err:        err.Error(),
			})
			continue
		}
		out = append(out, runEntry{RunSummary: rep.Trend()})
	}
	writeJSON(w, out)
}

func (s *server) handleRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	infos, err := s.store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Only IDs the store listed reach Load, so a crafted id cannot name a
	// file outside the results directory.
	for _, info := range infos {
		if info.ID == id {
			s.serveReport(w, info)
			return
		}
	}
	writeError(w, http.StatusNotFound, errors.New("no run "+id))
}

func (s *server) handleLatest(w http.ResponseWriter, r *http.Request) {
	infos, err := s.store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(infos) == 0 {
		writeError(w, http.StatusNotFound, errors.New("no runs yet"))
		return
	}
	s.serveReport(w, infos[0])
}

func (s *server) serveReport(w http.ResponseWriter, info results.RunInfo) {
	rep, err := s.report(info)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, rep)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	// A running run changes every few seconds; never let a cache answer.
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v) // the status line is already sent; nothing useful to do on error
}

func writeError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// withHeaders locks the page to its own embedded files: target names, paths
// and errors from run files are rendered as text, and the policy makes any
// slip there inert.
func withHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}
