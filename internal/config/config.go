// Package config defines the profiler configuration and how it is loaded.
//
// Values are resolved in increasing order of precedence:
//
//	built-in defaults < config file < CLI flags
//
// The config file may reference environment variables as ${VAR} so secrets
// (API keys, passwords) don't have to live in the file itself.
package config

import "time"

// Layer is one hop in the playback stack that the profiler can measure on its
// own. Measuring each hop separately lets us attribute latency:
//
//	jellyfin -> rclone mount -> infinidysk -> usenet provider
type Layer string

const (
	LayerJellyfin   Layer = "jellyfin"   // HTTP stream via the Jellyfin API (full stack)
	LayerRclone     Layer = "rclone"     // file reads on the rclone mount (skips Jellyfin)
	LayerInfinidysk Layer = "infinidysk" // HTTP reads straight from infinidysk (skips rclone)
	LayerUsenet     Layer = "usenet"     // raw NNTP connect/article latency (provider baseline)
)

// AllLayers lists every known layer, top of the stack first.
var AllLayers = []Layer{LayerJellyfin, LayerRclone, LayerInfinidysk, LayerUsenet}

// OutputFormat selects how results are rendered.
type OutputFormat string

const (
	FormatTable OutputFormat = "table"
	FormatJSON  OutputFormat = "json"
	FormatCSV   OutputFormat = "csv"
)

// Config is the full, resolved profiler configuration.
type Config struct {
	Jellyfin   JellyfinConfig   `yaml:"jellyfin"`
	Rclone     RcloneConfig     `yaml:"rclone"`
	Infinidysk InfinidyskConfig `yaml:"infinidysk"`
	Usenet     UsenetConfig     `yaml:"usenet"`

	Profile ProfileConfig `yaml:"profile"`
	Tools   ToolsConfig   `yaml:"tools"`
	Output  OutputConfig  `yaml:"output"`
	Log     LogConfig     `yaml:"log"`

	Targets []Target `yaml:"targets"`

	// Source is the config file this was loaded from ("" if defaults only).
	Source string `yaml:"-"`
}

type JellyfinConfig struct {
	URL                string `yaml:"url"`
	APIKey             string `yaml:"api_key"`
	UserID             string `yaml:"user_id"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
}

type RcloneConfig struct {
	// MountPath is the local directory where the rclone remote is mounted.
	MountPath string `yaml:"mount_path"`
	// RCURL is the rclone remote-control endpoint (rclone rcd / --rc). Optional;
	// needed for cold-cache runs (vfs/forget) and VFS stats.
	RCURL      string `yaml:"rc_url"`
	RCUser     string `yaml:"rc_user"`
	RCPassword string `yaml:"rc_password"`
}

type InfinidyskConfig struct {
	// URL is the base HTTP/WebDAV endpoint that rclone mounts.
	URL                string `yaml:"url"`
	Username           string `yaml:"username"`
	Password           string `yaml:"password"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
}

type UsenetConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	TLS      bool   `yaml:"tls"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type ProfileConfig struct {
	// Layers to measure. Defaults to every layer that has enough config.
	Layers []Layer `yaml:"layers"`
	// Iterations is the number of measured runs per target per layer.
	Iterations int `yaml:"iterations"`
	// Warmup runs are executed first and discarded.
	Warmup int `yaml:"warmup"`
	// Timeout bounds a single first-frame or seek measurement.
	Timeout Duration `yaml:"timeout"`
	// Cooldown is the pause between iterations.
	Cooldown Duration `yaml:"cooldown"`
	// ColdCache flushes the rclone VFS cache (via rc) before every iteration.
	ColdCache bool `yaml:"cold_cache"`
	// SeekPoints are the positions to seek to, e.g. "10%", "50%", "00:42:00", "90s".
	SeekPoints []SeekPoint `yaml:"seek_points"`
}

type ToolsConfig struct {
	FFprobe string `yaml:"ffprobe"`
	FFmpeg  string `yaml:"ffmpeg"`
}

type OutputConfig struct {
	Format OutputFormat `yaml:"format"`
	// Path to write results to; empty or "-" means stdout.
	Path string `yaml:"path"`
}

type LogConfig struct {
	Level  string `yaml:"level"`  // debug, info, warn, error
	Format string `yaml:"format"` // text, json
}

// Target is one media item to profile.
type Target struct {
	Name string `yaml:"name"`
	// JellyfinItemID is required for the jellyfin layer.
	JellyfinItemID string `yaml:"jellyfin_item_id"`
	// Path is relative to both the rclone mount root and the infinidysk root.
	// Required for the rclone and infinidysk layers.
	Path string `yaml:"path"`
	// NZB is an optional path to the source .nzb, used by the usenet layer to
	// pick real message IDs to fetch.
	NZB string `yaml:"nzb"`
}

// Default returns the built-in defaults.
func Default() *Config {
	return &Config{
		Usenet: UsenetConfig{Port: 563, TLS: true},
		Profile: ProfileConfig{
			Iterations: 5,
			Warmup:     1,
			Timeout:    Duration(60 * time.Second),
			Cooldown:   Duration(2 * time.Second),
			SeekPoints: []SeekPoint{
				mustSeek("10%"), mustSeek("50%"), mustSeek("90%"),
			},
		},
		Tools:  ToolsConfig{FFprobe: "ffprobe", FFmpeg: "ffmpeg"},
		Output: OutputConfig{Format: FormatTable},
		Log:    LogConfig{Level: "info", Format: "text"},
	}
}

// HasLayer reports whether l is in the configured layer set.
func (c *Config) HasLayer(l Layer) bool {
	for _, x := range c.Profile.Layers {
		if x == l {
			return true
		}
	}
	return false
}

// configuredLayers returns the layers that have enough connection config to
// be measured. Used when profile.layers is left empty.
func (c *Config) configuredLayers() []Layer {
	var out []Layer
	if c.Jellyfin.URL != "" {
		out = append(out, LayerJellyfin)
	}
	if c.Rclone.MountPath != "" {
		out = append(out, LayerRclone)
	}
	if c.Infinidysk.URL != "" {
		out = append(out, LayerInfinidysk)
	}
	if c.Usenet.Host != "" {
		out = append(out, LayerUsenet)
	}
	return out
}
