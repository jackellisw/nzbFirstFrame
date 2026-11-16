package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/results"
)

type source interface {
	open(ctx context.Context, off, end int64) (opened, error)
	close()
}

type opened struct {
	body io.ReadCloser
	size int64
	dial time.Duration
}

type unsatisfiableError struct{ size int64 }

func (e *unsatisfiableError) Error() string {
	if e.size < 0 {
		return "range starts past the end of the media"
	}
	return fmt.Sprintf("range starts past the end of the media (%d bytes)", e.size)
}

// tapDrainGrace bounds how long close waits for in-flight reads. Reads end
// on their own once ffprobe is gone, except a read blocked inside a stalled
// FUSE mount, which nothing can interrupt. Waiting for it would let a hung
// mount outlive profile.timeout.
const tapDrainGrace = time.Second

type tap struct {
	url string
	srv *http.Server
	src source
	rec *recorder
}

func startTap(src source, rec *recorder) (*tap, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start local tap: %w", err)
	}
	t := &tap{url: "http://" + ln.Addr().String() + "/media", src: src, rec: rec}
	t.srv = &http.Server{Handler: http.HandlerFunc(t.handle)}
	go t.srv.Serve(ln)
	return t, nil
}

func (t *tap) handle(w http.ResponseWriter, r *http.Request) {
	op := results.Op{Kind: results.OpRead, Start: t.rec.stamp(), Ref: r.Header.Get("Range")}
	fail := func(code int, msg string) {
		op.Err, op.End = msg, t.rec.stamp()
		t.rec.op(op)
		http.Error(w, msg, code)
	}

	off, end, ranged := int64(0), int64(-1), op.Ref != ""
	if ranged {
		var ok bool
		if off, end, ok = parseRange(op.Ref); !ok {
			fail(http.StatusRequestedRangeNotSatisfiable, fmt.Sprintf("unsupported Range %q", op.Ref))
			return
		}
	}
	op.Offset = off

	o, err := t.src.open(r.Context(), off, end)
	if un := (*unsatisfiableError)(nil); errors.As(err, &un) {
		if un.size >= 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", un.size))
		}
		fail(http.StatusRequestedRangeNotSatisfiable, err.Error())
		return
	}
	if err != nil && r.Context().Err() != nil {
		op.Abandoned, op.End = true, t.rec.stamp()
		t.rec.op(op)
		return
	}
	if err != nil {
		fail(http.StatusBadGateway, err.Error())
		return
	}
	op.Dial = o.dial

	last := o.size - 1
	if end >= 0 && end < last {
		last = end
	}
	length := last - off + 1

	h := w.Header()
	h.Set("Accept-Ranges", "bytes")
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", strconv.FormatInt(length, 10))
	if ranged {
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, last, o.size))
		w.WriteHeader(http.StatusPartialContent)
	}

	// Flushing every chunk keeps net/http's write buffer from holding a
	// short upstream read back from ffprobe, which would add latency the
	// stack did not cause.
	flusher := http.NewResponseController(w)
	body := io.LimitReader(o.body, length)
	buf := make([]byte, 32<<10)
	clientGone := false
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if op.FirstByte == 0 {
				op.FirstByte = t.rec.stamp()
			}
			m, werr := w.Write(buf[:n])
			op.Bytes += int64(m)
			if werr == nil {
				werr = flusher.Flush()
			}
			if werr != nil {
				clientGone = true
				break
			}
			op.End = t.rec.stamp()
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if r.Context().Err() != nil {
				clientGone = true
			} else {
				op.Err = rerr.Error()
			}
			break
		}
	}
	// End is the last delivery, not the hang-up. When ffprobe seeks it
	// stops reading at once but keeps the old connection open until the
	// next request's headers arrive, a whole upstream TTFB later. Ending
	// the op there would show bytes flowing through the next op's wait.
	if op.End == 0 {
		op.End = t.rec.stamp()
	}
	o.body.Close()

	switch {
	case op.Bytes == length:
	case clientGone:
		op.Abandoned = true
	case op.Err == "":
		op.Err = fmt.Sprintf("upstream ended after %d of %d bytes", op.Bytes, length)
	}
	t.rec.op(op)
}

func (t *tap) close() {
	ctx, cancel := context.WithTimeout(context.Background(), tapDrainGrace)
	defer cancel()
	if t.srv.Shutdown(ctx) != nil {
		t.srv.Close()
	}
}

func parseRange(h string) (off, end int64, ok bool) {
	spec, found := strings.CutPrefix(strings.TrimSpace(h), "bytes=")
	if !found {
		return 0, 0, false
	}
	first, lastStr, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, false
	}
	off, err := strconv.ParseInt(strings.TrimSpace(first), 10, 64)
	if err != nil || off < 0 {
		return 0, 0, false
	}
	lastStr = strings.TrimSpace(lastStr)
	if lastStr == "" {
		return off, -1, true
	}
	end, err = strconv.ParseInt(lastStr, 10, 64)
	if err != nil || end < off {
		return 0, 0, false
	}
	return off, end, true
}
