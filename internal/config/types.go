package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that reads and writes as "30s", "1m30s", etc.
type Duration time.Duration

func (d Duration) D() time.Duration          { return time.Duration(d) }
func (d Duration) String() string            { return time.Duration(d).String() }
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q (want e.g. 30s, 1m30s)", n.Line, s)
	}
	*d = Duration(v)
	return nil
}

// SeekPoint is a position within a media item, either relative to its
// runtime ("50%") or absolute ("90s", "1m30s", "01:02:03", "02:03").
type SeekPoint struct {
	raw       string
	Percent   float64       // 0-100, valid when IsPercent
	Offset    time.Duration // valid when !IsPercent
	IsPercent bool
}

// ParseSeekPoint parses a seek position.
func ParseSeekPoint(s string) (SeekPoint, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return SeekPoint{}, fmt.Errorf("empty seek point")
	}
	if p, ok := strings.CutSuffix(s, "%"); ok {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || v < 0 || v > 100 {
			return SeekPoint{}, fmt.Errorf("invalid seek percentage %q (want 0-100%%)", s)
		}
		return SeekPoint{raw: s, Percent: v, IsPercent: true}, nil
	}
	if strings.Contains(s, ":") {
		d, err := parseClock(s)
		if err != nil {
			return SeekPoint{}, err
		}
		return SeekPoint{raw: s, Offset: d}, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return SeekPoint{}, fmt.Errorf("invalid seek point %q (want e.g. 50%%, 90s, 01:02:03)", s)
	}
	return SeekPoint{raw: s, Offset: d}, nil
}

// ParseSeekPoints parses a comma-separated list, e.g. "10%,50%,00:20:00".
func ParseSeekPoints(s string) ([]SeekPoint, error) {
	var out []SeekPoint
	for _, part := range strings.Split(s, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		p, err := ParseSeekPoint(part)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Resolve returns the absolute offset for a media item of the given runtime.
func (p SeekPoint) Resolve(runtime time.Duration) time.Duration {
	if p.IsPercent {
		return time.Duration(float64(runtime) * p.Percent / 100)
	}
	return p.Offset
}

func (p SeekPoint) String() string { return p.raw }

func (p SeekPoint) MarshalYAML() (any, error) { return p.raw, nil }

func (p *SeekPoint) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := ParseSeekPoint(s)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*p = v
	return nil
}

// parseClock parses HH:MM:SS or MM:SS (seconds may be fractional).
func parseClock(s string) (time.Duration, error) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("invalid timestamp %q (want HH:MM:SS or MM:SS)", s)
	}
	var total float64
	for i, part := range parts {
		v, err := strconv.ParseFloat(part, 64)
		if err != nil || v < 0 || (i > 0 && v >= 60) {
			return 0, fmt.Errorf("invalid timestamp %q (want HH:MM:SS or MM:SS)", s)
		}
		total = total*60 + v
	}
	return time.Duration(total * float64(time.Second)), nil
}

func mustSeek(s string) SeekPoint {
	p, err := ParseSeekPoint(s)
	if err != nil {
		panic(err)
	}
	return p
}
