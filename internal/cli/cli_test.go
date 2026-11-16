package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testConfig = `
jellyfin:
  url: http://jf.test:8096
  api_key: key
rclone:
  mount_path: /mnt/media
profile:
  iterations: 3
  seek_points: ["50%"]
targets:
  - name: a
    jellyfin_item_id: aaa
    path: movies/a.mkv
  - name: b
    jellyfin_item_id: bbb
    path: movies/b.mkv
`

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = Main(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func writeConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir) // keep the search path away from any real ./nzbff.yaml
	t.Setenv("NZBFF_CONFIG", "")
	p := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(p, []byte(testConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDryRunUsesConfig(t *testing.T) {
	cfg := writeConfig(t)
	code, out, errOut := run(t, "run", "-c", cfg, "-dry-run")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"mode:        all", "layers:      jellyfin, rclone", "iterations:  3", "seek points: 50%", "movies/a.mkv", "movies/b.mkv"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestFlagsOverrideConfig(t *testing.T) {
	cfg := writeConfig(t)
	code, out, errOut := run(t, "seek", "-config", cfg, "-dry-run",
		"-iterations", "7", "-layers", "rclone", "-seek", "10%,00:01:00", "-target", "b", "-cold-cache", "-rclone-rc-url", "http://127.0.0.1:5572")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"layers:      rclone", "iterations:  7", "cold cache true", "seek points: 10%, 00:01:00", "movies/b.mkv"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "movies/a.mkv") {
		t.Errorf("-target b should exclude a:\n%s", out)
	}
}

func TestAdhocTargetNeedsNoConfigTargets(t *testing.T) {
	writeConfig(t)
	code, out, errOut := run(t, "startup", "-dry-run", "-rclone-mount", "/mnt/x", "-path", "tv/ep1.mkv")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "adhoc") || !strings.Contains(out, "tv/ep1.mkv") || strings.Contains(out, "seek points") {
		t.Errorf("unexpected plan:\n%s", out)
	}
}

func TestInvalidConfigFails(t *testing.T) {
	cfg := writeConfig(t)
	code, _, errOut := run(t, "run", "-c", cfg, "-iterations", "0", "-cold-cache")
	if code != exitFail {
		t.Fatalf("exit %d, want %d", code, exitFail)
	}
	if !strings.Contains(errOut, "iterations") || !strings.Contains(errOut, "cold_cache") {
		t.Errorf("want both problems reported:\n%s", errOut)
	}
}

func TestBadFlagIsUsageError(t *testing.T) {
	writeConfig(t)
	for _, args := range [][]string{
		{"run", "-iterations", "lots"},
		{"run", "-layers", "plex"},
		{"seek", "-seek", "200%"},
		{"startup", "-seek", "50%"}, // seek flag not offered on startup
		{"run", "stray-arg"},
		{"bogus"},
	} {
		if code, _, _ := run(t, args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
}

func TestRunNotImplementedYet(t *testing.T) {
	cfg := writeConfig(t)
	code, _, errOut := run(t, "run", "-c", cfg)
	if code != exitFail || !strings.Contains(errOut, "not implemented") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
}

func TestConfigSubcommands(t *testing.T) {
	cfg := writeConfig(t)

	code, out, errOut := run(t, "config", "validate", "-c", cfg)
	if code != 0 || !strings.Contains(out, "ok:") {
		t.Errorf("validate: exit %d out %q err %q", code, out, errOut)
	}

	code, out, _ = run(t, "config", "show", "-c", cfg)
	if code != 0 || strings.Contains(out, "api_key: key") || !strings.Contains(out, "********") {
		t.Errorf("show should redact secrets:\n%s", out)
	}
	if !strings.Contains(out, "timeout: 1m0s") {
		t.Errorf("show should render durations readably:\n%s", out)
	}

	code, out, _ = run(t, "config", "init")
	if code != 0 || !strings.Contains(out, "wrote nzbff.yaml") {
		t.Errorf("init: exit %d out %q", code, out)
	}
	if code, _, errOut = run(t, "config", "init"); code != exitFail || !strings.Contains(errOut, "already exists") {
		t.Errorf("init twice should refuse: exit %d %q", code, errOut)
	}

	// The freshly written ./nzbff.yaml is now picked up from the search path.
	t.Setenv("NZBFF_JELLYFIN_API_KEY", "k")
	code, out, errOut = run(t, "config", "validate")
	if code != 0 || !strings.Contains(out, "nzbff.yaml") {
		t.Errorf("validate init'd config: exit %d out %q err %q", code, out, errOut)
	}
}
