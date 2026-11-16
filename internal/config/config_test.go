package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestExampleConfigLoadsAndValidates(t *testing.T) {
	t.Setenv("NZBFF_JELLYFIN_API_KEY", "secret")
	cfg, err := Parse(Example)
	if err != nil {
		t.Fatalf("parse example: %v", err)
	}
	if err := cfg.Finalize(); err != nil {
		t.Fatalf("example should validate: %v", err)
	}
	if got := cfg.Jellyfin.APIKey; got != "secret" {
		t.Errorf("api_key = %q, want env value", got)
	}
	want := []Layer{LayerJellyfin, LayerRclone, LayerInfinidysk}
	if strings.Join(toStrings(cfg.Profile.Layers), ",") != strings.Join(toStrings(want), ",") {
		t.Errorf("inferred layers = %v, want %v", cfg.Profile.Layers, want)
	}
}

func TestParseKeepsDefaultsForUnsetKeys(t *testing.T) {
	cfg, err := Parse([]byte("profile:\n  iterations: 9\n"))
	if err != nil {
		t.Fatal(err)
	}
	d := Default()
	if cfg.Profile.Iterations != 9 {
		t.Errorf("iterations = %d, want 9", cfg.Profile.Iterations)
	}
	if cfg.Profile.Timeout != d.Profile.Timeout || cfg.Tools.FFprobe != d.Tools.FFprobe {
		t.Errorf("defaults lost: %+v", cfg.Profile)
	}
}

func TestParseEmpty(t *testing.T) {
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile.Iterations != Default().Profile.Iterations {
		t.Error("empty file should yield defaults")
	}
}

func TestParseReplacesDefaultSlices(t *testing.T) {
	cfg, err := Parse([]byte(`profile: {seek_points: ["25%"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Profile.SeekPoints) != 1 || cfg.Profile.SeekPoints[0].String() != "25%" {
		t.Errorf("seek_points = %v, want [25%%]", cfg.Profile.SeekPoints)
	}
}

func TestParseRejectsUnknownKeys(t *testing.T) {
	_, err := Parse([]byte("jellyfin:\n  apikey: x\n"))
	if err == nil || !strings.Contains(err.Error(), "apikey") {
		t.Fatalf("want unknown-field error mentioning apikey, got %v", err)
	}
}

func TestParseBadDuration(t *testing.T) {
	_, err := Parse([]byte("profile:\n  timeout: soon\n"))
	if err == nil || !strings.Contains(err.Error(), "invalid duration") {
		t.Fatalf("want duration error, got %v", err)
	}
}

func TestEnvExpansion(t *testing.T) {
	t.Setenv("T_PORT", "119")
	t.Setenv("T_PASS", "123")
	t.Setenv("T_HOST", "news.example.com")
	src := `
# a comment mentioning ${NOT_SET} must be ignored
usenet:
  host: ${T_HOST}
  port: ${T_PORT}
  password: "${T_PASS}"
  username: ${T_USER:-anon}
rclone:
  mount_path: /mnt/${T_HOST}/media
`
	cfg, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	u := cfg.Usenet
	if u.Host != "news.example.com" || u.Port != 119 || u.Password != "123" || u.Username != "anon" {
		t.Errorf("usenet = %+v", u)
	}
	if cfg.Rclone.MountPath != "/mnt/news.example.com/media" {
		t.Errorf("mount_path = %q", cfg.Rclone.MountPath)
	}
}

func TestEnvExpansionMissing(t *testing.T) {
	_, err := Parse([]byte("jellyfin:\n  api_key: ${NZBFF_TEST_DEFINITELY_UNSET}\n"))
	if err == nil || !strings.Contains(err.Error(), "NZBFF_TEST_DEFINITELY_UNSET") {
		t.Fatalf("want missing-env error, got %v", err)
	}
}

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	t.Setenv(EnvConfigPath, "")

	if p, err := Resolve(""); err != nil || p != "" {
		t.Fatalf("no config anywhere: got %q, %v", p, err)
	}
	if _, err := Resolve("missing.yaml"); err == nil {
		t.Fatal("explicit missing path should error")
	}

	os.WriteFile("nzbff.yaml", nil, 0o600)
	if p, _ := Resolve(""); p != "nzbff.yaml" {
		t.Errorf("want ./nzbff.yaml, got %q", p)
	}

	other := filepath.Join(dir, "other.yaml")
	os.WriteFile(other, nil, 0o600)
	t.Setenv(EnvConfigPath, other)
	if p, _ := Resolve(""); p != other {
		t.Errorf("env should beat search paths: got %q", p)
	}
	if p, _ := Resolve("nzbff.yaml"); p != "nzbff.yaml" {
		t.Errorf("explicit should beat env: got %q", p)
	}
}

func TestValidateReportsAllProblems(t *testing.T) {
	cfg := Default()
	cfg.Profile.Layers = []Layer{LayerJellyfin, LayerRclone}
	cfg.Profile.Iterations = 0
	cfg.Profile.ColdCache = true
	cfg.Targets = []Target{{Name: "a", Path: "/abs.mkv"}}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("want errors")
	}
	for _, want := range []string{
		"jellyfin.url is required",
		"jellyfin.api_key",
		"rclone.mount_path",
		"iterations",
		"cold_cache requires rclone.rc_url",
		"jellyfin_item_id is required",
		"must be relative",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
}

func TestValidateNoLayers(t *testing.T) {
	cfg := Default()
	cfg.Targets = []Target{{Path: "x.mkv"}}
	if err := cfg.Finalize(); err == nil || !strings.Contains(err.Error(), "no layers") {
		t.Fatalf("want no-layers error, got %v", err)
	}
}

func TestParseSeekPoint(t *testing.T) {
	const runtime = 2 * time.Hour
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"50%", time.Hour},
		{"0%", 0},
		{"12.5%", 15 * time.Minute},
		{"90s", 90 * time.Second},
		{"1m30s", 90 * time.Second},
		{"01:02:03", time.Hour + 2*time.Minute + 3*time.Second},
		{"02:03", 2*time.Minute + 3*time.Second},
		{"00:00:01.5", 1500 * time.Millisecond},
	}
	for _, c := range cases {
		p, err := ParseSeekPoint(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got := p.Resolve(runtime); got != c.want {
			t.Errorf("%q resolved to %v, want %v", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "101%", "-5%", "abc", "1:60", "1:2:3:4", "-10s"} {
		if _, err := ParseSeekPoint(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestParseLayers(t *testing.T) {
	ls, err := ParseLayers(" Jellyfin, rclone ,")
	if err != nil || len(ls) != 2 || ls[0] != LayerJellyfin || ls[1] != LayerRclone {
		t.Errorf("got %v, %v", ls, err)
	}
	if ls, _ := ParseLayers("all"); len(ls) != len(AllLayers) {
		t.Errorf("all = %v", ls)
	}
	if _, err := ParseLayers("plex"); err == nil {
		t.Error("want error for unknown layer")
	}
}

func TestRedacted(t *testing.T) {
	cfg := Default()
	cfg.Jellyfin.APIKey = "k"
	cfg.Usenet.Password = "p"
	r := cfg.Redacted()
	if r.Jellyfin.APIKey == "k" || r.Usenet.Password == "p" || r.Rclone.RCPassword != "" {
		t.Errorf("redaction wrong: %+v", r)
	}
	if cfg.Jellyfin.APIKey != "k" {
		t.Error("Redacted mutated the original")
	}
}

func toStrings(ls []Layer) []string {
	s := make([]string, len(ls))
	for i, l := range ls {
		s[i] = string(l)
	}
	return s
}

func TestRedactedMasksCredentialsInURLs(t *testing.T) {
	cfg := Default()
	cfg.Jellyfin.URL = "https://jf.example/base?api_key=jf-secret&static=true"
	cfg.Rclone.RCURL = "http://rc:rc-secret@127.0.0.1:5572/?token=tok-secret"
	cfg.Infinidysk.URL = "https://dysk-user:dysk-secret@dysk.example/library?ApiKey=key-secret&X-Emby-Token=emby-secret&access_token=at-secret"
	before := *cfg

	out, err := yaml.Marshal(cfg.Redacted())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"jf-secret", "rc-secret", "tok-secret", "dysk-secret", "key-secret", "emby-secret", "at-secret"} {
		if strings.Contains(string(out), secret) {
			t.Errorf("redacted config shows %q:\n%s", secret, out)
		}
	}
	for _, kept := range []string{"https://jf.example/base?", "static=true", "dysk-user", "dysk.example/library", "127.0.0.1:5572"} {
		if !strings.Contains(string(out), kept) {
			t.Errorf("redacted config lost %q, which is not a secret:\n%s", kept, out)
		}
	}
	if cfg.Jellyfin.URL != before.Jellyfin.URL || cfg.Infinidysk.URL != before.Infinidysk.URL {
		t.Error("Redacted mutated the original")
	}
	if bad := (&Config{Jellyfin: JellyfinConfig{URL: "http://u:p%zz@host"}}).Redacted(); strings.Contains(bad.Jellyfin.URL, "p%zz") {
		t.Errorf("an unparsable URL printed as %q; it may hold a credential", bad.Jellyfin.URL)
	}
}
