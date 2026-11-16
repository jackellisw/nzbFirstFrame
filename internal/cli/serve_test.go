package cli

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func serve(t *testing.T, args ...string) (url, dir string, stop func() int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out, w := io.Pipe()
	code := make(chan int, 1)
	go func() {
		code <- Main(ctx, append([]string{"serve", "-listen", "127.0.0.1:0"}, args...), w, io.Discard)
		w.Close()
	}()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		cancel()
		t.Fatalf("serve printed nothing (exit %d)", <-code)
	}
	go io.Copy(io.Discard, out)
	m := regexp.MustCompile(`^dashboard on (http://127\.0\.0\.1:\d+/) reading (.+)\n$`).FindStringSubmatch(line)
	if m == nil {
		cancel()
		t.Fatalf("unexpected first line %q", line)
	}
	return m[1], m[2], func() int {
		cancel()
		select {
		case c := <-code:
			return c
		case <-time.After(10 * time.Second):
			t.Fatal("serve did not shut down after cancel")
			return -1
		}
	}
}

func TestServeReadsResultsDirFromConfigAndStopsOnCancel(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("NZBFF_CONFIG", "")
	dir := filepath.Join(t.TempDir(), "results")
	cfg := filepath.Join(t.TempDir(), "nzbff.yaml")
	if err := os.WriteFile(cfg, []byte("results:\n  dir: "+dir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	url, got, stop := serve(t, "-c", cfg)
	if got != dir {
		t.Errorf("reading %q, want results.dir %q", got, dir)
	}
	for path, want := range map[string]string{"": "<title>nzbff</title>", "api/runs": "[]"} {
		res, err := http.Get(url + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK || !strings.Contains(string(body), want) {
			t.Errorf("GET /%s: %d %q, want %q", path, res.StatusCode, body, want)
		}
	}
	if code := stop(); code != exitOK {
		t.Errorf("exit %d after cancel, want %d", code, exitOK)
	}
}

func TestServeResultsFlagOverridesConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("NZBFF_CONFIG", "")
	cfg := filepath.Join(t.TempDir(), "nzbff.yaml")
	if err := os.WriteFile(cfg, []byte("results:\n  dir: /nonexistent/from-config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_, got, stop := serve(t, "-c", cfg, "-results", dir)
	defer stop()
	if got != dir {
		t.Errorf("reading %q, want -results %q", got, dir)
	}
}

func TestServeRejectsArguments(t *testing.T) {
	if code, _, _ := run(t, "serve", "stray"); code != exitUsage {
		t.Errorf("exit %d, want %d", code, exitUsage)
	}
}
