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
