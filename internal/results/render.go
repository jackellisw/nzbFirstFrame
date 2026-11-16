package results

import (
	"io"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

// Render writes a report in the configured format.
//
//	table  one row per (target, layer, phase): N, p50, p90, min, max, reqs,
//	       bytes; failures listed under the table with their errors
//	json   the Report, indented (same shape as GET /api/runs/{id})
//	csv    one row per (sample, phase): run_id, target, layer, iteration,
//	       warmup, phase, label, ok, latency_ms, requests, bytes,
//	       first_ttfb_ms, wait_ms, transfer_ms, client_ms, err
func Render(w io.Writer, rep Report, format config.OutputFormat) error {
	panic("not implemented")
}
