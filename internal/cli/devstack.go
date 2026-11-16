package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jackellisw/nzbfirstframe/internal/fakestack"
)

// runDevStack implements
// `nzbff dev-stack [-dir d] [-ttfb 200ms] [-article-ttfb 80ms] [-container mkv] [-o file]`:
// fakestack.Start, print a YAML config pointing at it (secrets included; it
// is all loopback), then block until ctx is cancelled. Lets a developer run
// `nzbff run -c fake.yaml` and `nzbff serve` against known latency.
// Not listed in usage.
func runDevStack(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("dev-stack", "dev-stack [flags]", stderr)
	dir := fs.String("dir", "", "working `directory` for the mount, NZB and results (default: a temp dir removed on exit)")
	ttfb := fs.Duration("ttfb", 200*time.Millisecond, "time to first byte injected into every fake Jellyfin and infinidysk response")
	articleTTFB := fs.Duration("article-ttfb", 80*time.Millisecond, "time to first body byte injected into every fake NNTP article")
	container := fs.String("container", "mkv", "generated media `container`: mkv (cues at end) or mp4 (moov at end)")
	ffmpeg := fs.String("ffmpeg", "ffmpeg", "ffmpeg binary `path`, used once to generate the media")
	out := fs.String("o", "", "write the config to `file` instead of stdout")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %v\n", fs.Args())
		fs.Usage()
		return codeError{exitUsage}
	}

	fmt.Fprintln(stderr, "dev-stack: preparing media (the first run for a container encodes it, later runs reuse the cache)")
	st, err := fakestack.Start(ctx, fakestack.Options{
		Spec:           fakestack.MediaSpec{Container: *container},
		Dir:            *dir,
		JellyfinTTFB:   *ttfb,
		InfinidyskTTFB: *ttfb,
		ArticleTTFB:    *articleTTFB,
		FFmpeg:         *ffmpeg,
	})
	if err != nil {
		return err
	}
	defer st.Close()

	cfg := stdout
	if *out != "" {
		f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		cfg = f
	}
	enc := yaml.NewEncoder(cfg)
	enc.SetIndent(2)
	if err := enc.Encode(st.Config()); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}

	fmt.Fprintf(stderr, `dev-stack: fake stack is up
  jellyfin    %s  (ttfb %s)
  rclone      %s  (rc %s)
  infinidysk  %s  (ttfb %s)
  usenet      %s  (article ttfb %s)
  results     %s
measure it with `+"`nzbff run -c <config>`"+`, view it with `+"`nzbff serve -c <config>`"+`; Ctrl-C stops the stack
`,
		st.JellyfinURL, *ttfb, st.MountDir, st.RCURL, st.InfinidyskURL, *ttfb, st.NNTPAddr, *articleTTFB,
		st.Config().Results.Dir)

	<-ctx.Done()
	return nil
}
