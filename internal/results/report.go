package results

import (
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

// Report is the analyzed view of one run. It is the -format json output and
// the body of GET /api/runs/{id}; the dashboard renders nothing it doesn't
// get from here.
type Report struct {
	Run     RunHeader `json:"run"`
	Targets []Target  `json:"targets"`
	Cells   []Cell    `json:"cells"` // target-major, layers top of stack first
}

// RunHeader is Run without its targets and series.
type RunHeader struct {
	ID       string     `json:"id"`
	Status   Status     `json:"status"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
	Host     string     `json:"host"`
	Version  string     `json:"version"`
	Mode     string     `json:"mode"`
	Settings Settings   `json:"settings"`
}

// Cell is one (target, layer) with statistics per phase and every sample.
type Cell struct {
	SeriesKey
	Phases  []PhaseStats   `json:"phases"`
	Samples []SampleReport `json:"samples"`
}

// PhaseStats summarizes one phase over measured (non-warmup), marked samples.
type PhaseStats struct {
	Phase  int    `json:"phase"`
	Label  string `json:"label"` // "startup", "seek 50%": the trend key across runs
	N      int    `json:"n"`
	Failed int    `json:"failed"`

	Min  time.Duration `json:"min_ns"`
	P50  time.Duration `json:"p50_ns"`
	P90  time.Duration `json:"p90_ns"`
	Max  time.Duration `json:"max_ns"`
	Mean time.Duration `json:"mean_ns"`

	Requests float64       `json:"requests_p50"`
	Bytes    int64         `json:"bytes_p50"`
	TTFB     time.Duration `json:"first_ttfb_p50_ns"` // first attributed op
}

// SampleReport is one stored sample plus its analysis, for drill-down.
type SampleReport struct {
	Sample
	Analysis Analysis `json:"analysis"`
}

// Summarize derives the Report from a stored run. Pure.
func Summarize(run *Run) Report {
	// TODO:
	//   targets by name → plan
	//   one Cell per stored Series, in stored order, even when it has no
	//   samples: the grid is the plan, so a layer that failed every sample
	//   (or a target that could not be planned) stays visible
	//   for each series: analyses = Analyze(s.Timeline, plan) per sample
	//   per phase: latencies of !Warmup && OK samples → sort → nearest-rank
	//   stats; Failed counts measured samples whose phase was not marked
	//   No cross-layer subtraction: layers measure different workloads.
	panic("not implemented")
}

// RunSummary is one point per trend line: GET /api/runs.
type RunSummary struct {
	RunHeader
	Points []TrendPoint `json:"points"`
}

// TrendPoint is the median of one phase label in one cell.
type TrendPoint struct {
	Target string        `json:"target"`
	Layer  config.Layer  `json:"layer"`
	Label  string        `json:"label"`
	P50    time.Duration `json:"p50_ns"`
	N      int           `json:"n"`
}

// Trend projects a report onto its trend points.
func (r Report) Trend() RunSummary { panic("not implemented") }
