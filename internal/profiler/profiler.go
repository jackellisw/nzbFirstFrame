// Package profiler runs first-frame and seek measurements across the stack.
//
// Not implemented yet — this is the seam the CLI calls into.
package profiler

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

// Mode selects which measurements to run.
type Mode string

const (
	ModeAll     Mode = "all"     // startup, then seeks
	ModeStartup Mode = "startup" // time to first frame from a cold open
	ModeSeek    Mode = "seek"    // time to first frame after seeking to each seek point
)

func (m Mode) Startup() bool { return m == ModeAll || m == ModeStartup }
func (m Mode) Seek() bool    { return m == ModeAll || m == ModeSeek }

// Options configures a profiling run.
type Options struct {
	Config *config.Config
	Mode   Mode
	Logger *slog.Logger
	Out    io.Writer // where results are rendered
}

// ErrNotImplemented is returned until measurement lands.
var ErrNotImplemented = errors.New("profiling is not implemented yet (use -dry-run to inspect the plan)")

// Run executes the profile described by opts.
func Run(ctx context.Context, opts Options) error {
	_ = ctx
	return ErrNotImplemented
}
