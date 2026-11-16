package cli

import (
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

// overrides collects CLI flags that override config values. Flags are parsed
// before the config file is loaded (the file path is itself a flag), so each
// flag validates its value immediately and queues a setter to apply once the
// config exists. Only flags actually passed are applied.
type overrides struct {
	fs      *flag.FlagSet
	setters []func(*config.Config)
}

func newOverrides(fs *flag.FlagSet) *overrides { return &overrides{fs: fs} }

func (o *overrides) apply(cfg *config.Config) {
	for _, set := range o.setters {
		set(cfg)
	}
}

// str registers a string flag that sets a config field.
func (o *overrides) str(name, usage string, field func(*config.Config) *string) {
	o.fs.Func(name, usage, func(v string) error {
		o.setters = append(o.setters, func(c *config.Config) { *field(c) = v })
		return nil
	})
}

func (o *overrides) int(name, usage string, field func(*config.Config) *int) {
	o.fs.Func(name, usage, func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("want an integer")
		}
		o.setters = append(o.setters, func(c *config.Config) { *field(c) = n })
		return nil
	})
}

func (o *overrides) bool(name, usage string, field func(*config.Config) *bool) {
	o.fs.Var(boolFlag(func(b bool) {
		o.setters = append(o.setters, func(c *config.Config) { *field(c) = b })
	}), name, usage)
}

func (o *overrides) duration(name, usage string, field func(*config.Config) *config.Duration) {
	o.fs.Func(name, usage, func(v string) error {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("want a duration like 30s or 1m30s")
		}
		o.setters = append(o.setters, func(c *config.Config) { *field(c) = config.Duration(d) })
		return nil
	})
}

// custom registers a flag with its own parse-and-apply logic.
func (o *overrides) custom(name, usage string, parse func(string) (func(*config.Config), error)) {
	o.fs.Func(name, usage, func(v string) error {
		set, err := parse(v)
		if err != nil {
			return err
		}
		o.setters = append(o.setters, set)
		return nil
	})
}

// commonFlags are accepted by every command that reads config.
type commonFlags struct {
	configPath string
}

func registerCommon(fs *flag.FlagSet, o *overrides) *commonFlags {
	c := &commonFlags{}
	fs.StringVar(&c.configPath, "config", "", "config file `path` (default: $NZBFF_CONFIG, ./nzbff.yaml, then user config dir)")
	fs.StringVar(&c.configPath, "c", "", "shorthand for -config")
	fs.Var(boolFlag(func(b bool) {
		if b {
			o.setters = append(o.setters, func(c *config.Config) { c.Log.Level = "debug" })
		}
	}), "v", "verbose: shorthand for -log-level debug")
	o.str("log-level", "log `level`: debug, info, warn, error", func(c *config.Config) *string { return &c.Log.Level })
	o.str("log-format", "log `format`: text or json", func(c *config.Config) *string { return &c.Log.Format })
	return c
}

// boolFlag is a flag.Value for a boolean flag with a callback.
type boolFlag func(bool)

func (f boolFlag) String() string   { return "" }
func (f boolFlag) IsBoolFlag() bool { return true }
func (f boolFlag) Set(s string) error {
	b, err := strconv.ParseBool(s)
	if err != nil {
		return fmt.Errorf("want true or false")
	}
	f(b)
	return nil
}

// registerProfile registers the flags shared by run/startup/seek.
func registerProfile(fs *flag.FlagSet, o *overrides, withSeek bool) *profileFlags {
	p := &profileFlags{}

	// Selection
	o.custom("layers", "comma-separated `layers` to measure: jellyfin,rclone,infinidysk,usenet or all (default: every configured layer)",
		func(v string) (func(*config.Config), error) {
			ls, err := config.ParseLayers(v)
			if err != nil {
				return nil, err
			}
			return func(c *config.Config) { c.Profile.Layers = ls }, nil
		})
	fs.Func("target", "only profile config targets with this `name` (repeatable)", func(v string) error {
		p.targetNames = append(p.targetNames, v)
		return nil
	})
	fs.StringVar(&p.adhoc.Path, "path", "", "ad-hoc target: file `path` relative to the rclone mount / infinidysk root (replaces config targets)")
	fs.StringVar(&p.adhoc.JellyfinItemID, "item-id", "", "ad-hoc target: Jellyfin item `id` (replaces config targets)")
	fs.StringVar(&p.adhoc.NZB, "nzb", "", "ad-hoc target: `.nzb file` for the usenet layer (replaces config targets)")

	// Measurement
	o.int("iterations", "`n` measured runs per target per layer", func(c *config.Config) *int { return &c.Profile.Iterations })
	o.int("warmup", "`n` discarded warmup runs before measuring", func(c *config.Config) *int { return &c.Profile.Warmup })
	o.duration("timeout", "per-measurement `timeout`, e.g. 60s", func(c *config.Config) *config.Duration { return &c.Profile.Timeout })
	o.duration("cooldown", "pause between iterations (`duration`, e.g. 2s)", func(c *config.Config) *config.Duration { return &c.Profile.Cooldown })
	o.bool("cold-cache", "flush the rclone VFS cache before every iteration (needs rclone.rc_url)", func(c *config.Config) *bool { return &c.Profile.ColdCache })
	if withSeek {
		o.custom("seek", "comma-separated seek `points`, e.g. 10%,50%,00:42:00,90s", func(v string) (func(*config.Config), error) {
			pts, err := config.ParseSeekPoints(v)
			if err != nil {
				return nil, err
			}
			if len(pts) == 0 {
				return nil, fmt.Errorf("no seek points given")
			}
			return func(c *config.Config) { c.Profile.SeekPoints = pts }, nil
		})
	}

	// Stack endpoints
	o.str("jellyfin-url", "Jellyfin base `url`", func(c *config.Config) *string { return &c.Jellyfin.URL })
	o.str("rclone-mount", "rclone mount `path`", func(c *config.Config) *string { return &c.Rclone.MountPath })
	o.str("rclone-rc-url", "rclone remote-control `url`", func(c *config.Config) *string { return &c.Rclone.RCURL })
	o.str("infinidysk-url", "infinidysk base `url`", func(c *config.Config) *string { return &c.Infinidysk.URL })
	o.str("usenet-host", "usenet provider `host`", func(c *config.Config) *string { return &c.Usenet.Host })

	// Tools & output
	o.str("ffprobe", "ffprobe binary `path`", func(c *config.Config) *string { return &c.Tools.FFprobe })
	o.str("ffmpeg", "ffmpeg binary `path`", func(c *config.Config) *string { return &c.Tools.FFmpeg })
	o.custom("format", "output `format`: table, json, csv", func(v string) (func(*config.Config), error) {
		f := config.OutputFormat(strings.ToLower(v))
		return func(c *config.Config) { c.Output.Format = f }, nil
	})
	o.str("o", "write results to `file` (default stdout)", func(c *config.Config) *string { return &c.Output.Path })
	o.str("output", "write results to `file` (same as -o)", func(c *config.Config) *string { return &c.Output.Path })
	o.str("results", "store the run in `dir` (default: results.dir from the config)", func(c *config.Config) *string { return &c.Results.Dir })

	fs.BoolVar(&p.dryRun, "dry-run", false, "print the resolved plan and exit without measuring")
	return p
}

type profileFlags struct {
	targetNames []string
	adhoc       config.Target
	dryRun      bool
}

// applyTargets narrows or replaces cfg.Targets based on -target / ad-hoc flags.
func (p *profileFlags) applyTargets(cfg *config.Config) error {
	if p.adhoc != (config.Target{}) {
		if len(p.targetNames) > 0 {
			return fmt.Errorf("-target can't be combined with -path/-item-id/-nzb")
		}
		t := p.adhoc
		t.Name = "adhoc"
		cfg.Targets = []config.Target{t}
		return nil
	}
	if len(p.targetNames) == 0 {
		return nil
	}
	byName := make(map[string]config.Target, len(cfg.Targets))
	for _, t := range cfg.Targets {
		byName[t.Name] = t
	}
	var out []config.Target
	for _, n := range p.targetNames {
		t, ok := byName[n]
		if !ok {
			return fmt.Errorf("-target %q: no such target in config", n)
		}
		out = append(out, t)
	}
	cfg.Targets = out
	return nil
}
