package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
	"github.com/jackellisw/nzbfirstframe/internal/dashboard"
	"github.com/jackellisw/nzbfirstframe/internal/results"
)

// runServe implements `nzbff serve [-listen 127.0.0.1:8484] [-results dir]`:
// open the results store from config (results.dir) and serve
// dashboard.New(store) until ctx is cancelled (graceful shutdown, 5s).
// Needs no stack config; only the results directory.
func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("serve", "serve [flags]", stderr)
	o := newOverrides(fs)
	common := registerCommon(fs, o)
	listen := fs.String("listen", "127.0.0.1:8484", "`address` to serve the dashboard on")
	o.str("results", "results `dir` to read (default: results.dir from the config)", func(c *config.Config) *string { return &c.Results.Dir })
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		fs.Usage()
		return codeError{exitUsage}
	}

	cfg, err := loadConfig(common, o)
	if err != nil {
		return err
	}
	// Defaults only: validating would demand stack endpoints serve never uses.
	cfg.ApplyDerived()
	if cfg.Results.Dir == "" {
		return errors.New("no results directory: set results.dir in the config or pass -results")
	}
	store, err := results.Open(cfg.Results.Dir)
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           dashboard.New(store),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(newLogger(cfg, stderr).Handler(), slog.LevelWarn),
	}
	fmt.Fprintf(stdout, "dashboard on http://%s/ reading %s\n", ln.Addr(), store.Dir())

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}
