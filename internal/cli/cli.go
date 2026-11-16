// Package cli implements the nzbff command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/jackellisw/nzbfirstframe/internal/config"
	"github.com/jackellisw/nzbfirstframe/internal/version"
)

const usage = `nzbff — first-frame & seek profiler for jellyfin -> rclone -> infinidysk -> usenet

Usage:
  nzbff <command> [flags]

Commands:
  run        Measure startup (first frame) and seek times
  startup    Measure startup time to first frame only
  seek       Measure seek times only
  serve      Serve the results dashboard
  config     Inspect, validate or create a config file
  version    Print version information

Run 'nzbff <command> -h' for command flags.
`

// codeError carries a specific exit code out of a command.
type codeError struct{ code int }

func (e codeError) Error() string { return fmt.Sprintf("exit %d", e.code) }

// Exit codes.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

// Main runs the CLI and returns the process exit code.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}

	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "run":
		err = runProfile(ctx, "run", rest, stdout, stderr)
	case "startup":
		err = runProfile(ctx, "startup", rest, stdout, stderr)
	case "seek":
		err = runProfile(ctx, "seek", rest, stdout, stderr)
	case "serve":
		err = runServe(ctx, rest, stdout, stderr)
	case "dev-stack":
		err = runDevStack(ctx, rest, stdout, stderr)
	case "config":
		err = runConfig(rest, stdout, stderr)
	case "version", "-version", "--version":
		fmt.Fprintln(stdout, version.String())
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
	default:
		fmt.Fprintf(stderr, "nzbff: unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}

	var ee codeError
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, flag.ErrHelp):
		return exitOK
	case errors.As(err, &ee):
		return ee.code
	default:
		fmt.Fprintf(stderr, "nzbff %s: %s\n", cmd, indentErr(err))
		return exitFail
	}
}

// newFlagSet returns a FlagSet that reports errors to the caller instead of
// exiting, with a usage header for the command.
func newFlagSet(name, synopsis string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("nzbff "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: nzbff %s\n\nFlags:\n", synopsis)
		fs.PrintDefaults()
	}
	return fs
}

// parseFlags parses args, mapping flag errors to a usage exit code.
func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return codeError{exitUsage} // flag package already printed the error + usage
	}
	return nil
}

// newLogger builds the logger from resolved config.
func newLogger(cfg *config.Config, w io.Writer) *slog.Logger {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(cfg.Log.Level)) // validated already; falls back to info
	opts := &slog.HandlerOptions{Level: lvl}
	if cfg.Log.Format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// indentErr puts each joined error on its own line.
func indentErr(err error) string {
	msg := err.Error()
	if !strings.Contains(msg, "\n") {
		return msg
	}
	return "\n  " + strings.ReplaceAll(msg, "\n", "\n  ")
}
