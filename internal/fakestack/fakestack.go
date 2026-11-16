// Package fakestack runs an in-process imitation of the whole stack with
// injected latency, for end-to-end tests of the real engine and for
// `nzbff dev-stack`. It shares no code with package probe: it is an
// independent implementation of the same protocols, so a protocol mistake
// in one shows up as a test failure instead of agreeing with itself.
//
//	jellyfin    GET /Videos/{ItemID}/stream?static=true, requires X-Emby-Token,
//	            serves the file from MountDir after JellyfinTTFB
//	rclone      MountDir: a plain directory holding the media at Target.Path
//	            (no latency can be injected into local file reads), plus an
//	            rc endpoint that answers POST /vfs/forget
//	infinidysk  GET /{path} with Range and basic auth, after InfinidyskTTFB
//	usenet      NNTP on loopback: greeting, AUTHINFO, BODY <id>; the NZB
//	            splits the media into SegmentSize yEnc articles. The 222
//	            status line is sent at once and the body after ArticleTTFB,
//	            so a client that times the status line instead of the first
//	            body byte reports no latency.
//
// Every fake server logs its requests (Hits) so tests can compare what the
// engine reported against what the server saw.
package fakestack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

// Options shapes the fake stack. Zero values are usable: no latency,
// generated media, 750 KB segments.
type Options struct {
	// Media is an existing video file. Empty → Generate one from Spec.
	Media string
	// Spec describes the media to generate when Media is empty.
	Spec MediaSpec
	// Dir is the working directory (mount dir, NZB, results).
	// Empty → a new temp dir removed by Close.
	Dir string

	JellyfinTTFB   time.Duration
	InfinidyskTTFB time.Duration
	ArticleTTFB    time.Duration
	SegmentSize    int64

	FFmpeg string // for Generate; default "ffmpeg"
}

// Stack is a running fake stack.
type Stack struct {
	Target config.Target // Name "fake", Path, JellyfinItemID, NZB all set

	JellyfinURL   string
	APIKey        string
	InfinidyskURL string
	User, Pass    string // infinidysk basic auth, rclone rc and NNTP AUTHINFO
	MountDir      string
	RCURL         string // rclone rc; answers vfs/forget only
	NNTPAddr      string // host:port, plaintext
	// Duration is the generated media's runtime; 0 for Options.Media.
	Duration time.Duration

	opts    Options
	dir     string
	tempDir bool
	media   *os.File
	usenet  *newsServer
	servers []*http.Server

	mu   sync.Mutex
	hits []Hit
}

const (
	fakeItemID = "fa4e0000000000000000000000000001"
	fakeAPIKey = "fakestack-api-key"
	fakeUser   = "fake"
	fakePass   = "fake-password"
	// The directory needs escaping in a URL, so the infinidysk layer's
	// path handling is exercised by every run.
	fakeMovieDir = "movies/Fake Movie (2024)"
)

// Start generates media if needed, writes the NZB, and starts the servers on
// 127.0.0.1:0. The returned Stack is ready to measure.
func Start(ctx context.Context, opts Options) (_ *Stack, err error) {
	if opts.SegmentSize <= 0 {
		opts.SegmentSize = 750_000
	}
	if opts.FFmpeg == "" {
		opts.FFmpeg = "ffmpeg"
	}
	s := &Stack{APIKey: fakeAPIKey, User: fakeUser, Pass: fakePass, opts: opts, dir: opts.Dir}
	defer func() {
		if err != nil {
			s.Close()
		}
	}()

	if s.dir == "" {
		if s.dir, err = os.MkdirTemp("", "nzbff-fakestack-"); err != nil {
			return nil, err
		}
		s.tempDir = true
	}
	s.MountDir = filepath.Join(s.dir, "mount")
	movieDir := filepath.Join(s.MountDir, filepath.FromSlash(fakeMovieDir))
	if err := os.MkdirAll(movieDir, 0o755); err != nil {
		return nil, err
	}

	var mediaPath string
	if opts.Media != "" {
		mediaPath = filepath.Join(movieDir, filepath.Base(opts.Media))
		err = place(opts.Media, mediaPath)
	} else {
		mediaPath, err = Generate(ctx, opts.FFmpeg, movieDir, opts.Spec)
		s.Duration = opts.Spec.withDefaults().Length
	}
	if err != nil {
		return nil, err
	}
	if s.media, err = os.Open(mediaPath); err != nil {
		return nil, err
	}
	st, err := s.media.Stat()
	if err != nil {
		return nil, err
	}

	s.Target = config.Target{
		Name:           "fake",
		JellyfinItemID: fakeItemID,
		Path:           path.Join(fakeMovieDir, filepath.Base(mediaPath)),
		NZB:            filepath.Join(s.dir, "fake.nzb"),
	}
	s.usenet = newNewsServer(s, filepath.Base(mediaPath), st.Size())
	if err := os.WriteFile(s.Target.NZB, s.usenet.nzb(), 0o644); err != nil {
		return nil, err
	}

	if s.JellyfinURL, err = s.serveHTTP(s.jellyfinHandler()); err != nil {
		return nil, err
	}
	if s.InfinidyskURL, err = s.serveHTTP(s.infinidyskHandler()); err != nil {
		return nil, err
	}
	if s.RCURL, err = s.serveHTTP(s.rcHandler()); err != nil {
		return nil, err
	}
	if s.NNTPAddr, err = s.usenet.listen(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Stack) serveHTTP(h http.Handler) (url string, err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	srv := &http.Server{Handler: h}
	s.servers = append(s.servers, srv)
	go srv.Serve(ln)
	return "http://" + ln.Addr().String(), nil
}

// Config returns a finalized config measuring every layer of this stack with
// one target, results stored under Dir. Tests tweak Profile on the copy.
// There is no cooldown: nothing in the fake stack needs to settle.
func (s *Stack) Config() *config.Config {
	host, portStr, _ := net.SplitHostPort(s.NNTPAddr)
	port, _ := strconv.Atoi(portStr)

	cfg := config.Default()
	cfg.Jellyfin = config.JellyfinConfig{URL: s.JellyfinURL, APIKey: s.APIKey}
	cfg.Rclone = config.RcloneConfig{MountPath: s.MountDir, RCURL: s.RCURL, RCUser: s.User, RCPassword: s.Pass}
	cfg.Infinidysk = config.InfinidyskConfig{URL: s.InfinidyskURL, Username: s.User, Password: s.Pass}
	cfg.Usenet = config.UsenetConfig{Host: host, Port: port, Username: s.User, Password: s.Pass}
	cfg.Profile.Cooldown = 0
	cfg.Tools.FFmpeg = s.opts.FFmpeg
	cfg.Results.Dir = filepath.Join(s.dir, "results")
	cfg.Targets = []config.Target{s.Target}
	if err := cfg.Finalize(); err != nil {
		panic(fmt.Sprintf("fakestack: built an invalid config: %v", err))
	}
	return cfg
}

// Hit is one request a fake server answered.
type Hit struct {
	Layer config.Layer
	At    time.Time
	Ref   string // Range header, message-id, or "vfs/forget file=<path>"
	Bytes int64  // payload sent; 0 while the response is still in flight
}

// Hits returns everything served so far, in arrival order. Safe for
// concurrent use.
func (s *Stack) Hits() []Hit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.hits)
}

// hit logs a request on arrival and returns how to report what it sent.
func (s *Stack) hit(layer config.Layer, ref string) (sent func(bytes int64)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := len(s.hits)
	s.hits = append(s.hits, Hit{Layer: layer, At: time.Now(), Ref: ref})
	return func(bytes int64) {
		s.mu.Lock()
		s.hits[i].Bytes = bytes
		s.mu.Unlock()
	}
}

// Close stops every server and removes a temp Dir.
func (s *Stack) Close() error {
	var errs []error
	for _, srv := range s.servers {
		errs = append(errs, srv.Close())
	}
	if s.usenet != nil {
		s.usenet.close()
	}
	if s.media != nil {
		errs = append(errs, s.media.Close())
	}
	if s.tempDir {
		errs = append(errs, os.RemoveAll(s.dir))
	}
	return errors.Join(errs...)
}

// place puts the file src at dst, replacing what is there: a hard link when
// the filesystem allows it, else a copy.
func place(src, dst string) error {
	from, err := os.Stat(src)
	if err != nil {
		return err
	}
	if to, err := os.Stat(dst); err == nil && os.SameFile(from, to) {
		return nil
	}
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if os.Link(src, dst) == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// wait injects latency. It reports false when the client gave up first.
func wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
