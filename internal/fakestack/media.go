package fakestack

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// MediaSpec describes generated test media.
type MediaSpec struct {
	Container string        // "mkv" (cues at end) or "mp4" (moov at end, no faststart)
	Length    time.Duration // default 3m
	GOP       int           // default 48 (libx264 -g)
}

func (m MediaSpec) withDefaults() MediaSpec {
	if m.Container == "" {
		m.Container = "mkv"
	}
	if m.Length <= 0 {
		m.Length = 3 * time.Minute
	}
	if m.GOP <= 0 {
		m.GOP = 48
	}
	return m
}

// muxers maps a container to its ffmpeg muxer. Both write their index
// (Cues, moov) after the media data unless told otherwise, which is the
// layout that costs a player extra round trips.
var muxers = map[string]string{"mkv": "matroska", "mp4": "mp4"}

// recipe names the encode settings below. Bump it when they change, so
// media cached by an older build is not reused.
const recipe = "r1"

// Generate encodes testsrc2 at 720p with libx264 into dir and returns the
// path. Tests skip when ffmpeg or ffprobe is not on PATH.
//
// The encode is cached under os.TempDir keyed by spec, so only the first
// call for a spec pays for it; dir gets a hard link (or a copy across
// filesystems).
func Generate(ctx context.Context, ffmpeg, dir string, spec MediaSpec) (string, error) {
	spec = spec.withDefaults()
	muxer, ok := muxers[spec.Container]
	if !ok {
		return "", fmt.Errorf("fakestack: unknown container %q (want mkv or mp4)", spec.Container)
	}
	name := fmt.Sprintf("testsrc2-720p-%ds-g%d-%s.%s", int(spec.Length.Seconds()), spec.GOP, recipe, spec.Container)
	cacheDir := filepath.Join(os.TempDir(), "nzbff-fakestack-media")
	cached := filepath.Join(cacheDir, name)

	if st, err := os.Stat(cached); err != nil || st.Size() == 0 {
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			return "", err
		}
		// Encode beside the cache entry and rename, so a concurrent test
		// process never sees a half-written file.
		tmp, err := os.CreateTemp(cacheDir, "encoding-*")
		if err != nil {
			return "", err
		}
		tmp.Close()
		defer os.Remove(tmp.Name())

		var stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, ffmpeg,
			"-v", "error", "-nostdin", "-y",
			"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=1280x720:rate=24:duration=%.3f", spec.Length.Seconds()),
			"-c:v", "libx264", "-preset", "veryfast", "-g", fmt.Sprint(spec.GOP), "-bf", "2", "-pix_fmt", "yuv420p",
			"-f", muxer, tmp.Name())
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("fakestack: ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		if err := os.Rename(tmp.Name(), cached); err != nil {
			return "", err
		}
	}

	out := filepath.Join(dir, name)
	if err := place(cached, out); err != nil {
		return "", err
	}
	return out, nil
}
