package fakestack

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"strings"
	"sync"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

type newsServer struct {
	stack *Stack
	name  string
	size  int64
	parts int

	ln   net.Listener
	wg   sync.WaitGroup
	ctx  context.Context
	stop context.CancelFunc

	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

const (
	decoyNFOMessageID = "nfo@fakestack"
	nfoText           = "fakestack test release\r\n"
)

func newNewsServer(s *Stack, name string, size int64) *newsServer {
	parts := int((size + s.opts.SegmentSize - 1) / s.opts.SegmentSize)
	n := &newsServer{stack: s, name: name, size: size, parts: parts, conns: map[net.Conn]struct{}{}}
	n.ctx, n.stop = context.WithCancel(context.Background())
	return n
}

func partMessageID(part int) string { return fmt.Sprintf("part%05d@fakestack", part) }

func (n *newsServer) partRange(part int) (off, length int64) {
	off = int64(part-1) * n.stack.opts.SegmentSize
	return off, min(n.stack.opts.SegmentSize, n.size-off)
}

func (n *newsServer) nzb() []byte {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd">` + "\n")
	b.WriteString(`<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">` + "\n")
	file := func(subject string, segments func()) {
		b.WriteString(` <file poster="fakestack &lt;fake@fakestack&gt;" date="1700000000" subject="`)
		xml.EscapeText(&b, []byte(subject))
		b.WriteString(`">` + "\n  <groups><group>alt.binaries.fakestack</group></groups>\n  <segments>\n")
		segments()
		b.WriteString("  </segments>\n </file>\n")
	}
	file(`"fake.nfo" yEnc (1/1)`, func() {
		fmt.Fprintf(&b, `   <segment bytes="%d" number="1">%s</segment>`+"\n", len(nfoText), decoyNFOMessageID)
	})
	file(fmt.Sprintf(`"%s" yEnc (1/%d)`, n.name, n.parts), func() {
		for part := 1; part <= n.parts; part++ {
			_, length := n.partRange(part)
			fmt.Fprintf(&b, `   <segment bytes="%d" number="%d">%s</segment>`+"\n", length, part, partMessageID(part))
		}
	})
	b.WriteString("</nzb>\n")
	return b.Bytes()
}

func (n *newsServer) article(id string) ([]byte, bool) {
	if id == decoyNFOMessageID {
		return yenc("fake.nfo", 1, 1, int64(len(nfoText)), 0, []byte(nfoText)), true
	}
	var part int
	if _, err := fmt.Sscanf(id, "part%05d@fakestack", &part); err != nil || part < 1 || part > n.parts || id != partMessageID(part) {
		return nil, false
	}
	off, length := n.partRange(part)
	data := make([]byte, length)
	if _, err := n.stack.media.ReadAt(data, off); err != nil {
		return nil, false
	}
	return yenc(n.name, part, n.parts, n.size, off, data), true
}

// yenc encodes one part: every byte plus 42, with NUL, CR, LF and "="
// escaped, in lines of 128 characters. A leading "." is left alone; hiding
// it is the transport's job (dot-stuffing).
func yenc(name string, part, total int, fileSize, off int64, data []byte) []byte {
	var b bytes.Buffer
	b.Grow(len(data) + len(data)/16)
	fmt.Fprintf(&b, "=ybegin part=%d total=%d line=128 size=%d name=%s\r\n", part, total, fileSize, name)
	fmt.Fprintf(&b, "=ypart begin=%d end=%d\r\n", off+1, off+int64(len(data)))
	col := 0
	for _, c := range data {
		e := c + 42
		if e == 0 || e == '\r' || e == '\n' || e == '=' {
			b.WriteByte('=')
			e += 64
			col++
		}
		b.WriteByte(e)
		if col++; col >= 128 {
			b.WriteString("\r\n")
			col = 0
		}
	}
	if col > 0 {
		b.WriteString("\r\n")
	}
	fmt.Fprintf(&b, "=yend size=%d part=%d pcrc32=%08x\r\n", len(data), part, crc32.ChecksumIEEE(data))
	return b.Bytes()
}

func dotStuff(payload []byte) []byte {
	out := bytes.ReplaceAll(payload, []byte("\r\n."), []byte("\r\n.."))
	if bytes.HasPrefix(out, []byte(".")) {
		out = append([]byte("."), out...)
	}
	return out
}

func (n *newsServer) listen() (addr string, err error) {
	if n.ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
		return "", err
	}
	n.wg.Go(func() {
		for {
			conn, err := n.ln.Accept()
			if err != nil {
				return
			}
			n.mu.Lock()
			n.conns[conn] = struct{}{}
			n.mu.Unlock()
			n.wg.Go(func() {
				n.session(conn)
				n.mu.Lock()
				delete(n.conns, conn)
				n.mu.Unlock()
				conn.Close()
			})
		}
	})
	return n.ln.Addr().String(), nil
}

func (n *newsServer) close() {
	n.stop()
	if n.ln == nil {
		return
	}
	n.ln.Close()
	n.mu.Lock()
	for conn := range n.conns {
		conn.Close()
	}
	n.mu.Unlock()
	n.wg.Wait()
}

func (n *newsServer) session(conn net.Conn) {
	in := bufio.NewReader(conn)
	say := func(line string) { io.WriteString(conn, line+"\r\n") }

	say("200 fakestack NNTP ready")
	user, authed := "", false
	for {
		line, err := in.ReadString('\n')
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
		switch strings.ToUpper(verb) {
		case "AUTHINFO":
			kind, value, _ := strings.Cut(arg, " ")
			switch strings.ToUpper(kind) {
			case "USER":
				user = value
				say("381 password required")
			case "PASS":
				if authed = user == n.stack.User && value == n.stack.Pass; authed {
					say("281 authentication accepted")
				} else {
					say("481 authentication failed")
				}
			default:
				say("501 syntax error")
			}
		case "BODY":
			id := strings.TrimSuffix(strings.TrimPrefix(arg, "<"), ">")
			payload, found := n.article(id)
			switch {
			case !authed:
				say("480 authentication required")
			case !found || "<"+id+">" != arg:
				say("430 no such article")
			default:
				sent := n.stack.hit(config.LayerUsenet, arg)
				wire := append(dotStuff(payload), ".\r\n"...)
				say("222 0 " + arg)
				if !wait(n.ctx, n.stack.opts.ArticleTTFB) {
					return
				}
				if _, err := conn.Write(wire); err != nil {
					return
				}
				sent(int64(len(payload)))
			}
		case "QUIT":
			say("205 bye")
			return
		default:
			say("500 unknown command")
		}
	}
}
