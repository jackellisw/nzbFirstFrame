package probe

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/results"
)

type stubSource struct {
	data      []byte
	firstByte time.Duration
	short     int64
	openErr   error
	hang      bool

	mu    sync.Mutex
	asked [][2]int64
}

func (s *stubSource) open(ctx context.Context, off, end int64) (opened, error) {
	s.mu.Lock()
	s.asked = append(s.asked, [2]int64{off, end})
	s.mu.Unlock()
	if s.hang {
		<-ctx.Done()
		return opened{}, ctx.Err()
	}
	if s.openErr != nil {
		return opened{}, s.openErr
	}
	size := int64(len(s.data))
	if off >= size {
		return opened{}, &unsatisfiableError{size: size}
	}
	body := &slowBody{Reader: bytes.NewReader(s.data[off : size-s.short]), delay: s.firstByte}
	return opened{body: body, size: size, dial: 7 * time.Millisecond}, nil
}

func (s *stubSource) close() {}

type slowBody struct {
	io.Reader
	delay time.Duration
}

func (b *slowBody) Read(p []byte) (int, error) {
	if b.delay > 0 {
		time.Sleep(b.delay)
		b.delay = 0
	}
	return b.Reader.Read(p)
}

func (b *slowBody) Close() error { return nil }

func tapOver(t *testing.T, src source) (*tap, *recorder) {
	t.Helper()
	rec := newRecorder()
	tp, err := startTap(src, rec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tp.close)
	return tp, rec
}

func get(t *testing.T, url, rangeHeader string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func soleOp(t *testing.T, tp *tap, rec *recorder) results.Op {
	t.Helper()
	tp.close()
	ops := rec.snapshot().Ops
	if len(ops) != 1 {
		t.Fatalf("recorded %d ops, want 1: %+v", len(ops), ops)
	}
	return ops[0]
}

func TestTapRangeSemantics(t *testing.T) {
	data := make([]byte, 1000)
	for i := range data {
		data[i] = byte(i % 251)
	}
	for _, tc := range []struct {
		name, header string
		status       int
		contentRange string
		body         []byte
		asked        [][2]int64
	}{
		{"no range", "", 200, "", data, [][2]int64{{0, -1}}},
		{"open end", "bytes=100-", 206, "bytes 100-999/1000", data[100:], [][2]int64{{100, -1}}},
		{"closed", "bytes=100-199", 206, "bytes 100-199/1000", data[100:200], [][2]int64{{100, 199}}},
		{"single byte", "bytes=0-0", 206, "bytes 0-0/1000", data[:1], [][2]int64{{0, 0}}},
		{"end past eof", "bytes=900-5000", 206, "bytes 900-999/1000", data[900:], [][2]int64{{900, 5000}}},
		{"start at eof", "bytes=1000-", 416, "bytes */1000", nil, [][2]int64{{1000, -1}}},
		{"multi range", "bytes=0-10,20-30", 416, "", nil, nil},
		{"suffix range", "bytes=-500", 416, "", nil, nil},
		{"inverted", "bytes=200-100", 416, "", nil, nil},
		{"other unit", "items=0-5", 416, "", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &stubSource{data: data}
			tp, rec := tapOver(t, src)
			resp, body := get(t, tp.url, tc.header)

			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.status)
			}
			if got := resp.Header.Get("Content-Range"); got != tc.contentRange {
				t.Errorf("Content-Range %q, want %q", got, tc.contentRange)
			}
			if len(src.asked) != len(tc.asked) || (len(tc.asked) == 1 && src.asked[0] != tc.asked[0]) {
				t.Errorf("source asked for %v, want %v", src.asked, tc.asked)
			}
			op := soleOp(t, tp, rec)
			if op.Kind != results.OpRead || op.Ref != tc.header {
				t.Errorf("op %+v: want a read with Ref %q", op, tc.header)
			}
			if tc.status >= 400 {
				if op.Err == "" || op.Bytes != 0 || op.FirstByte != 0 {
					t.Errorf("op %+v: want a failed read with no payload", op)
				}
				return
			}
			if !bytes.Equal(body, tc.body) {
				t.Errorf("body is %d bytes, want the %d requested", len(body), len(tc.body))
			}
			if resp.Header.Get("Accept-Ranges") != "bytes" || resp.ContentLength != int64(len(tc.body)) {
				t.Errorf("Accept-Ranges %q, Content-Length %d; want bytes, %d",
					resp.Header.Get("Accept-Ranges"), resp.ContentLength, len(tc.body))
			}
			if op.Offset != tc.asked[0][0] || op.Bytes != int64(len(tc.body)) || op.Abandoned || op.Err != "" {
				t.Errorf("op %+v: want offset %d and all %d bytes delivered", op, tc.asked[0][0], len(tc.body))
			}
			if op.Dial != 7*time.Millisecond {
				t.Errorf("op.Dial = %v, want the source's 7ms", op.Dial)
			}
		})
	}
}

func TestTapStampsFirstByteWhenItArrivesBeforeForwardingIt(t *testing.T) {
	const delay = 120 * time.Millisecond
	tp, rec := tapOver(t, &stubSource{data: make([]byte, 4096), firstByte: delay})

	sent := rec.stamp()
	resp, err := http.Get(tp.url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadFull(resp.Body, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	received := rec.stamp()
	io.Copy(io.Discard, resp.Body)

	op := soleOp(t, tp, rec)
	if op.Start < sent || op.Start > received {
		t.Errorf("Start %v is outside the request's lifetime [%v, %v]", op.Start, sent, received)
	}
	if wait := op.FirstByte - op.Start; wait < delay {
		t.Errorf("TTFB %v, less than the source's %v", wait, delay)
	}
	if op.FirstByte > received {
		t.Errorf("FirstByte %v stamped after the client already had the byte at %v", op.FirstByte, received)
	}
	if op.End < op.FirstByte {
		t.Errorf("End %v before FirstByte %v", op.End, op.FirstByte)
	}
}

func TestTapMarksReadAbandonedWhenClientHangsUp(t *testing.T) {
	const size = 64 << 20
	tp, rec := tapOver(t, &stubSource{data: make([]byte, size)})

	resp, err := http.Get(tp.url)
	if err != nil {
		t.Fatal(err)
	}
	const consumed = 1000
	if _, err := io.ReadFull(resp.Body, make([]byte, consumed)); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	op := soleOp(t, tp, rec)
	if !op.Abandoned || op.Err != "" {
		t.Errorf("op %+v: want Abandoned and no Err", op)
	}
	if op.Bytes < consumed || op.Bytes >= size {
		t.Errorf("Bytes = %d, want at least the %d consumed and less than the whole %d", op.Bytes, consumed, size)
	}
}

func TestTapMarksReadAbandonedWhenClientHangsUpBeforeUpstreamAnswers(t *testing.T) {
	tp, rec := tapOver(t, &stubSource{hang: true})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, tp.url, nil)
	if _, err := http.DefaultClient.Do(req); err == nil {
		t.Fatal("request succeeded against a source that never answers")
	}

	op := soleOp(t, tp, rec)
	if !op.Abandoned || op.Err != "" || op.Bytes != 0 {
		t.Errorf("op %+v: want an abandoned read, not an upstream failure", op)
	}
}

func TestTapReportsUpstreamFailures(t *testing.T) {
	t.Run("open fails", func(t *testing.T) {
		tp, rec := tapOver(t, &stubSource{openErr: errors.New("dial tcp: connection refused")})
		resp, _ := get(t, tp.url, "bytes=0-")
		if resp.StatusCode != http.StatusBadGateway {
			t.Errorf("status %d, want 502", resp.StatusCode)
		}
		if op := soleOp(t, tp, rec); op.Err != "dial tcp: connection refused" || op.Abandoned {
			t.Errorf("op %+v: want the open error", op)
		}
	})
	t.Run("body ends early", func(t *testing.T) {
		tp, rec := tapOver(t, &stubSource{data: make([]byte, 1000), short: 400})
		req, _ := http.NewRequest(http.MethodGet, tp.url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err == nil {
			t.Error("client read a truncated body without an error")
		}
		if op := soleOp(t, tp, rec); op.Err == "" || op.Bytes != 600 || op.Abandoned {
			t.Errorf("op %+v: want an error after 600 of 1000 bytes", op)
		}
	})
}
