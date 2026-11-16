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

// Packet lines delimit the read intervals and frame lines mark them. Each
// interval (#1) prints exactly one packet line when it reads its packet, and
// ffprobe flushes the decoder at the end of every interval, so interval k's
// frame line prints before interval k+1's packet line and request. An
// interval can print no frame: a stream cut between keyframes decodes
// nothing from the startup packet, and counting frame lines alone would
// hand the next interval's frame to startup. Measured on MKV and MP4 (h264,
// 2 B-frames, with and without audio): frame and packet lines agree within
// 15 ms per interval, and frames also catch streams that fail to decode.
func ffprobeArgs(url string, plan results.Plan) []string {
	intervals := "%+#1"
	for _, s := range plan.Seeks {
		intervals += fmt.Sprintf(",%.3f%%+#1", s.Offset.Seconds())
	}
	return []string{
		"-v", "error",
		"-select_streams", "v:0",
		"-show_packets", "-show_frames",
		"-show_entries", "packet=pts_time:frame=pts_time",
		"-of", "compact",
		"-read_intervals", intervals,
		url,
	}
}

const pipeGrace = 500 * time.Millisecond

func (p *Prober) ffprobe(ctx context.Context, rec *recorder, url string, plan results.Plan) error {
	var stderr tail
	run, kill := context.WithCancel(ctx)
	defer kill()
	cmd := exec.CommandContext(run, p.cfg.Tools.FFprobe, ffprobeArgs(url, plan)...)
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
	// wrapper script's child can, so a kill also closes our end of stdout
	// here and (WaitDelay) of stderr in Wait.
	defer context.AfterFunc(run, func() { stdout.Close() })()

	interval := -1
	lines := bufio.NewReader(stdout)
	for {
		line, rerr := lines.ReadString('\n')
		// Stamped before parsing: the next seek's request follows the line
		// by ~0.3 ms, and a late stamp would move time from this phase into
		// the next.
		at := rec.stamp()
		switch section, pts := parseLine(line); section {
		case "packet":
			interval++
			// The previous interval printed no frame, and marks must stay
			// contiguous, so no later phase can be marked either.
			if interval > rec.marked() {
				kill()
			}
		case "frame":
			if interval == rec.marked() && interval < plan.Len() {
				rec.mark(at, pts)
			}
		default:
			if strings.TrimSpace(line) != "" {
				p.log.Debug("ffprobe: ignored output line", "line", strings.TrimSpace(line))
			}
		}
		if rerr != nil {
			break
		}
	}
	werr := cmd.Wait()

	n := rec.marked()
	if n == plan.Len() {
		if werr != nil {
			// ffprobe can exit non-zero over a read it abandoned. Every
			// phase completed, so the sample stands.
			p.log.Debug("ffprobe exited non-zero after the last frame", "err", werr, "stderr", stderr.String())
		}
		return nil
	}
	detail := "ffprobe exited without printing a frame"
	switch {
	case interval >= n:
		detail = "ffprobe read this phase's packet but decoded no frame from it"
	case werr != nil:
		detail = "ffprobe: " + werr.Error()
	}
	if s := stderr.String(); s != "" {
		detail += ": " + s
	}
	return p.phaseError(ctx, plan.Label(n), detail)
}

// parseLine reads one line of ffprobe's compact output. section is "packet",
// "frame", or "" for any other line; pts is a frame's presentation time in
// seconds, -1 when ffprobe printed none.
func parseLine(line string) (section string, pts float64) {
	fields := strings.Split(strings.TrimSpace(line), "|")
	switch fields[0] {
	case "packet":
		return "packet", 0
	case "frame":
		for _, field := range fields[1:] {
			v, found := strings.CutPrefix(field, "pts_time=")
			if !found {
				continue
			}
			if v == "N/A" {
				return "frame", -1
			}
			pts, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return "", 0
			}
			if math.IsNaN(pts) || math.IsInf(pts, 0) {
				return "frame", -1
			}
			return "frame", pts
		}
	}
	return "", 0
}

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

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(strings.Fields(string(t.buf)), " ")
}
