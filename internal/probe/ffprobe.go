package probe

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/results"
)

// ffprobeArgs builds the one-process session for a plan: open, then each
// seek in order, decoding one video frame per interval.
//
//	-v error -select_streams v:0 -show_frames -show_entries frame=pts_time
//	-of compact=p=0 -read_intervals %+#1,<s1>%+#1,<s2>%+#1 <url>
//
// Interval starts are seconds (not percent), formatted %.3f.
//
// Frames, not packets: ffprobe flushes the decoder at the end of every read
// interval, so interval k's frame line prints before interval k+1's first
// request. Measured on MKV and MP4 (h264, 2 B-frames): frame and packet
// lines agree within 15 ms per interval, and frames also catch streams that
// fail to decode.
func ffprobeArgs(url string, plan results.Plan) []string {
	intervals := "%+#1"
	for _, s := range plan.Seeks {
		intervals += fmt.Sprintf(",%.3f%%+#1", s.Offset.Seconds())
	}
	return []string{
		"-v", "error",
		"-select_streams", "v:0",
		"-show_frames",
		"-show_entries", "frame=pts_time",
		"-of", "compact=p=0",
		"-read_intervals", intervals,
		url,
	}
}

// pipeGrace is how long a killed ffprobe's output pipes may stay open
// before we stop waiting for them.
const pipeGrace = 500 * time.Millisecond

// ffprobe runs one session and turns its stdout into marks.
func (p *Prober) ffprobe(ctx context.Context, rec *recorder, url string, plan results.Plan) error {
	var stderr tail
	cmd := exec.CommandContext(ctx, p.cfg.Tools.FFprobe, ffprobeArgs(url, plan)...)
	cmd.Stderr = &stderr
	cmd.WaitDelay = pipeGrace
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return p.phaseError(ctx, plan.Label(0), fmt.Sprintf("ffprobe: %v", err))
	}

	// Killing ffprobe frees its pipes only if nothing else holds them. A
	// wrapper script's child can, so the timeout also closes our end of
	// stdout here and (WaitDelay) of stderr in Wait.
	defer context.AfterFunc(ctx, func() { stdout.Close() })()

	lines := bufio.NewReader(stdout)
	for {
		line, rerr := lines.ReadString('\n')
		// Stamped before parsing: the next seek's request follows the line
		// by ~0.3 ms, and a late stamp would move time from this phase into
		// the next.
		at := rec.now()
		if pts, ok := parseFrameLine(line); !ok {
			if strings.TrimSpace(line) != "" {
				p.log.Debug("ffprobe: ignored output line", "line", strings.TrimSpace(line))
			}
		} else if rec.marked() < plan.Len() {
			rec.mark(at, pts)
		}
		if rerr != nil {
			break
		}
	}
	werr := cmd.Wait()

	if n := rec.marked(); n < plan.Len() {
		detail := "ffprobe exited without printing a frame"
		if werr != nil {
			detail = "ffprobe: " + werr.Error()
		}
		if s := stderr.String(); s != "" {
			detail += ": " + s
		}
		return p.phaseError(ctx, plan.Label(n), detail)
	}
	if werr != nil {
		// ffprobe can exit non-zero over a read it abandoned. Every phase
		// completed, so the sample stands.
		p.log.Debug("ffprobe exited non-zero after the last frame", "err", werr, "stderr", stderr.String())
	}
	return nil
}

// parseFrameLine extracts pts_time from a compact line such as
// "pts_time=30.000000" (p=0 drops the section name). "N/A" → -1, as is any
// value JSON cannot store.
func parseFrameLine(line string) (pts float64, ok bool) {
	for _, field := range strings.Split(strings.TrimSpace(line), "|") {
		v, found := strings.CutPrefix(field, "pts_time=")
		if !found {
			continue
		}
		if v == "N/A" {
			return -1, true
		}
		pts, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, false
		}
		if math.IsNaN(pts) || math.IsInf(pts, 0) {
			return -1, true
		}
		return pts, true
	}
	return 0, false
}

// mediaDuration runs a format=duration probe against url.
func (p *Prober) mediaDuration(ctx context.Context, url string) (time.Duration, error) {
	var stderr tail
	cmd := exec.CommandContext(ctx, p.cfg.Tools.FFprobe,
		"-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", url)
	cmd.Stderr = &stderr
	cmd.WaitDelay = pipeGrace
	out, err := cmd.Output()
	if err != nil {
		detail := "ffprobe: " + err.Error()
		if s := stderr.String(); s != "" {
			detail += ": " + s
		}
		return 0, errors.New(detail)
	}
	text := strings.TrimSpace(string(out))
	secs, err := strconv.ParseFloat(text, 64)
	if err != nil || secs <= 0 {
		return 0, fmt.Errorf("ffprobe reported duration %q", text)
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// tail keeps the last tailSize bytes written to it: enough of ffprobe's
// stderr to say why it failed without holding a runaway log.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

const tailSize = 4 << 10

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if over := len(t.buf) - tailSize; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(b), nil
}

// String returns the tail on one line.
func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(strings.Fields(string(t.buf)), " ")
}
