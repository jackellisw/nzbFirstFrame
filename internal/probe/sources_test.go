package probe

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

// rangeServer serves data with net/http's own Range implementation and
// keeps the last request it saw.
func rangeServer(t *testing.T, data string) (*httptest.Server, *http.Request) {
	t.Helper()
	last := new(http.Request)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*last = *r.Clone(r.Context())
		http.ServeContent(w, r, "", time.Time{}, strings.NewReader(data))
	}))
	t.Cleanup(srv.Close)
	return srv, last
}

func readAll(t *testing.T, o opened) string {
	t.Helper()
	defer o.body.Close()
	b, err := io.ReadAll(o.body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestJellyfinSourceStreamsStaticWithKeyInHeader(t *testing.T) {
	srv, last := rangeServer(t, "0123456789")
	cfg := &config.Config{Jellyfin: config.JellyfinConfig{URL: srv.URL + "/", APIKey: "s3cret"}}
	src := jellyfinSource(New(cfg, nil), config.Target{JellyfinItemID: "abc123"})
	defer src.close()

	o, err := src.open(t.Context(), 4, -1)
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, o); got != "456789" || o.size != 10 {
		t.Errorf("read %q of size %d, want %q of 10", got, o.size, "456789")
	}
	if last.URL.Path != "/Videos/abc123/stream" || last.URL.RawQuery != "static=true" {
		t.Errorf("requested %s?%s, want /Videos/abc123/stream?static=true", last.URL.Path, last.URL.RawQuery)
	}
	if last.Header.Get("X-Emby-Token") != "s3cret" {
		t.Errorf("X-Emby-Token = %q, want the API key", last.Header.Get("X-Emby-Token"))
	}
	if got := last.Header.Get("Range"); got != "bytes=4-" {
		t.Errorf("Range = %q, want bytes=4-", got)
	}
}

func TestInfinidyskSourceEscapesPathAndSendsBasicAuth(t *testing.T) {
	srv, last := rangeServer(t, "0123456789")
	cfg := &config.Config{Infinidysk: config.InfinidyskConfig{URL: srv.URL, Username: "u", Password: "p w"}}
	const path = "movies/A Film (2024) #1/50% off?.mkv"
	src := infinidyskSource(New(cfg, nil), config.Target{Path: path})
	defer src.close()

	o, err := src.open(t.Context(), 2, 5)
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, o); got != "2345" || o.size != 10 {
		t.Errorf("read %q of size %d, want %q of 10", got, o.size, "2345")
	}
	if last.URL.Path != "/"+path || last.URL.RawQuery != "" {
		t.Errorf("server saw path %q query %q, want /%s and no query", last.URL.Path, last.URL.RawQuery, path)
	}
	if user, pass, ok := last.BasicAuth(); !ok || user != "u" || pass != "p w" {
		t.Errorf("basic auth %q/%q (present %v), want u/p w", user, pass, ok)
	}

	cfg.Infinidysk.Username = ""
	anon := infinidyskSource(New(cfg, nil), config.Target{Path: path})
	defer anon.close()
	if o, err := anon.open(t.Context(), 0, -1); err != nil {
		t.Fatal(err)
	} else {
		o.body.Close()
	}
	if _, _, ok := last.BasicAuth(); ok {
		t.Error("sent basic auth without a configured username")
	}
}

func TestCredentialsDoNotFollowARedirectToAnotherHost(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]http.Header{} // host+path → headers of the last request
	var redirect string              // where the origin sends a source's first request
	serve := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Host+r.URL.Path] = r.Header.Clone()
		to := redirect
		mu.Unlock()
		switch r.URL.Path {
		case "/Videos/item/stream", "/movie.mkv":
			http.Redirect(w, r, to, http.StatusFound)
		default:
			http.ServeContent(w, r, "", time.Time{}, strings.NewReader("0123456789"))
		}
	}
	origin := httptest.NewServer(http.HandlerFunc(serve))
	t.Cleanup(origin.Close)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/back" {
			mu.Lock()
			seen[r.Host+r.URL.Path] = r.Header.Clone()
			mu.Unlock()
			http.Redirect(w, r, origin.URL+"/returned", http.StatusFound)
			return
		}
		serve(w, r)
	}))
	t.Cleanup(other.Close)

	cfg := &config.Config{
		Jellyfin:   config.JellyfinConfig{URL: origin.URL, APIKey: "s3cret"},
		Infinidysk: config.InfinidyskConfig{URL: origin.URL, Username: "u", Password: "p"},
	}
	sources := []struct {
		name       string
		src        func() source
		first      string
		credential string
	}{
		{"jellyfin", func() source { return jellyfinSource(New(cfg, nil), config.Target{JellyfinItemID: "item"}) }, "/Videos/item/stream", "X-Emby-Token"},
		{"infinidysk", func() source { return infinidyskSource(New(cfg, nil), config.Target{Path: "movie.mkv"}) }, "/movie.mkv", "Authorization"},
	}
	for name, tc := range map[string]struct {
		redirect, final string // URL the origin redirects to, URL that serves the media
		keep            bool
	}{
		"to another host":                {other.URL + "/media", other.URL + "/media", false},
		"within the origin":              {origin.URL + "/moved", origin.URL + "/moved", true},
		"to another host and back again": {other.URL + "/back", origin.URL + "/returned", false},
	} {
		for _, s := range sources {
			t.Run(name+"/"+s.name, func(t *testing.T) {
				mu.Lock()
				clear(seen)
				redirect = tc.redirect
				mu.Unlock()
				src := s.src()
				defer src.close()
				o, err := src.open(t.Context(), 4, -1)
				if err != nil {
					t.Fatal(err)
				}
				if got := readAll(t, o); got != "456789" {
					t.Errorf("read %q through the redirect, want 456789", got)
				}

				mu.Lock()
				defer mu.Unlock()
				first := seen[strings.TrimPrefix(origin.URL, "http://")+s.first]
				if first == nil || first.Get(s.credential) == "" {
					t.Fatalf("the configured URL got no %s", s.credential)
				}
				final := seen[strings.TrimPrefix(tc.final, "http://")]
				if final == nil {
					t.Fatalf("%s was never requested; saw %v", tc.final, seen)
				}
				if got := final.Get(s.credential); (got != "") != tc.keep {
					t.Errorf("%s reached %s with %s %q; want it sent: %v", s.name, tc.final, s.credential, got, tc.keep)
				}
			})
		}
	}
}

func TestHTTPSourceReportsDialOnlyForNewConnections(t *testing.T) {
	srv, _ := rangeServer(t, "0123456789")
	src := &httpSource{url: srv.URL, client: newSampleClient(false)}
	defer src.close()

	first, err := src.open(t.Context(), 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, first) // drained, so the connection returns to the pool
	second, err := src.open(t.Context(), 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, second)
	if first.dial <= 0 || second.dial != 0 {
		t.Errorf("dial = %v then %v, want a connect then a reused connection", first.dial, second.dial)
	}
}

func TestHTTPSourceValidatesTheUpstreamAnswer(t *testing.T) {
	answer := func(status int, header map[string]string, body string) *httpSource {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for k, v := range header {
				w.Header().Set(k, v)
			}
			w.WriteHeader(status)
			io.WriteString(w, body)
		}))
		t.Cleanup(srv.Close)
		return &httpSource{url: srv.URL, client: newSampleClient(false)}
	}

	t.Run("200 without Range support serves a read from 0", func(t *testing.T) {
		o, err := answer(200, nil, "0123456789").open(t.Context(), 0, -1)
		if err != nil {
			t.Fatal(err)
		}
		if got := readAll(t, o); got != "0123456789" || o.size != 10 {
			t.Errorf("read %q of size %d", got, o.size)
		}
	})
	for name, tc := range map[string]struct {
		status int
		header map[string]string
		off    int64
	}{
		"200 to a read from an offset":        {200, nil, 5},
		"206 for a different offset":          {206, map[string]string{"Content-Range": "bytes 0-9/10"}, 5},
		"206 without Content-Range":           {206, nil, 5},
		"206 with unknown size":               {206, map[string]string{"Content-Range": "bytes 5-9/*"}, 5},
		"401":                                 {401, nil, 0},
		"404":                                 {404, nil, 0},
		"500":                                 {500, nil, 0},
		"416 is not an upstream failure kind": {416, map[string]string{"Content-Range": "bytes */10"}, 10},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := answer(tc.status, tc.header, "0123456789").open(t.Context(), tc.off, -1)
			if err == nil {
				t.Fatal("open accepted the answer")
			}
			var un *unsatisfiableError
			if got := errors.As(err, &un); got != (tc.status == 416) {
				t.Errorf("unsatisfiable = %v for status %d: %v", got, tc.status, err)
			}
			if tc.status == 416 && un.size != 10 {
				t.Errorf("unsatisfiable size %d, want the 10 from Content-Range", un.size)
			}
		})
	}
}

func TestMountSourceReadsTheFileFromAnOffset(t *testing.T) {
	mount := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mount, "movies", "A Film"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mount, "movies", "A Film", "a.mkv"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := New(&config.Config{Rclone: config.RcloneConfig{MountPath: mount}}, nil)
	src := mountSource(p, config.Target{Path: "movies/A Film/a.mkv"})

	o, err := src.open(t.Context(), 7, -1)
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, o); got != "789" || o.size != 10 || o.dial != 0 {
		t.Errorf("read %q, size %d, dial %v; want 789, 10, 0", got, o.size, o.dial)
	}

	var un *unsatisfiableError
	if _, err := src.open(t.Context(), 10, -1); !errors.As(err, &un) || un.size != 10 {
		t.Errorf("read at EOF: %v, want unsatisfiable with size 10", err)
	}
	if _, err := mountSource(p, config.Target{Path: "movies/A Film"}).open(t.Context(), 0, -1); err == nil {
		t.Error("opened a directory as media")
	}
	if _, err := mountSource(p, config.Target{Path: "movies/none.mkv"}).open(t.Context(), 0, -1); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: %v, want not-exist", err)
	}
}
