// Package fakestack runs an in-process imitation of the whole stack with
// injected latency, for end-to-end tests of the real engine and for
// `nzbff dev-stack`. It shares no code with package probe: it is an
// independent implementation of the same protocols, so a protocol mistake
// in one shows up as a test failure instead of agreeing with itself.
//
//	jellyfin    GET /Videos/{ItemID}/stream?static=true, requires X-Emby-Token,
//	            serves the file from MountDir after JellyfinTTFB
//	rclone      MountDir: a plain directory holding the media at Target.Path
//	            (no latency can be injected into local file reads)
//	infinidysk  GET /{path} with Range and basic auth, after InfinidyskTTFB
//	usenet      NNTP on loopback: greeting, AUTHINFO, BODY <id> after
//	            ArticleTTFB; the NZB splits the media into SegmentSize articles
//
// Every fake server logs its requests (Hits) so tests can compare what the
// engine reported against what the server saw.
package fakestack

import (
	"context"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

// Options shapes the fake stack. Zero values are usable: no latency,
// generated media, 750 KB segments.
type Options struct {
	// Media is an existing video file. Empty → Generate one into Dir.
	Media string
	// Dir is the working directory (mount dir, NZB, generated media).
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
	User, Pass    string // infinidysk basic auth and NNTP AUTHINFO
	MountDir      string
	NNTPAddr      string // host:port, plaintext
	Duration      time.Duration
}

// Start generates media if needed, writes the NZB, and starts the servers on
// 127.0.0.1:0. The returned Stack is ready to measure.
func Start(ctx context.Context, opts Options) (*Stack, error) { panic("not implemented") }

// Config returns a finalized config measuring every layer of this stack with
// one target, results stored under Dir. Tests tweak Profile on the copy.
func (s *Stack) Config() *config.Config { panic("not implemented") }

// Hit is one request a fake server answered.
type Hit struct {
	Layer config.Layer
	At    time.Time
	Ref   string // Range header or message-id
	Bytes int64
}

// Hits returns everything served so far, in arrival order. Safe for
// concurrent use.
func (s *Stack) Hits() []Hit { panic("not implemented") }

// Close stops every server and removes a temp Dir.
func (s *Stack) Close() error { panic("not implemented") }

// MediaSpec describes generated test media.
type MediaSpec struct {
	Container string        // "mkv" (cues at end) or "mp4" (moov at end, no faststart)
	Length    time.Duration // default 3m
	GOP       int           // default 48 (libx264 -g)
}

// Generate encodes testsrc2 at 720p with libx264 into dir and returns the
// path. Tests skip when ffmpeg or ffprobe is not on PATH.
func Generate(ctx context.Context, ffmpeg, dir string, spec MediaSpec) (string, error) {
	panic("not implemented")
}
