package probe_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/jackellisw/nzbfirstframe/internal/config"
	"github.com/jackellisw/nzbfirstframe/internal/results"
)

const (
	echoedUser = "login-name"
	echoedPass = "pass-w0rd"
)

var filler = strings.Repeat("A", 2000)

// stored is what the run file keeps of a sample: its error and timeline.
func stored(t *testing.T, tl results.Timeline, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("sample succeeded; want the server's refusal")
	}
	raw, jerr := json.Marshal(results.Sample{Timeline: tl, Err: err.Error()})
	if jerr != nil {
		t.Fatal(jerr)
	}
	return string(raw)
}

func checkScrubbed(t *testing.T, text string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if i := strings.Index(text, s); i >= 0 {
			t.Errorf("stored text holds the configured credential %q: …%s…", s, text[max(i-80, 0):min(i+40, len(text))])
		}
	}
	for _, field := range strings.Split(text, `"err":`)[1:] {
		if n := strings.Count(field, "A"); n > 200 {
			t.Errorf("an error keeps %d bytes of the server's filler; want a short prefix: %.200s…", n, field)
		}
	}
}

func TestNNTPServerTextIsStoredScrubbedAndShort(t *testing.T) {
	ln := listen(t)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				fmt.Fprint(conn, "200 ready\r\n")
				in := bufio.NewScanner(conn)
				for in.Scan() {
					switch line := in.Text(); {
					case strings.HasPrefix(line, "AUTHINFO USER"):
						fmt.Fprint(conn, "381 password required\r\n")
					case strings.HasPrefix(line, "AUTHINFO PASS"):
						fmt.Fprintf(conn, "481 no account %s with password %s %s\r\n", echoedUser, echoedPass, filler[:400])
						return
					}
				}
			}()
		}
	}()
	cfg, target := usenetConfig(t, ln, mixedNZB)
	cfg.Usenet.Username, cfg.Usenet.Password = echoedUser, echoedPass

	tl, err := newProber(cfg).Sample(t.Context(), config.LayerUsenet, target, planAt())
	text := stored(t, tl, err)
	checkScrubbed(t, text, echoedUser, echoedPass)
	if !strings.Contains(err.Error(), "481 no account") {
		t.Errorf("err = %v, want the start of the provider's reply kept", err)
	}
}

func TestNNTPGreetingIsStoredAsAShortPrefix(t *testing.T) {
	ln := listen(t)
	(&news{greeting: "400 " + filler[:500]}).serve(t, ln)
	cfg, target := usenetConfig(t, ln, mixedNZB)

	tl, err := newProber(cfg).Sample(t.Context(), config.LayerUsenet, target, planAt())
	checkScrubbed(t, stored(t, tl, err))
	if !strings.Contains(err.Error(), "400 AAAA") {
		t.Errorf("err = %v, want the start of the greeting kept", err)
	}
}

func TestRCServerTextIsStoredScrubbedAndShort(t *testing.T) {
	rc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":"bad login %s/%s %s","status":401}`, echoedUser, echoedPass, filler)
	}))
	defer rc.Close()
	cfg := config.Default()
	cfg.Rclone = config.RcloneConfig{MountPath: t.TempDir(), RCURL: rc.URL, RCUser: echoedUser, RCPassword: echoedPass}
	cfg.Profile.ColdCache = true

	tl, err := newProber(cfg).Sample(t.Context(), config.LayerRclone, config.Target{Name: "x", Path: "x.mkv"}, planAt())
	checkScrubbed(t, stored(t, tl, err), echoedUser, echoedPass)
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "bad login") {
		t.Errorf("err = %v, want rclone's status and the start of its error", err)
	}
}

// rawHTTP answers every request with reply verbatim, so the status line can
// carry what net/http's ResponseWriter would not send.
func rawHTTP(t *testing.T, reply string) string {
	t.Helper()
	ln := listen(t)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if _, err := http.ReadRequest(bufio.NewReader(conn)); err == nil {
					fmt.Fprint(conn, reply)
				}
			}()
		}
	}()
	return "http://" + ln.Addr().String()
}

func TestHTTPServerTextIsStoredScrubbedAndShort(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH")
	}
	echo := echoedUser + " " + echoedPass + " " + filler
	for name, reply := range map[string]string{
		"reason phrase":     "HTTP/1.1 503 Down for " + echo + "\r\nContent-Length: 0\r\n\r\n",
		"Content-Range":     "HTTP/1.1 206 Partial Content\r\nContent-Range: bytes 0-1/" + echo + "\r\nContent-Length: 2\r\n\r\nab",
		"malformed answer":  "NOT-HTTP-" + strings.ReplaceAll(echo, " ", "-") + "\r\n\r\n",
		"api key in reason": "HTTP/1.1 401 Bad key " + echo + "\r\nContent-Length: 0\r\n\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			url := rawHTTP(t, reply)
			for _, layer := range []config.Layer{config.LayerJellyfin, config.LayerInfinidysk} {
				cfg := config.Default()
				cfg.Jellyfin = config.JellyfinConfig{URL: url, APIKey: echoedPass}
				cfg.Infinidysk = config.InfinidyskConfig{URL: url, Username: echoedUser, Password: echoedPass}
				target := config.Target{Name: "x", Path: "x.mkv", JellyfinItemID: "item"}

				tl, err := newProber(cfg).Sample(t.Context(), layer, target, planAt())
				text := stored(t, tl, err)
				checkScrubbed(t, text, echoedUser, echoedPass)
				if !strings.Contains(text, "upstream") {
					t.Errorf("%s: stored %s, want the upstream failure kept", layer, text)
				}
			}
		})
	}
}
