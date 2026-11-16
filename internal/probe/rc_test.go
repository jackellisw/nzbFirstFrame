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
