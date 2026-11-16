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

// sampleUsenet measures the provider floor for the same plan, one NNTP
// session per sample, with the same phase semantics as the video layers so
// Analyze needs no special case:
//
//	op connect  dial (+TLS) until the 200/201 greeting
//	op auth     AUTHINFO USER → 381, AUTHINFO PASS → 281 (skipped without creds)
//	phase 0     op article: BODY <first segment of the largest file>; mark at
//	            the terminating "." line (the article is needed whole before
//	            infinidysk can yEnc-decode and serve it)
//	phase k     op article: BODY <segment at plan.Seeks[k-1].Fraction>; mark
//	            QUIT
//
// Article op: Start when the command is written, FirstByte when the first
// body byte after the 222 status line arrives (the provider's TTFB), End at
// ".", Bytes = body bytes after dot-unstuffing, Offset = the segment's
// cumulative byte offset. Connect and auth ops carry the arrival of the
// server's first reply line as FirstByte.
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

// nntpSession is one connection to the provider, recording its exchanges
// on the sample's timeline.
type nntpSession struct {
	conn net.Conn
	r    *bufio.Reader
	rec  *recorder
	stop func() bool // detaches conn from the sample's context
}

func (s *nntpSession) close() {
	s.stop()
	s.conn.Close()
}

// connectNNTP dials the provider (with TLS when configured) and reads the
// greeting, as one connect op.
func (p *Prober) connectNNTP(ctx context.Context, rec *recorder) (*nntpSession, error) {
	u := p.cfg.Usenet
	addr := net.JoinHostPort(u.Host, strconv.Itoa(u.Port))
	op := results.Op{Kind: results.OpConnect, Start: rec.now(), Offset: -1, Ref: addr}
	fail := func(err error) (*nntpSession, error) {
		op.Err, op.End = err.Error(), rec.now()
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

	s := &nntpSession{conn: conn, r: bufio.NewReaderSize(conn, 64<<10), rec: rec}
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

// auth runs AUTHINFO USER/PASS as one auth op.
func (s *nntpSession) auth(user, pass string) error {
	op := results.Op{Kind: results.OpAuth, Start: s.rec.now(), Offset: -1}
	code, line, at, err := s.command("AUTHINFO USER " + user)
	op.FirstByte = at
	if err == nil && code == 381 {
		code, line, at, err = s.command("AUTHINFO PASS " + pass)
	}
	if err == nil && code != 281 {
		err = fmt.Errorf("AUTHINFO: %s", line)
	}
	if err != nil {
		op.Err, op.End = err.Error(), s.rec.now()
		s.rec.op(op)
		return err
	}
	op.End = at
	s.rec.op(op)
	return nil
}

// body fetches one article as one article op and closes the next phase when
// its last byte has arrived.
func (s *nntpSession) body(seg nzbSegment) error {
	ref := "<" + seg.MessageID + ">"
	op := results.Op{Kind: results.OpArticle, Start: s.rec.now(), Offset: seg.Offset, Ref: ref}
	code, line, _, err := s.command("BODY " + ref)
	if err == nil && code != 222 {
		err = fmt.Errorf("%s", line)
	}
	if err == nil {
		// The 222 line only says the provider has the article. Its TTFB is
		// the first byte of the body.
		_, err = s.r.Peek(1)
		op.FirstByte = s.rec.now()
	}
	if err == nil {
		op.Bytes, err = readDotBody(s.r)
	}
	op.End = s.rec.now()
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

// quit ends the session politely. The measurement is over, so a provider
// that drops the connection instead of answering is not an error.
func (s *nntpSession) quit() {
	s.conn.SetDeadline(time.Now().Add(time.Second))
	s.command("QUIT")
}

// command sends one command line and reads the status line that answers it.
func (s *nntpSession) command(line string) (code int, reply string, at time.Duration, err error) {
	if _, err := io.WriteString(s.conn, line+"\r\n"); err != nil {
		return 0, "", 0, err
	}
	return s.status()
}

// status reads one status line ("222 0 <id> body") and stamps its arrival.
func (s *nntpSession) status() (code int, line string, at time.Duration, err error) {
	line, err = s.r.ReadString('\n')
	at = s.rec.now()
	if err != nil {
		return 0, "", at, err
	}
	line = strings.TrimRight(line, "\r\n")
	digits, _, _ := strings.Cut(line, " ")
	code, err = strconv.Atoi(digits)
	if err != nil || len(digits) != 3 {
		return 0, "", at, fmt.Errorf("malformed NNTP status line %q", line)
	}
	return code, line, at, nil
}

// readDotBody consumes a multi-line data block up to its "." terminator and
// returns the size of the content: every line with its line ending, minus
// the dot that dot-stuffing put in front of lines that begin with one.
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
