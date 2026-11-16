package probe

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

func TestForgetPostsTheTargetToVFSForget(t *testing.T) {
	var path, body, user, pass string
	status, reply := http.StatusOK, `{"forgotten":["movies/a b.mkv"]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		path, body = r.Method+" "+r.URL.Path, string(raw)
		user, pass, _ = r.BasicAuth()
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	defer srv.Close()
	p := New(&config.Config{Rclone: config.RcloneConfig{RCURL: srv.URL + "/", RCUser: "rc", RCPassword: "secret"}}, nil)

	if err := p.forget(t.Context(), "movies/a b.mkv"); err != nil {
		t.Fatal(err)
	}
	if path != "POST /vfs/forget" || body != `{"file":"movies/a b.mkv"}` || user != "rc" || pass != "secret" {
		t.Errorf("rc saw %s with body %s as %s/%s", path, body, user, pass)
	}

	if err := p.forget(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if body != `{}` {
		t.Errorf("target without a path: body %s, want {} (forget everything)", body)
	}

	status, reply = http.StatusInternalServerError, `{"error":"file not in vfs","status":500}`
	err := p.forget(t.Context(), "movies/a b.mkv")
	if err == nil || !strings.Contains(err.Error(), "file not in vfs") || !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v, want rclone's status and error text", err)
	}
	status, reply = http.StatusUnauthorized, `not json`
	if err := p.forget(t.Context(), "x"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v, want the 401", err)
	}
}

func TestForgetKeepsCredentialsOnTheRCOriginAndFailsWhenRedirectedAway(t *testing.T) {
	type seen struct{ method, path, body, user, pass string }
	var rc, other []seen
	record := func(log *[]seen) func(*http.Request) {
		return func(r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			user, pass, _ := r.BasicAuth()
			*log = append(*log, seen{r.Method, r.URL.Path, string(raw), user, pass})
		}
	}
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record(&other)(r)
		io.WriteString(w, `{}`)
	}))
	defer elsewhere.Close()
	var redirect func(w http.ResponseWriter, r *http.Request)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record(&rc)(r)
		if r.URL.Path == "/vfs/forget" {
			redirect(w, r)
			return
		}
		io.WriteString(w, `{}`)
	}))
	defer origin.Close()
	p := New(&config.Config{Rclone: config.RcloneConfig{RCURL: origin.URL, RCUser: "rc", RCPassword: "rc-secret"}}, nil)

	for _, tc := range []struct {
		name     string
		location string
		code     int
		wantErr  bool
	}{
		{"307 to another port", elsewhere.URL + "/vfs/forget", http.StatusTemporaryRedirect, true},
		{"302 to another port", elsewhere.URL + "/vfs/forget", http.StatusFound, true},
		{"302 on the rc origin turns the POST into a GET", origin.URL + "/moved", http.StatusFound, true},
		{"307 on the rc origin", origin.URL + "/moved", http.StatusTemporaryRedirect, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc, other = nil, nil
			redirect = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, tc.location, tc.code) }
			err := p.forget(t.Context(), "movies/a.mkv")
			if (err != nil) != tc.wantErr {
				t.Errorf("forget err = %v, want error: %v (rc saw %+v, other origin saw %+v)", err, tc.wantErr, rc, other)
			}
			for _, s := range other {
				if s.user != "" || s.pass != "" {
					t.Errorf("another origin received the rc login %q/%q", s.user, s.pass)
				}
			}
			if !tc.wantErr {
				want := seen{"POST", "/moved", `{"file":"movies/a.mkv"}`, "rc", "rc-secret"}
				if len(rc) != 2 || rc[1] != want {
					t.Errorf("rc saw %+v, want the POST re-sent with its login to %+v", rc, want)
				}
			}
		})
	}
}
