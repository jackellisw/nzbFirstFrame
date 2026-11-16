package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ApplyDerived fills defaults that depend on other settings, e.g. layers
// inferred from which stack sections are configured. Call it after CLI
// overrides and before Validate.
func (c *Config) ApplyDerived() {
	if len(c.Profile.Layers) == 0 {
		c.Profile.Layers = c.configuredLayers()
	}
	if c.Results.Dir == "" {
		c.Results.Dir = defaultResultsDir()
	}
	for i := range c.Targets {
		if c.Targets[i].Name == "" {
			c.Targets[i].Name = fmt.Sprintf("target-%d", i+1)
		}
	}
}

func defaultResultsDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "nzbff", "results")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "nzbff", "results")
}

// Finalize is ApplyDerived followed by Validate.
func (c *Config) Finalize() error {
	c.ApplyDerived()
	return c.Validate()
}

// Validate checks the config is internally consistent and that every selected
// layer has what it needs. All problems are reported together.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	// Profile
	p := c.Profile
	if len(p.Layers) == 0 {
		add("no layers to measure: configure at least one of jellyfin.url, rclone.mount_path, infinidysk.url, usenet.host (or set profile.layers)")
	}
	seen := map[Layer]bool{}
	for _, l := range p.Layers {
		if !slices.Contains(AllLayers, l) {
			add("profile.layers: unknown layer %q (want one of %s)", l, joinLayers(AllLayers))
		}
		if seen[l] {
			add("profile.layers: %q listed twice", l)
		}
		seen[l] = true
	}
	if p.Iterations < 1 {
		add("profile.iterations must be >= 1 (got %d)", p.Iterations)
	}
	if p.Warmup < 0 {
		add("profile.warmup must be >= 0 (got %d)", p.Warmup)
	}
	if p.Timeout <= 0 {
		add("profile.timeout must be > 0")
	}
	if p.Cooldown < 0 {
		add("profile.cooldown must be >= 0")
	}
	if p.ColdCache && c.Rclone.RCURL == "" {
		add("profile.cold_cache requires rclone.rc_url (cache is flushed via vfs/forget)")
	}

	// Layer connection settings
	if c.HasLayer(LayerJellyfin) {
		checkURL(&errs, "jellyfin.url", c.Jellyfin.URL)
		if c.Jellyfin.APIKey == "" {
			add("jellyfin.api_key is required for the jellyfin layer")
		}
	}
	if c.HasLayer(LayerRclone) && c.Rclone.MountPath == "" {
		add("rclone.mount_path is required for the rclone layer")
	}
	if c.Rclone.RCURL != "" {
		checkURL(&errs, "rclone.rc_url", c.Rclone.RCURL)
	}
	if c.HasLayer(LayerInfinidysk) {
		checkURL(&errs, "infinidysk.url", c.Infinidysk.URL)
	}
	if c.HasLayer(LayerUsenet) {
		if c.Usenet.Host == "" {
			add("usenet.host is required for the usenet layer")
		}
		if c.Usenet.Port < 1 || c.Usenet.Port > 65535 {
			add("usenet.port must be 1-65535 (got %d)", c.Usenet.Port)
		}
	}

	// Targets
	if len(c.Targets) == 0 {
		add("no targets: add entries under `targets:` or pass -path / -item-id")
	}
	names := map[string]bool{}
	for i, t := range c.Targets {
		label := fmt.Sprintf("targets[%d]", i)
		if t.Name != "" {
			label = fmt.Sprintf("targets[%d] (%s)", i, t.Name)
			if names[t.Name] {
				add("%s: duplicate target name", label)
			}
			names[t.Name] = true
		}
		if c.HasLayer(LayerJellyfin) && t.JellyfinItemID == "" {
			add("%s: jellyfin_item_id is required for the jellyfin layer", label)
		}
		if (c.HasLayer(LayerRclone) || c.HasLayer(LayerInfinidysk)) && t.Path == "" {
			add("%s: path is required for the rclone/infinidysk layers", label)
		}
		if strings.HasPrefix(t.Path, "/") {
			add("%s: path must be relative to the mount/infinidysk root (got %q)", label, t.Path)
		}
		if c.HasLayer(LayerUsenet) && t.NZB == "" {
			add("%s: nzb is required for the usenet layer", label)
		}
	}

	// Tools / output / logging
	if c.Tools.FFprobe == "" {
		add("tools.ffprobe must not be empty")
	}
	if c.Tools.FFmpeg == "" {
		add("tools.ffmpeg must not be empty")
	}
	switch c.Output.Format {
	case FormatTable, FormatJSON, FormatCSV:
	default:
		add("output.format: unknown format %q (want table, json or csv)", c.Output.Format)
	}
	switch strings.ToLower(c.Log.Level) {
	case "debug", "info", "warn", "error":
	default:
		add("log.level: unknown level %q (want debug, info, warn or error)", c.Log.Level)
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		add("log.format: unknown format %q (want text or json)", c.Log.Format)
	}

	return errors.Join(errs...)
}

func checkURL(errs *[]error, field, raw string) {
	if raw == "" {
		*errs = append(*errs, fmt.Errorf("%s is required", field))
		return
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		*errs = append(*errs, fmt.Errorf("%s: want an http(s) URL, got %q", field, raw))
	}
}

func joinLayers(ls []Layer) string {
	s := make([]string, len(ls))
	for i, l := range ls {
		s[i] = string(l)
	}
	return strings.Join(s, ", ")
}

// ParseLayers parses a comma-separated layer list, e.g. "jellyfin,rclone".
func ParseLayers(s string) ([]Layer, error) {
	var out []Layer
	for _, part := range strings.Split(s, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		if part == "all" {
			return slices.Clone(AllLayers), nil
		}
		l := Layer(part)
		if !slices.Contains(AllLayers, l) {
			return nil, fmt.Errorf("unknown layer %q (want one of %s, or all)", part, joinLayers(AllLayers))
		}
		out = append(out, l)
	}
	return out, nil
}

// Redacted returns a copy with secrets masked, safe to print.
func (c *Config) Redacted() *Config {
	r := *c
	mask := func(s *string) {
		if *s != "" {
			*s = "********"
		}
	}
	mask(&r.Jellyfin.APIKey)
	mask(&r.Rclone.RCPassword)
	mask(&r.Infinidysk.Password)
	mask(&r.Usenet.Password)
	return &r
}
