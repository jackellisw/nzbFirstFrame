package probe

import (
	"context"
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
func ffprobeArgs(url string, plan results.Plan) []string { panic("not implemented") }

// ffprobe runs one session and turns its stdout into marks.
func (p *Prober) ffprobe(ctx context.Context, rec *recorder, url string, plan results.Plan) error {
	// TODO:
	//   cmd := exec.CommandContext(ctx, p.cfg.Tools.FFprobe, ffprobeArgs(url, plan)...)
	//   stdout pipe; stderr into a 4 KiB ring for the error message
	//   reader goroutine: bufio.Reader.ReadString('\n') in a loop;
	//     at := rec.now() IMMEDIATELY after the read returns, before parsing
	//     (the next seek's request follows the line by ~0.3ms; stamping late
	//     would move time from seek k-1 into seek k)
	//     pts, ok := parseFrameLine(line); if !ok { log.Debug; continue }
	//     if marks so far == plan.Len() { continue }  // defensive: extra lines
	//     rec.mark(at, pts)
	//   wait; if marks < plan.Len(): return fmt.Errorf("phase %s: no frame (ffprobe: %s)", plan.Label(n), stderrTail)
	//   exit error with all marks present: log, return nil (ffprobe often exits
	//   non-zero on an abandoned read; the phases completed)
	panic("not implemented")
}

// parseFrameLine extracts pts_time from a compact line such as
// "pts_time=30.000000" (p=0 drops the section name). "N/A" → -1.
func parseFrameLine(line string) (pts float64, ok bool) { panic("not implemented") }

// mediaDuration runs a format=duration probe against url.
func (p *Prober) mediaDuration(ctx context.Context, url string) (time.Duration, error) {
	panic("not implemented")
}
