package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"

	"github.com/jackellisw/nzbfirstframe/internal/config"
	"github.com/jackellisw/nzbfirstframe/internal/profiler"
)

// loadConfig loads the config file and layers the CLI overrides on top.
func loadConfig(common *commonFlags, o *overrides) (*config.Config, error) {
	cfg, err := config.Load(common.configPath)
	if err != nil {
		return nil, err
	}
	o.apply(cfg)
	return cfg, nil
}

func runProfile(ctx context.Context, name string, args []string, stdout, stderr io.Writer) error {
	mode := map[string]profiler.Mode{
		"run":     profiler.ModeAll,
		"startup": profiler.ModeStartup,
		"seek":    profiler.ModeSeek,
	}[name]

	fs := newFlagSet(name, name+" [flags]", stderr)
	o := newOverrides(fs)
	common := registerCommon(fs, o)
	pf := registerProfile(fs, o, mode.Seek())
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
	if err := pf.applyTargets(cfg); err != nil {
		return err
	}
	err = cfg.Finalize()
	if mode.Seek() && len(cfg.Profile.SeekPoints) == 0 {
		err = errors.Join(err, errors.New("no seek points: set profile.seek_points or pass -seek"))
	}
	if cfg.Results.Dir == "" {
		err = errors.Join(err, errors.New("no results directory: set results.dir or pass -results"))
	}
	if err != nil {
		return fmt.Errorf("invalid configuration%s:\n%w", sourceSuffix(cfg), err)
	}

	logger := newLogger(cfg, stderr)
	logger.Debug("config resolved", "source", cfg.Source, "mode", mode, "layers", cfg.Profile.Layers, "targets", len(cfg.Targets))

	if pf.dryRun {
		printPlan(stdout, cfg, mode)
		return nil
	}

	opts := profiler.Options{Config: cfg, Mode: mode, Logger: logger, Out: stdout}
	if p := cfg.Output.Path; p != "" && p != "-" {
		f, err := os.Create(p)
		if err != nil {
			return err
		}
		opts.Out = f
		err = profiler.Run(ctx, opts)
		if cerr := f.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return profileErr(err)
	}
	return profileErr(profiler.Run(ctx, opts))
}

func profileErr(err error) error {
	if errors.Is(err, context.Canceled) {
		return errors.New("interrupted; the partial run is stored")
	}
	return err
}

func printPlan(w io.Writer, cfg *config.Config, mode profiler.Mode) {
	p := cfg.Profile
	src := cfg.Source
	if src == "" {
		src = "(none — defaults + flags)"
	}
	fmt.Fprintf(w, "config:      %s\n", src)
	fmt.Fprintf(w, "mode:        %s\n", mode)
	fmt.Fprintf(w, "layers:      %s\n", joinStringers(p.Layers))
	fmt.Fprintf(w, "iterations:  %d (+%d warmup), timeout %s, cooldown %s, cold cache %t\n",
		p.Iterations, p.Warmup, p.Timeout, p.Cooldown, p.ColdCache)
	if mode.Seek() {
		fmt.Fprintf(w, "seek points: %s\n", joinStringers(p.SeekPoints))
	}
	fmt.Fprintf(w, "output:      %s -> %s\n", cfg.Output.Format, orStdout(cfg.Output.Path))
	fmt.Fprintf(w, "results:     %s\n", cfg.Results.Dir)
	fmt.Fprintf(w, "targets:\n")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  NAME\tJELLYFIN ITEM\tPATH\tNZB")
	for _, t := range cfg.Targets {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", dash(t.Name), dash(t.JellyfinItemID), dash(t.Path), dash(t.NZB))
	}
	tw.Flush()
}

func runConfig(args []string, stdout, stderr io.Writer) error {
	const sub = `Usage: nzbff config <subcommand> [flags]

Subcommands:
  show       Print the effective config (defaults + file + flags), secrets redacted
  validate   Check the config for errors
  init       Write an annotated example config (default ./nzbff.yaml)
  path       Show which config file would be loaded and where we look
`
	if len(args) == 0 {
		fmt.Fprint(stderr, sub)
		return codeError{exitUsage}
	}
	switch args[0] {
	case "show":
		fs := newFlagSet("config show", "config show [flags]", stderr)
		o := newOverrides(fs)
		common := registerCommon(fs, o)
		reveal := fs.Bool("show-secrets", false, "print secrets instead of redacting them")
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		cfg, err := loadConfig(common, o)
		if err != nil {
			return err
		}
		cfg.ApplyDerived()
		if !*reveal {
			cfg = cfg.Redacted()
		}
		fmt.Fprintf(stdout, "# source: %s\n", orNone(cfg.Source))
		enc := yaml.NewEncoder(stdout)
		enc.SetIndent(2)
		if err := enc.Encode(cfg); err != nil {
			return err
		}
		return enc.Close()

	case "validate":
		fs := newFlagSet("config validate", "config validate [flags]", stderr)
		o := newOverrides(fs)
		common := registerCommon(fs, o)
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		cfg, err := loadConfig(common, o)
		if err != nil {
			return err
		}
		if err := cfg.Finalize(); err != nil {
			return fmt.Errorf("invalid configuration%s:\n%w", sourceSuffix(cfg), err)
		}
		fmt.Fprintf(stdout, "ok: %s (layers: %s; %d target(s))\n",
			orNone(cfg.Source), joinStringers(cfg.Profile.Layers), len(cfg.Targets))
		return nil

	case "init":
		fs := newFlagSet("config init", "config init [flags] [path]", stderr)
		force := fs.Bool("force", false, "overwrite an existing file")
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		path := "nzbff.yaml"
		if fs.NArg() > 0 {
			path = fs.Arg(0)
		}
		flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
		if *force {
			flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		}
		f, err := os.OpenFile(path, flags, 0o600) // may hold secrets
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists (use -force to overwrite)", path)
		} else if err != nil {
			return err
		}
		if _, err := f.Write(config.Example); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "wrote %s\n", path)
		return nil

	case "path":
		fs := newFlagSet("config path", "config path [flags]", stderr)
		o := newOverrides(fs)
		common := registerCommon(fs, o)
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		p, err := config.Resolve(common.configPath)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "using: %s\n", orNone(p))
		fmt.Fprintf(stdout, "search order: -config, $%s", config.EnvConfigPath)
		for _, s := range config.SearchPaths() {
			fmt.Fprintf(stdout, ", %s", s)
		}
		fmt.Fprintln(stdout)
		return nil

	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, sub)
		return nil
	default:
		fmt.Fprintf(stderr, "unknown config subcommand %q\n\n%s", args[0], sub)
		return codeError{exitUsage}
	}
}

func sourceSuffix(cfg *config.Config) string {
	if cfg.Source == "" {
		return ""
	}
	return " (" + cfg.Source + ")"
}

func joinStringers[T any](xs []T) string {
	if len(xs) == 0 {
		return "(none)"
	}
	s := make([]string, len(xs))
	for i, x := range xs {
		s[i] = fmt.Sprint(x)
	}
	return strings.Join(s, ", ")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return "(none — using defaults)"
	}
	return s
}

func orStdout(s string) string {
	if s == "" || s == "-" {
		return "stdout"
	}
	return s
}
