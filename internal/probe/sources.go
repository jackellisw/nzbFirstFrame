package probe

import (
	"context"
	"net/http"

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
	// TODO: GET s.url with Range bytes=off-[end]; httptrace ConnectStart/
	//   TLSHandshakeDone to fill dial; require 206 (or 200 when off==0, size
	//   from Content-Length); size from Content-Range "/total".
	panic("not implemented")
}

// fileSource reads a file on the rclone mount, as Jellyfin does.
type fileSource struct{ path string }

func (s *fileSource) open(ctx context.Context, off, end int64) (opened, error) {
	// TODO: os.Open (on the timeline: rclone resolves the path here),
	//   f.Stat for size, f.Seek(off), wrap in LimitReader when end >= 0.
	//   dial is always 0.
	panic("not implemented")
}

// jellyfinSource streams the item through Jellyfin unmodified:
// {url}/Videos/{item}/stream?static=true with the API key in X-Emby-Token.
func jellyfinSource(p *Prober, t config.Target) source {
	// TODO: &httpSource{url: ..., header: {"X-Emby-Token": p.cfg.Jellyfin.APIKey}, client: newSampleClient(p.cfg.Jellyfin.InsecureSkipVerify)}
	panic("not implemented")
}

// mountSource reads {mount_path}/{path}.
func mountSource(p *Prober, t config.Target) source {
	// TODO: &fileSource{path: filepath.Join(p.cfg.Rclone.MountPath, filepath.FromSlash(t.Path))}
	panic("not implemented")
}

// infinidyskSource reads {url}/{path} (each segment path-escaped) with
// optional basic auth.
func infinidyskSource(p *Prober, t config.Target) source {
	panic("not implemented")
}
