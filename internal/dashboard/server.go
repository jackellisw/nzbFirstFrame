// Package dashboard serves the embedded web UI and its JSON API over a
// results.Store. It never writes the store.
//
//	GET /                       embedded index.html + app.js (no CDN; homelab)
//	GET /api/runs               []results.RunSummary, newest first (trends)
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
package dashboard

import (
	"embed"
	"net/http"
	"sync"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/results"
)

//go:embed assets
var assets embed.FS

// New returns the dashboard handler.
func New(store *results.Store) http.Handler {
	_ = assets
	panic("not implemented")
}

// server memoizes Summarize per run file. Key includes mtime: finished runs
// never change so they are analyzed once; a running run is re-analyzed when
// its file is rewritten. Correctness never depends on the cache (it is keyed
// by the only thing that can change), so it can be dropped at any time.
type server struct {
	store *results.Store

	mu    sync.Mutex
	cache map[string]cached // by run ID
}

type cached struct {
	modified time.Time
	report   results.Report
}

// report returns the analyzed run, loading it if the file changed.
func (s *server) report(info results.RunInfo) (results.Report, error) {
	panic("not implemented")
}

func (s *server) handleRuns(w http.ResponseWriter, r *http.Request)   { panic("not implemented") }
func (s *server) handleRun(w http.ResponseWriter, r *http.Request)    { panic("not implemented") }
func (s *server) handleLatest(w http.ResponseWriter, r *http.Request) { panic("not implemented") }
