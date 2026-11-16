package cli

import (
	"context"
	"io"
)

// runDevStack implements `nzbff dev-stack [-media f] [-ttfb 200ms] ...`:
// fakestack.Start, print a YAML config pointing at it (secrets included; it
// is all loopback), then block until ctx is cancelled. Lets a developer run
// `nzbff run -c fake.yaml` and `nzbff serve` against known latency.
// Not listed in usage.
func runDevStack(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	panic("not implemented")
}
