// Package dashboard serves the embedded web UI and its JSON API over a
// results store. It never writes the store.
//
//	GET /                       embedded index.html, app.js, app.css (no CDN; homelab)
//	GET /api/runs               []runEntry (results.RunSummary + err), newest first
//	GET /api/runs/latest        results.Report of the newest run
//	GET /api/runs/{id}          results.Report (distributions, drill-down)
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
		panic(err)
	}
	s := &server{store: store, trends: map[string]cachedTrend{}}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/runs", s.handleRuns)
	mux.HandleFunc("GET /api/runs/latest", s.handleLatest)
	mux.HandleFunc("GET /api/runs/{id}", s.handleRun)
	return withSecurityHeaders(mux)
}

type server struct {
	store Store

	mu     sync.Mutex
	trends map[string]cachedTrend
	last   struct {
		id       string
		modified time.Time
		report   results.Report
	}
}

type cachedTrend struct {
	modified time.Time
	trend    results.RunSummary
}

func (s *server) report(info results.RunInfo) (results.Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last.id == info.ID && s.last.modified.Equal(info.Modified) {
		return s.last.report, nil
	}
	run, err := s.store.Load(info.ID)
	if err != nil {
		return results.Report{}, err
	}
	rep := results.Summarize(run)
	s.last.id, s.last.modified, s.last.report = info.ID, info.Modified, rep
	s.trends[info.ID] = cachedTrend{modified: info.Modified, trend: rep.Trend()}
	return rep, nil
}

func (s *server) trend(info results.RunInfo) (results.RunSummary, error) {
	s.mu.Lock()
	c, ok := s.trends[info.ID]
	s.mu.Unlock()
	if ok && c.modified.Equal(info.Modified) {
		return c.trend, nil
	}
	rep, err := s.report(info)
	if err != nil {
		return results.RunSummary{}, err
	}
	return rep.Trend(), nil
}

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
		trend, err := s.trend(info)
		if err != nil {
			out = append(out, runEntry{
				RunSummary: results.RunSummary{RunHeader: results.RunHeader{ID: info.ID, Started: info.Started}},
				Err:        err.Error(),
			})
			continue
		}
		out = append(out, runEntry{RunSummary: trend})
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
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func withSecurityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}
