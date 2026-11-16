package probe_test

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
	"github.com/jackellisw/nzbfirstframe/internal/results"
)

type news struct {
	greeting string
	bodies   map[string]string
	stall    string

	mu       sync.Mutex
	commands []string
}

func (n *news) serve(t *testing.T, ln net.Listener) {
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go n.session(conn)
		}
	}()
}

func (n *news) session(conn net.Conn) {
	defer conn.Close()
	fmt.Fprintf(conn, "%s\r\n", n.greeting)
	in := bufio.NewScanner(conn)
	for in.Scan() {
		line := in.Text()
		n.mu.Lock()
		n.commands = append(n.commands, line)
		n.mu.Unlock()
		verb, arg, _ := strings.Cut(line, " ")
		switch verb {
		case "AUTHINFO":
			if strings.HasPrefix(arg, "USER") {
				fmt.Fprint(conn, "381 more\r\n")
			} else {
				fmt.Fprint(conn, "281 ok\r\n")
			}
		case "BODY":
			body, ok := n.bodies[arg]
			switch {
			case arg == n.stall:
				fmt.Fprint(conn, "222 0 "+arg+"\r\nfirst half\r\n")
				time.Sleep(time.Minute)
			case ok:
				fmt.Fprint(conn, "222 0 "+arg+"\r\n"+body+".\r\n")
			default:
				fmt.Fprint(conn, "430 no such article\r\n")
			}
		case "QUIT":
			fmt.Fprint(conn, "205 bye\r\n")
			return
		}
	}
}

func (n *news) saw() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.commands)
}

func usenetConfig(t *testing.T, ln net.Listener, nzb string) (*config.Config, config.Target) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "release.nzb")
	if err := os.WriteFile(path, []byte(nzb), 0o644); err != nil {
		t.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	cfg := config.Default()
	cfg.Usenet.Host, cfg.Usenet.TLS = host, false
	fmt.Sscan(port, &cfg.Usenet.Port)
	cfg.Targets = []config.Target{{Name: "release", NZB: path}}
	if err := cfg.Finalize(); err != nil {
		t.Fatal(err)
	}
	return cfg, cfg.Targets[0]
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

const mixedNZB = `<?xml version="1.0" encoding="iso-8859-1"?>
<!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd">
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
 <file poster="p" date="1" subject="caf` + "\xe9" + `.nfo (1/1)">
  <segments><segment bytes="50" number="1">nfo@example</segment></segments>
 </file>
 <file poster="p" date="1" subject="movie.mkv (1/3)">
  <groups><group>alt.binaries.test</group></groups>
  <segments>
   <segment bytes="600" number="3">&lt;m3@example&gt;</segment>
   <segment bytes="100" number="1">m1@example</segment>
   <segment bytes="300" number="2"> m2@example </segment>
   <segment bytes="300" number="2">m2-repost@example</segment>
  </segments>
 </file>
</nzb>`

func TestUsenetFetchesTheSegmentAtEachFractionOfTheLargestFile(t *testing.T) {
	srv := &news{greeting: "201 no posting", bodies: map[string]string{
		"<m1@example>": "one\r\n", "<m2@example>": "two\r\n", "<m3@example>": "three\r\n",
	}}
	ln := listen(t)
	srv.serve(t, ln)
	cfg, target := usenetConfig(t, ln, mixedNZB)

	plan := planAt(0.05, 0.1, 0.39, 0.4, 1)
	tl, err := newProber(cfg).Sample(t.Context(), config.LayerUsenet, target, plan)
	if err != nil {
		t.Fatal(err)
	}
	checkTimelineInvariants(t, tl)

	want := []string{"BODY <m1@example>", "BODY <m1@example>", "BODY <m2@example>", "BODY <m2@example>",
		"BODY <m3@example>", "BODY <m3@example>", "QUIT"}
	if got := srv.saw(); !slices.Equal(got, want) {
		t.Errorf("commands %q\n    want %q (no AUTHINFO without credentials)", got, want)
	}
	var offsets []int64
	for _, op := range tl.Ops {
		if op.Kind == results.OpArticle {
			offsets = append(offsets, op.Offset)
		} else if op.Kind != results.OpConnect {
			t.Errorf("unexpected %s op without credentials", op.Kind)
		}
	}
	if want := []int64{0, 0, 100, 100, 400, 400}; !slices.Equal(offsets, want) {
		t.Errorf("article offsets %v, want %v", offsets, want)
	}
	if len(tl.Marks) != plan.Len() {
		t.Errorf("%d marks, want %d", len(tl.Marks), plan.Len())
	}
}

func TestUsenetBodySizeIsTheContentAfterDotUnstuffing(t *testing.T) {
	const wire = "..hidden\r\n..\r\n\r\nplain\r\n...two\r\n"
	const content = ".hidden\r\n.\r\n\r\nplain\r\n..two\r\n"
	long := strings.Repeat("x", 200_000) + "\r\n"
	srv := &news{greeting: "200 hi", bodies: map[string]string{"<m1@example>": wire, "<m2@example>": long}}
	ln := listen(t)
	srv.serve(t, ln)
	cfg, target := usenetConfig(t, ln, mixedNZB)
	cfg.Usenet.Username, cfg.Usenet.Password = "user", "pass"

	tl, err := newProber(cfg).Sample(t.Context(), config.LayerUsenet, target, planAt(0.2))
	if err != nil {
		t.Fatal(err)
	}
	checkTimelineInvariants(t, tl)
	if want := []string{"AUTHINFO USER user", "AUTHINFO PASS pass", "BODY <m1@example>", "BODY <m2@example>", "QUIT"}; !slices.Equal(srv.saw(), want) {
		t.Errorf("commands %q, want %q", srv.saw(), want)
	}
	if len(tl.Ops) != 4 || tl.Ops[1].Kind != results.OpAuth {
		t.Fatalf("ops %+v, want connect, auth and two articles", tl.Ops)
	}
	if got := tl.Ops[2].Bytes; got != int64(len(content)) {
		t.Errorf("stuffed article: %d bytes, want the %d of its content", got, len(content))
	}
	if got := tl.Ops[3].Bytes; got != int64(len(long)) {
		t.Errorf("long-line article: %d bytes, want %d", got, len(long))
	}
}

func TestUsenetMissingArticleFailsItsPhaseAndKeepsEarlierOnes(t *testing.T) {
	srv := &news{greeting: "200 hi", bodies: map[string]string{"<m1@example>": "one\r\n"}}
	ln := listen(t)
	srv.serve(t, ln)
	cfg, target := usenetConfig(t, ln, mixedNZB)

	tl, err := newProber(cfg).Sample(t.Context(), config.LayerUsenet, target, planAt(0.2, 0.9))
	if err == nil {
		t.Fatal("sample succeeded although the provider lacks an article")
	}
	checkTimelineInvariants(t, tl)
	for _, want := range []string{`"seek 20%"`, "<m2@example>", "430"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
	if len(tl.Marks) != 1 {
		t.Errorf("%d marks, want startup only", len(tl.Marks))
	}
	if last := tl.Ops[len(tl.Ops)-1]; last.Kind != results.OpArticle || !strings.Contains(last.Err, "430") {
		t.Errorf("last op %+v, want the failed article", last)
	}
	if got := srv.saw(); slices.Contains(got, "BODY <m3@example>") {
		t.Errorf("went on to later phases after a failure: %q", got)
	}
}

func TestUsenetTimeoutInterruptsAStalledArticle(t *testing.T) {
	srv := &news{greeting: "200 hi", stall: "<m1@example>"}
	ln := listen(t)
	srv.serve(t, ln)
	cfg, target := usenetConfig(t, ln, mixedNZB)
	cfg.Profile.Timeout = config.Duration(300 * time.Millisecond)

	start := time.Now()
	tl, err := newProber(cfg).Sample(t.Context(), config.LayerUsenet, target, planAt())
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("sample returned after %v with a 300ms timeout", took)
	}
	if err == nil || !strings.Contains(err.Error(), `timeout after 300ms in phase "startup"`) {
		t.Errorf("err = %v, want a timeout in startup", err)
	}
	checkTimelineInvariants(t, tl)
	if len(tl.Marks) != 0 {
		t.Errorf("marked a phase whose article never ended: %+v", tl.Marks)
	}
	if last := tl.Ops[len(tl.Ops)-1]; last.Kind != results.OpArticle || last.Err == "" || last.FirstByte == 0 {
		t.Errorf("last op %+v, want the article that started arriving and then failed", last)
	}
}

func TestUsenetRejectsBadInput(t *testing.T) {
	for name, tc := range map[string]struct{ greeting, nzb, want string }{
		"service unavailable greeting": {"400 go away", mixedNZB, "400 go away"},
		"nzb without segments":         {"200 hi", `<nzb><file subject="x"><segments></segments></file></nzb>`, "no file with segments"},
		"nzb that is not xml":          {"200 hi", "this is not an nzb", "nzb"},
		"segment without message-id":   {"200 hi", `<nzb><file subject="x"><segments><segment bytes="1" number="1"> </segment></segments></file></nzb>`, "message-id"},
	} {
		t.Run(name, func(t *testing.T) {
			ln := listen(t)
			(&news{greeting: tc.greeting}).serve(t, ln)
			cfg, target := usenetConfig(t, ln, tc.nzb)
			tl, err := newProber(cfg).Sample(t.Context(), config.LayerUsenet, target, planAt())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
			checkTimelineInvariants(t, tl)
			if len(tl.Marks) != 0 {
				t.Errorf("marks %+v after a failed session", tl.Marks)
			}
		})
	}
}

func TestUsenetConnectsOverVerifiedTLS(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "news.test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	roots := x509.NewCertPool()
	roots.AddCert(cert)

	srv := &news{greeting: "200 hi over tls", bodies: map[string]string{"<m1@example>": "one\r\n"}}
	ln := tls.NewListener(listen(t), &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	})
	srv.serve(t, ln)
	cfg, target := usenetConfig(t, ln, mixedNZB)
	cfg.Usenet.TLS = true

	trusting := newProber(cfg)
	trusting.TrustNNTPRoots(roots)
	tl, err := trusting.Sample(t.Context(), config.LayerUsenet, target, planAt())
	if err != nil {
		t.Fatalf("sample over TLS: %v", err)
	}
	if len(tl.Marks) != 1 || !slices.Contains(srv.saw(), "BODY <m1@example>") {
		t.Errorf("marks %+v, commands %q: want the article fetched over TLS", tl.Marks, srv.saw())
	}

	tl, err = newProber(cfg).Sample(t.Context(), config.LayerUsenet, target, planAt())
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("err = %v, want the untrusted certificate rejected", err)
	}
	if len(tl.Ops) != 1 || tl.Ops[0].Kind != results.OpConnect || tl.Ops[0].Err == "" {
		t.Errorf("ops %+v, want one failed connect", tl.Ops)
	}
}

func TestUsenetFailsAnOverlongStatusLineWithoutBufferingIt(t *testing.T) {
	const sent = 4 << 20
	ln := listen(t)
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	greeting := append([]byte("200 "), bytes.Repeat([]byte("A"), sent)...)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write(greeting)
		<-hold
	}()
	cfg, target := usenetConfig(t, ln, mixedNZB)
	cfg.Profile.Timeout = config.Duration(5 * time.Second)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	_, err := newProber(cfg).Sample(t.Context(), config.LayerUsenet, target, planAt())
	took := time.Since(start)
	runtime.ReadMemStats(&after)

	if err == nil || !strings.Contains(err.Error(), "longer than 512 bytes") {
		t.Errorf("err = %v, want the greeting rejected as longer than RFC 3977's 512 bytes", err)
	}
	if took > time.Second {
		t.Errorf("sample took %s; an over-long line must fail when its 512th byte arrives, not at the 5s timeout", took)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 1<<20 {
		t.Errorf("reading one status line allocated %d KiB while the server sent %d KiB", alloc>>10, sent>>10)
	}
}
