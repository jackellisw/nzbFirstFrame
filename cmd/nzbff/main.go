// Command nzbff profiles startup (time to first frame) and seek latency
// across a jellyfin -> rclone -> infinidysk -> usenet streaming stack.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackellisw/nzbfirstframe/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Main(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
