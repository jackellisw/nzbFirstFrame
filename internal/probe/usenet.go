package probe

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
	"github.com/jackellisw/nzbfirstframe/internal/results"
)

func (p *Prober) sampleUsenet(ctx context.Context, rec *recorder, t config.Target, plan results.Plan) error {
	files, err := loadNZB(t.NZB)
	if err != nil {
		return err
	}
	file := largest(files)

	s, err := p.connectNNTP(ctx, rec)
	if err != nil {
		return p.phaseError(ctx, plan.Label(0), err.Error())
	}
	defer s.close()

	if u := p.cfg.Usenet; u.Username != "" {
		if err := s.auth(u.Username, u.Password); err != nil {
			return p.phaseError(ctx, plan.Label(0), err.Error())
		}
	}
	for phase := range plan.Len() {
		seg := file.Segments[0]
		if phase > 0 {
			seg = file.segmentAt(plan.Seeks[phase-1].Fraction)
		}
		if err := s.body(seg); err != nil {
			return p.phaseError(ctx, plan.Label(phase), err.Error())
		}
	}
	s.quit()
	return nil
}

type nntpSession struct {
	conn  net.Conn
	r     *bufio.Reader
	rec   *recorder
	scrub scrubber
	stop  func() bool
}

func (s *nntpSession) close() {
	s.stop()
	s.conn.Close()
}

func (p *Prober) connectNNTP(ctx context.Context, rec *recorder) (*nntpSession, error) {
	u := p.cfg.Usenet
	addr := net.JoinHostPort(u.Host, strconv.Itoa(u.Port))
	op := results.Op{Kind: results.OpConnect, Start: rec.stamp(), Offset: -1, Ref: addr}
	fail := func(err error) (*nntpSession, error) {
		op.Err, op.End = err.Error(), rec.stamp()
		rec.op(op)
		return nil, fmt.Errorf("connect %s: %w", addr, err)
	}

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fail(err)
	}
	if u.TLS {
		secure := tls.Client(conn, &tls.Config{ServerName: u.Host, RootCAs: p.nntpRoots})
		if err := secure.HandshakeContext(ctx); err != nil {
			conn.Close()
			return fail(err)
		}
		conn = secure
	}

	s := &nntpSession{conn: conn, r: bufio.NewReaderSize(conn, 64<<10), rec: rec, scrub: newScrubber(p.cfg)}
	// Reads on the connection ignore ctx; closing it is what makes the
	// per-sample timeout and Ctrl-C interrupt a stalled provider.
	s.stop = context.AfterFunc(ctx, func() { conn.Close() })
	code, line, at, err := s.status()
	if err == nil && code != 200 && code != 201 {
		err = fmt.Errorf("greeting: %s", line)
	}
	if err != nil {
		s.close()
		return fail(err)
	}
	op.FirstByte, op.End = at, at
	rec.op(op)
	return s, nil
}

func (s *nntpSession) auth(user, pass string) error {
	op := results.Op{Kind: results.OpAuth, Start: s.rec.stamp(), Offset: -1}
	code, line, at, err := s.command("AUTHINFO USER " + user)
	op.FirstByte = at
	if err == nil && code == 381 {
		code, line, at, err = s.command("AUTHINFO PASS " + pass)
	}
	if err == nil && code != 281 {
		err = fmt.Errorf("AUTHINFO: %s", line)
	}
	if err != nil {
		op.Err, op.End = err.Error(), s.rec.stamp()
		s.rec.op(op)
		return err
	}
	op.End = at
	s.rec.op(op)
	return nil
}

func (s *nntpSession) body(seg nzbSegment) error {
	ref := "<" + seg.MessageID + ">"
	op := results.Op{Kind: results.OpArticle, Start: s.rec.stamp(), Offset: seg.Offset, Ref: ref}
	code, line, _, err := s.command("BODY " + ref)
	if err == nil && code != 222 {
		err = fmt.Errorf("%s", line)
	}
	if err == nil {
		// The 222 line only says the provider has the article. Its TTFB is
		// the first byte of the body.
		_, err = s.r.Peek(1)
		op.FirstByte = s.rec.stamp()
	}
	if err == nil {
		op.Bytes, err = readDotBody(s.r)
	}
	op.End = s.rec.stamp()
	if err != nil {
		err = fmt.Errorf("BODY %s: %w", ref, err)
		op.Err = err.Error()
		s.rec.op(op)
		return err
	}
	s.rec.op(op)
	s.rec.mark(op.End, -1)
	return nil
}

func (s *nntpSession) quit() {
	s.conn.SetDeadline(time.Now().Add(time.Second))
	s.command("QUIT")
}

func (s *nntpSession) command(line string) (code int, reply string, at time.Duration, err error) {
	if _, err := io.WriteString(s.conn, line+"\r\n"); err != nil {
		return 0, "", 0, err
	}
	return s.status()
}

func (s *nntpSession) status() (code int, line string, at time.Duration, err error) {
	line, err = readStatusLine(s.r)
	at = s.rec.stamp()
	if err != nil {
		return 0, "", at, err
	}
	line = strings.TrimRight(line, "\r\n")
	digits, _, _ := strings.Cut(line, " ")
	code, err = strconv.Atoi(digits)
	if err != nil || len(digits) != 3 {
		return 0, "", at, fmt.Errorf("malformed NNTP status line %q", s.scrub.text(line))
	}
	return code, s.scrub.text(line), at, nil
}

// maxStatusLine is RFC 3977's limit on a response line, CRLF included.
const maxStatusLine = 512

func readStatusLine(r *bufio.Reader) (string, error) {
	line := make([]byte, 0, maxStatusLine)
	for len(line) < maxStatusLine {
		c, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		line = append(line, c)
		if c == '\n' {
			return string(line), nil
		}
	}
	return "", fmt.Errorf("status line longer than %d bytes", maxStatusLine)
}

func readDotBody(r *bufio.Reader) (int64, error) {
	var n int64
	lineStart := true
	for {
		chunk, err := r.ReadSlice('\n')
		// A line longer than the buffer comes back in pieces; only the
		// first piece is at the start of a line.
		whole := err == nil
		if err == io.EOF {
			return n, io.ErrUnexpectedEOF
		}
		if err != nil && err != bufio.ErrBufferFull {
			return n, err
		}
		if lineStart && chunk[0] == '.' {
			if whole && (len(chunk) == 2 || string(chunk) == ".\r\n") {
				return n, nil
			}
			chunk = chunk[1:]
		}
		n += int64(len(chunk))
		lineStart = whole
	}
}
