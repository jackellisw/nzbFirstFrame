package probe

import (
	"context"
	"io"
	"net"
	"net/http"
	"time"
)

// source is one layer's byte supply behind the tap. It only knows how to
// read the media from an offset; the tap owns HTTP Range semantics, timing
// and accounting, so they are identical for every layer.
//
// Invariant: a source does no upstream I/O outside open. In particular it
// learns the size from the response to the first open, never from a
// separate stat/HEAD, so nothing that touches upstream escapes the timeline
// (a stat on the mount would also undo a cold-cache vfs/forget).
type source interface {
	// open starts reading [off, end] (end == -1: to EOF). It returns once the
	// upstream has answered (response headers, or file opened and seeked);
	// the first Read on body then blocks for the first payload byte. size is
	// the full media size.
	open(ctx context.Context, off, end int64) (opened, error)
}

// opened is a started read.
type opened struct {
	body io.ReadCloser
	size int64
	// dial reports time spent opening a new upstream connection for this
	// read (0 if reused). Called after body is drained or closed, because
	// httptrace fills it asynchronously.
	dial func() time.Duration
}

// tap is a loopback HTTP server ffprobe reads from. Every request becomes
// one timeline.Op.
type tap struct {
	url  string // http://127.0.0.1:<port>/media
	ln   net.Listener
	srv  *http.Server
	src  source
	rec  *recorder
	done chan struct{}
}

func startTap(src source, rec *recorder) (*tap, error) {
	// TODO: listen 127.0.0.1:0; serve t.handle; url ends in a fixed name with
	// the source's extension is unnecessary (ffprobe sniffs content).
	panic("not implemented")
}

// handle serves one ffprobe request.
func (t *tap) handle(w http.ResponseWriter, r *http.Request) {
	// TODO:
	//   start := t.rec.now()
	//   off, end, ok := parseRange(r.Header.Get("Range"))  // single range only;
	//       absent → 0,-1 and reply 200; multi-range → 416 + op.Err
	//   o, err := t.src.open(r.Context(), off, end)
	//   if err → 502, record Op{Kind: read, Start, End: now, Offset: off, Err}
	//   write 206, Accept-Ranges: bytes, Content-Range: off-last/o.size, Content-Length
	//   copy loop with 32 KiB buffer:
	//       n, rerr := o.body.Read(buf)
	//       if n > 0 && firstByte == 0 { firstByte = t.rec.now() }   // stamp before writing
	//       _, werr := w.Write(buf[:n]); bytes += n
	//       werr → abandoned = true (client went away: ffprobe seeked); break
	//       rerr == EOF → break; other rerr → op.Err
	//   record Op{Kind: read, Start, FirstByte, End: now, Offset: off, Bytes, Dial: o.dial(),
	//             Abandoned, Ref: r.Header.Get("Range")}
	panic("not implemented")
}

// close stops the server and waits for in-flight handlers to record their
// ops (abandoned reads finish when their upstream body is closed).
func (t *tap) close() error { panic("not implemented") }

// parseRange parses a single "bytes=N-" or "bytes=N-M" header.
func parseRange(h string) (off, end int64, ok bool) { panic("not implemented") }
