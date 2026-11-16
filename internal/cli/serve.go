package cli

import (
	"context"
	"io"
)

// runServe implements `nzbff serve [-listen 127.0.0.1:8484] [-results dir]`:
// open the results store from config (results.dir) and serve
// dashboard.New(store) until ctx is cancelled (graceful shutdown, 5s).
// Needs no stack config; only the results directory.
func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	panic("not implemented")
}

// runDevStack implements `nzbff dev-stack [-media f] [-ttfb 200ms] ...`:
// fakestack.Start, print a YAML config pointing at it (secrets included; it
// is all loopback), then block until ctx is cancelled. Lets a developer run
// `nzbff run -c fake.yaml` and `nzbff serve` against known latency.
// Not listed in usage.
func runDevStack(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	panic("not implemented")
}
