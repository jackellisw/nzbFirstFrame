package probe

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

// httpSource reads from an HTTP server that supports Range (Jellyfin's
// static stream, infinidysk). The transport is created per sample and
// closed with it.
type httpSource struct {
	url    string
	header http.Header // e.g. X-Emby-Token; never on ffprobe's command line
	user   string      // basic auth, optional
	pass   string
	client *http.Client
}

func (s *httpSource) open(ctx context.Context, off, end int64) (opened, error) {
	// GotConn fires before the request is written, so dial is complete when
	// Do returns. Redirect hops each add their own wait.
	var dial time.Duration
	var asked time.Time
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GetConn: func(string) { asked = time.Now() },
		GotConn: func(info httptrace.GotConnInfo) {
			if !info.Reused {
				dial += time.Since(asked)
			}
		},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return opened{}, err
	}
	for k, v := range s.header {
		req.Header[k] = v
	}
	if s.user != "" {
		req.SetBasicAuth(s.user, s.pass)
	}
	rng := "bytes=" + strconv.FormatInt(off, 10) + "-"
	if end >= 0 {
		rng += strconv.FormatInt(end, 10)
	}
	req.Header.Set("Range", rng)

	resp, err := s.client.Do(req)
	if err != nil {
		return opened{}, err
	}
	size, err := rangeResponseSize(resp, off)
	if err != nil {
		resp.Body.Close()
		return opened{}, err
	}
	return opened{body: resp.Body, size: size, dial: dial}, nil
}

func (s *httpSource) close() { s.client.CloseIdleConnections() }

// rangeResponseSize checks that resp answers a read from off and returns
// the full media size it reports.
func rangeResponseSize(resp *http.Response, off int64) (int64, error) {
	switch resp.StatusCode {
	case http.StatusPartialContent:
		cr := resp.Header.Get("Content-Range")
		start, size, ok := parseContentRange(cr)
		if !ok {
			return 0, fmt.Errorf("upstream sent 206 with unusable Content-Range %q", cr)
		}
		if start != off || size <= off {
			return 0, fmt.Errorf("upstream answered Content-Range %q to a read from byte %d", cr, off)
		}
		return size, nil
	case http.StatusOK:
		// A server without Range support sends the whole file; that is
		// only the requested bytes when the read starts at 0.
		if off != 0 {
			return 0, fmt.Errorf("upstream ignored Range (200 OK to a read from byte %d)", off)
		}
		if resp.ContentLength < 0 {
			return 0, fmt.Errorf("upstream sent no Content-Length")
		}
		if resp.ContentLength == 0 {
			return 0, &unsatisfiableError{size: 0}
		}
		return resp.ContentLength, nil
	case http.StatusRequestedRangeNotSatisfiable:
		size := int64(-1)
		if total, ok := strings.CutPrefix(resp.Header.Get("Content-Range"), "bytes */"); ok {
			if n, err := strconv.ParseInt(total, 10, 64); err == nil {
				size = n
			}
		}
		return 0, &unsatisfiableError{size: size}
	default:
		return 0, fmt.Errorf("upstream answered %s", resp.Status)
	}
}

// parseContentRange parses "bytes START-END/SIZE". An unknown size ("*")
// is not ok: the tap needs it for its own Content-Range.
func parseContentRange(h string) (start, size int64, ok bool) {
	spec, found := strings.CutPrefix(h, "bytes ")
	if !found {
		return 0, 0, false
	}
	span, total, found := strings.Cut(spec, "/")
	if !found {
		return 0, 0, false
	}
	first, _, found := strings.Cut(span, "-")
	if !found {
		return 0, 0, false
	}
	start, err1 := strconv.ParseInt(strings.TrimSpace(first), 10, 64)
	size, err2 := strconv.ParseInt(strings.TrimSpace(total), 10, 64)
	return start, size, err1 == nil && err2 == nil
}

// newSampleClient returns a client with its own connection pool, so a
// sample pays its own connects.
func newSampleClient(insecureSkipVerify bool) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: insecureSkipVerify}
	return &http.Client{Transport: tr, CheckRedirect: credentialsStayOnOrigin}
}

// credentialHeaders are the headers the sources authenticate with.
var credentialHeaders = []string{"X-Emby-Token", "Authorization"}

// credentialsStayOnOrigin follows redirects but drops credentials from a
// redirected request once any hop has left the configured scheme and
// host:port. net/http copies every header of the first request to each
// redirect, and only strips Authorization (and only for another domain),
// so without this the Jellyfin API key goes wherever a Location points.
// Once dropped they stay dropped: a hop back to the origin was chosen by
// the other host.
func credentialsStayOnOrigin(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	origin := via[0].URL
	elsewhere := func(r *http.Request) bool {
		return r.URL.Scheme != origin.Scheme || r.URL.Host != origin.Host
	}
	if elsewhere(req) || slices.ContainsFunc(via[1:], elsewhere) {
		for _, h := range credentialHeaders {
			req.Header.Del(h)
		}
	}
	return nil
}

// fileSource reads a file on the rclone mount, as Jellyfin does.
type fileSource struct{ path string }

func (s *fileSource) open(ctx context.Context, off, end int64) (opened, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return opened{}, err
	}
	// Stat on the open handle, so the lookup rclone answers is the one
	// Open already paid for on the timeline.
	st, err := f.Stat()
	if err == nil && st.IsDir() {
		err = fmt.Errorf("%s is a directory", s.path)
	}
	if err == nil && off >= st.Size() {
		err = &unsatisfiableError{size: st.Size()}
	}
	if err == nil {
		_, err = f.Seek(off, io.SeekStart)
	}
	if err != nil {
		f.Close()
		return opened{}, err
	}
	return opened{body: f, size: st.Size()}, nil
}

func (s *fileSource) close() {}

// jellyfinSource streams the item through Jellyfin unmodified:
// {url}/Videos/{item}/stream?static=true with the API key in X-Emby-Token.
func jellyfinSource(p *Prober, t config.Target) source {
	j := p.cfg.Jellyfin
	return &httpSource{
		url:    strings.TrimRight(j.URL, "/") + "/Videos/" + url.PathEscape(t.JellyfinItemID) + "/stream?static=true",
		header: http.Header{"X-Emby-Token": {j.APIKey}},
		client: newSampleClient(j.InsecureSkipVerify),
	}
}

// mountSource reads {mount_path}/{path}.
func mountSource(p *Prober, t config.Target) source {
	return &fileSource{path: filepath.Join(p.cfg.Rclone.MountPath, filepath.FromSlash(t.Path))}
}

// infinidyskSource reads {url}/{path} (each segment path-escaped) with
// optional basic auth.
func infinidyskSource(p *Prober, t config.Target) source {
	d := p.cfg.Infinidysk
	segments := strings.Split(t.Path, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return &httpSource{
		url:    strings.TrimRight(d.URL, "/") + "/" + strings.Join(segments, "/"),
		user:   d.Username,
		pass:   d.Password,
		client: newSampleClient(d.InsecureSkipVerify),
	}
}
