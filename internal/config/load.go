package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// EnvConfigPath names the environment variable that can point at a config file.
const EnvConfigPath = "NZBFF_CONFIG"

// SearchPaths returns the locations checked, in order, when no config path is
// given explicitly.
func SearchPaths() []string {
	paths := []string{"nzbff.yaml", "nzbff.yml"}
	if dir, err := os.UserConfigDir(); err == nil {
		paths = append(paths, filepath.Join(dir, "nzbff", "config.yaml"))
	}
	// Also honour ~/.config on macOS, where UserConfigDir is ~/Library/Application Support.
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".config", "nzbff", "config.yaml")
		if !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	return paths
}

// Resolve picks the config file to load. An explicit path (from a flag) wins,
// then $NZBFF_CONFIG, then the first existing entry of SearchPaths. Explicit
// and env paths must exist; if nothing is found in the search paths, Resolve
// returns "" and the caller runs on defaults + flags.
func Resolve(explicit string) (string, error) {
	for _, p := range []string{explicit, os.Getenv(EnvConfigPath)} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("config file: %w", err)
		}
		return p, nil
	}
	for _, p := range SearchPaths() {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("config file: %w", err)
		}
	}
	return "", nil
}

// Load resolves and loads the config file (see Resolve), layered over the
// defaults. It does not validate; call Validate once CLI overrides are applied.
func Load(explicit string) (*Config, error) {
	path, err := Resolve(explicit)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return Default(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config file: %w", err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cfg.Source = path
	return cfg, nil
}

// Parse decodes YAML config over the defaults. Unknown keys are rejected so
// typos don't silently fall back to defaults. ${VAR} and ${VAR:-fallback}
// references in values are expanded from the environment.
func Parse(data []byte) (*Config, error) {
	cfg := Default()

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if root.Kind == 0 { // empty document
		return cfg, nil
	}
	if err := expandNode(&root); err != nil {
		return nil, err
	}
	// Re-encode the expanded tree so we can decode it strictly
	// (yaml.Node.Decode has no KnownFields option).
	expanded, err := yaml.Marshal(&root)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(expanded))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// expandNode expands ${VAR} references in scalar values only, so comments
// and keys are left alone.
func expandNode(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && strings.Contains(n.Value, "${") {
		var missing []string
		n.Value = envRef.ReplaceAllStringFunc(n.Value, func(m string) string {
			sub := envRef.FindStringSubmatch(m)
			if v, ok := os.LookupEnv(sub[1]); ok && v != "" {
				return v
			}
			if sub[2] != "" { // has :-fallback
				return sub[3]
			}
			missing = append(missing, sub[1])
			return ""
		})
		if len(missing) > 0 {
			return fmt.Errorf("line %d: environment variable(s) not set: %s", n.Line, strings.Join(missing, ", "))
		}
		// An unquoted reference re-resolves its type from the expanded value,
		// so `port: ${NNTP_PORT}` still decodes as an int. Quoted references
		// keep their !!str tag.
		if n.Style == 0 {
			n.Tag = ""
		}
	}
	for _, c := range n.Content {
		if err := expandNode(c); err != nil {
			return err
		}
	}
	return nil
}
