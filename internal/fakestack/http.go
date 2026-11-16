package fakestack

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

// jellyfinHandler imitates the one Jellyfin route the profiler uses.
func (s *Stack) jellyfinHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /Videos/{id}/stream", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Header.Get("X-Emby-Token") != s.APIKey:
			http.Error(w, "missing or wrong X-Emby-Token", http.StatusUnauthorized)
		case r.PathValue("id") != s.Target.JellyfinItemID:
			http.Error(w, "no such item", http.StatusNotFound)
		case r.URL.Query().Get("static") != "true":
			http.Error(w, "fakestack does not transcode; ask for static=true", http.StatusBadRequest)
		default:
			s.serveMedia(w, r, config.LayerJellyfin, s.opts.JellyfinTTFB,
				filepath.Join(s.MountDir, filepath.FromSlash(s.Target.Path)))
		}
	})
	return mux
}

// infinidyskHandler serves MountDir over HTTP behind basic auth.
func (s *Stack) infinidyskHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		// filepath.Localize rejects a path that would leave MountDir.
		rel, err := filepath.Localize(strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.serveMedia(w, r, config.LayerInfinidysk, s.opts.InfinidyskTTFB, filepath.Join(s.MountDir, rel))
	})
}

func (s *Stack) authorized(w http.ResponseWriter, r *http.Request) bool {
	if user, pass, ok := r.BasicAuth(); ok && user == s.User && pass == s.Pass {
		return true
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="fakestack"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

// serveMedia answers one read of a file after ttfb, leaving Range handling
// to net/http.
func (s *Stack) serveMedia(w http.ResponseWriter, r *http.Request, layer config.Layer, ttfb time.Duration, file string) {
	f, err := os.Open(file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}

	out := &countingWriter{ResponseWriter: w}
	sent := s.hit(layer, r.Header.Get("Range"))
	defer func() { sent(out.bytes) }()
	if !wait(r.Context(), ttfb) {
		return
	}
	http.ServeContent(out, r, filepath.Base(file), time.Time{}, f)
}

type countingWriter struct {
	http.ResponseWriter
	bytes int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	c.bytes += int64(n)
	return n, err
}

// rcHandler imitates rclone's remote control for the one call cold-cache
// runs make.
func (s *Stack) rcHandler() http.Handler {
	type failure struct {
		Error  string `json:"error"`
		Path   string `json:"path"`
		Status int    `json:"status"`
	}
	reply := func(w http.ResponseWriter, status int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(body)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(w, r) {
			return
		}
		method := strings.TrimPrefix(r.URL.Path, "/")
		if r.Method != http.MethodPost || method != "vfs/forget" {
			reply(w, http.StatusNotFound, failure{Error: `couldn't find method "` + method + `"`, Path: method, Status: http.StatusNotFound})
			return
		}
		var params struct {
			File string `json:"file"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if len(body) > 0 && json.Unmarshal(body, &params) != nil {
			reply(w, http.StatusBadRequest, failure{Error: "failed to read input JSON", Path: method, Status: http.StatusBadRequest})
			return
		}
		s.hit(config.LayerRclone, "vfs/forget file="+params.File)
		forgotten := []string{}
		if params.File != "" {
			forgotten = append(forgotten, params.File)
		}
		reply(w, http.StatusOK, map[string][]string{"forgotten": forgotten})
	})
}
