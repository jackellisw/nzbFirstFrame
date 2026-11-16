package fakestack_test

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jackellisw/nzbfirstframe/internal/config"
	"github.com/jackellisw/nzbfirstframe/internal/fakestack"
)

// syntheticMedia writes n pseudo-random bytes: the servers only move bytes,
// so most tests need no encoder.
func syntheticMedia(t *testing.T, n int) (path string, data []byte) {
	t.Helper()
	data = make([]byte, n)
	rng := rand.NewChaCha8([32]byte{1})
	rng.Read(data)
	path = filepath.Join(t.TempDir(), "synthetic.mkv")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, data
}

func start(t *testing.T, opts fakestack.Options) *fakestack.Stack {
	t.Helper()
	st, err := fakestack.Start(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func targetURL(st *fakestack.Stack) string {
	segments := strings.Split(st.Target.Path, "/")
	for i := range segments {
		segments[i] = url.PathEscape(segments[i])
	}
	return st.InfinidyskURL + "/" + strings.Join(segments, "/")
}

func do(t *testing.T, method, url string, edit func(*http.Request)) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func lastHit(t *testing.T, st *fakestack.Stack) fakestack.Hit {
	t.Helper()
	hits := st.Hits()
	if len(hits) == 0 {
		t.Fatal("no hit logged")
	}
	return hits[len(hits)-1]
}

func TestInfinidyskServesRangesBehindBasicAuthAfterTTFB(t *testing.T) {
	const ttfb = 100 * time.Millisecond
	media, data := syntheticMedia(t, 300_000)
	st := start(t, fakestack.Options{Media: media, InfinidyskTTFB: ttfb})

	if resp, _ := do(t, "GET", targetURL(st), nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no credentials: status %d, want 401", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", targetURL(st), func(r *http.Request) { r.SetBasicAuth(st.User, "nope") }); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong password: status %d, want 401", resp.StatusCode)
	}
	if hits := st.Hits(); len(hits) != 0 {
		t.Errorf("rejected requests logged as hits: %+v", hits)
	}

	sent := time.Now()
	resp, body := do(t, "GET", targetURL(st), func(r *http.Request) {
		r.SetBasicAuth(st.User, st.Pass)
		r.Header.Set("Range", "bytes=1000-200999")
	})
	if took := time.Since(sent); took < ttfb {
		t.Errorf("answered in %v, before the injected %v", took, ttfb)
	}
	if resp.StatusCode != http.StatusPartialContent || resp.Header.Get("Content-Range") != "bytes 1000-200999/300000" {
		t.Errorf("status %d, Content-Range %q", resp.StatusCode, resp.Header.Get("Content-Range"))
	}
	if !bytes.Equal(body, data[1000:201000]) {
		t.Errorf("body is not bytes 1000-200999 of the media (%d bytes)", len(body))
	}
	if hit := lastHit(t, st); hit.Layer != config.LayerInfinidysk || hit.Ref != "bytes=1000-200999" || hit.Bytes != 200_000 || hit.At.Before(sent) {
		t.Errorf("hit %+v, want infinidysk bytes=1000-200999 with 200000 bytes", hit)
	}

	if resp, _ := do(t, "GET", st.InfinidyskURL+"/movies/none.mkv", func(r *http.Request) { r.SetBasicAuth(st.User, st.Pass) }); resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing file: status %d, want 404", resp.StatusCode)
	}
}

func TestJellyfinServesStaticStreamOnlyWithTokenHeader(t *testing.T) {
	const ttfb = 100 * time.Millisecond
	media, data := syntheticMedia(t, 100_000)
	st := start(t, fakestack.Options{Media: media, JellyfinTTFB: ttfb})
	stream := st.JellyfinURL + "/Videos/" + st.Target.JellyfinItemID + "/stream"
	token := func(r *http.Request) { r.Header.Set("X-Emby-Token", st.APIKey) }

	for name, tc := range map[string]struct {
		url    string
		edit   func(*http.Request)
		status int
	}{
		"no token":           {stream + "?static=true", nil, 401},
		"token in the query": {stream + "?static=true&api_key=" + st.APIKey, nil, 401},
		"transcode":          {stream, token, 400},
		"unknown item":       {st.JellyfinURL + "/Videos/other/stream?static=true", token, 404},
	} {
		if resp, _ := do(t, "GET", tc.url, tc.edit); resp.StatusCode != tc.status {
			t.Errorf("%s: status %d, want %d", name, resp.StatusCode, tc.status)
		}
	}

	sent := time.Now()
	resp, body := do(t, "GET", stream+"?static=true", func(r *http.Request) {
		token(r)
		r.Header.Set("Range", "bytes=99000-")
	})
	if took := time.Since(sent); took < ttfb {
		t.Errorf("answered in %v, before the injected %v", took, ttfb)
	}
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(body, data[99000:]) {
		t.Errorf("status %d with %d bytes, want 206 with the last 1000", resp.StatusCode, len(body))
	}
	if hit := lastHit(t, st); hit.Layer != config.LayerJellyfin || hit.Ref != "bytes=99000-" || hit.Bytes != 1000 {
		t.Errorf("hit %+v, want jellyfin bytes=99000- with 1000 bytes", hit)
	}
}

func TestMountDirHoldsTheMediaAtTargetPath(t *testing.T) {
	media, data := syntheticMedia(t, 10_000)
	st := start(t, fakestack.Options{Media: media})
	got, err := os.ReadFile(filepath.Join(st.MountDir, filepath.FromSlash(st.Target.Path)))
	if err != nil || !bytes.Equal(got, data) {
		t.Errorf("mount file: %v, %d bytes; want the media", err, len(got))
	}
}

func TestRCAnswersForgetAndLogsIt(t *testing.T) {
	media, _ := syntheticMedia(t, 1000)
	st := start(t, fakestack.Options{Media: media})
	post := func(path, body, pass string) (*http.Response, []byte) {
		return do(t, "POST", st.RCURL+path, func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.SetBasicAuth(st.User, pass)
		})
	}

	if resp, _ := post("/vfs/forget", `{}`, "nope"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong password: status %d, want 401", resp.StatusCode)
	}
	resp, body := post("/vfs/forget", `{"file":"movies/a.mkv"}`, st.Pass)
	var ok struct{ Forgotten []string }
	if resp.StatusCode != 200 || json.Unmarshal(body, &ok) != nil || len(ok.Forgotten) != 1 || ok.Forgotten[0] != "movies/a.mkv" {
		t.Errorf("vfs/forget: status %d body %s", resp.StatusCode, body)
	}
	if hit := lastHit(t, st); hit.Layer != config.LayerRclone || hit.Ref != "vfs/forget file=movies/a.mkv" {
		t.Errorf("hit %+v, want the forget call", hit)
	}

	resp, body = post("/vfs/refresh", `{}`, st.Pass)
	var failure struct{ Error string }
	if resp.StatusCode != 404 || json.Unmarshal(body, &failure) != nil || failure.Error == "" {
		t.Errorf("unknown method: status %d body %s, want 404 with an error field", resp.StatusCode, body)
	}
}

type nzbDoc struct {
	Files []struct {
		Subject  string `xml:"subject,attr"`
		Segments []struct {
			Bytes  int64  `xml:"bytes,attr"`
			Number int    `xml:"number,attr"`
			ID     string `xml:",chardata"`
		} `xml:"segments>segment"`
	} `xml:"file"`
}

// nntpClient is a line-level client for the test's own reading of the
// protocol.
type nntpClient struct {
	conn net.Conn
	r    *bufio.Reader
}

func (c *nntpClient) status(t *testing.T) string {
	t.Helper()
	line, err := c.r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimRight(line, "\r\n")
}

func (c *nntpClient) cmd(t *testing.T, line string) string {
	t.Helper()
	fmt.Fprintf(c.conn, "%s\r\n", line)
	return c.status(t)
}

// body reads a multi-line block and undoes dot-stuffing.
func (c *nntpClient) body(t *testing.T) []string {
	t.Helper()
	var lines []string
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		line = strings.TrimSuffix(line, "\r\n")
		if line == "." {
			return lines
		}
		lines = append(lines, strings.TrimPrefix(line, "."))
	}
}

// ydecode decodes one yEnc part and checks it against its own trailer.
func ydecode(t *testing.T, lines []string) (begin int64, data []byte) {
	t.Helper()
	if len(lines) < 3 || !strings.HasPrefix(lines[0], "=ybegin ") || !strings.HasPrefix(lines[1], "=ypart ") {
		t.Fatalf("article does not start with =ybegin/=ypart: %q", lines[:min(len(lines), 2)])
	}
	var end int64
	if _, err := fmt.Sscanf(lines[1], "=ypart begin=%d end=%d", &begin, &end); err != nil {
		t.Fatal(err)
	}
	trailer := lines[len(lines)-1]
	for _, line := range lines[2 : len(lines)-1] {
		if len(line) > 129 {
			t.Fatalf("yEnc line of %d characters, want at most 128 (+1 for a trailing escape)", len(line))
		}
		for i := 0; i < len(line); i++ {
			c := line[i]
			if c == '=' {
				i++
				c = line[i] - 64
			}
			data = append(data, c-42)
		}
	}
	var size, part int
	var crc uint32
	if _, err := fmt.Sscanf(trailer, "=yend size=%d part=%d pcrc32=%x", &size, &part, &crc); err != nil {
		t.Fatalf("trailer %q: %v", trailer, err)
	}
	if size != len(data) || end-begin+1 != int64(len(data)) || crc != crc32.ChecksumIEEE(data) {
		t.Fatalf("part %d: decoded %d bytes, headers say size %d, range %d-%d, crc %08x", part, len(data), size, begin, end, crc)
	}
	return begin - 1, data
}

func TestNNTPServesTheMediaAsTheArticlesTheNZBLists(t *testing.T) {
	const (
		ttfb    = 80 * time.Millisecond
		segment = 100_000
	)
	media, data := syntheticMedia(t, 1_234_567)
	st := start(t, fakestack.Options{Media: media, ArticleTTFB: ttfb, SegmentSize: segment})

	raw, err := os.ReadFile(st.Target.NZB)
	if err != nil {
		t.Fatal(err)
	}
	var nzb nzbDoc
	if err := xml.Unmarshal(raw, &nzb); err != nil {
		t.Fatalf("NZB does not parse: %v", err)
	}
	if len(nzb.Files) != 2 || len(nzb.Files[0].Segments) != 1 || len(nzb.Files[1].Segments) != 13 {
		t.Fatalf("NZB lists %d files; want a one-segment extra first, then the media in 13 segments", len(nzb.Files))
	}

	conn, err := net.Dial("tcp", st.NNTPAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := &nntpClient{conn: conn, r: bufio.NewReader(conn)}
	first := "<" + nzb.Files[1].Segments[0].ID + ">"

	if got := c.status(t); !strings.HasPrefix(got, "200") {
		t.Fatalf("greeting %q", got)
	}
	if got := c.cmd(t, "BODY "+first); !strings.HasPrefix(got, "480") {
		t.Errorf("BODY before AUTHINFO: %q, want 480", got)
	}
	if got := c.cmd(t, "AUTHINFO USER "+st.User); !strings.HasPrefix(got, "381") {
		t.Errorf("AUTHINFO USER: %q, want 381", got)
	}
	if got := c.cmd(t, "AUTHINFO PASS nope"); !strings.HasPrefix(got, "481") {
		t.Errorf("wrong password: %q, want 481", got)
	}
	c.cmd(t, "AUTHINFO USER "+st.User)
	if got := c.cmd(t, "AUTHINFO PASS "+st.Pass); !strings.HasPrefix(got, "281") {
		t.Fatalf("AUTHINFO PASS: %q, want 281", got)
	}
	if got := c.cmd(t, "BODY <nobody@nowhere>"); !strings.HasPrefix(got, "430") {
		t.Errorf("unknown article: %q, want 430", got)
	}

	var rebuilt []byte
	var listed int64
	for i, seg := range nzb.Files[1].Segments {
		if seg.Number != i+1 {
			t.Fatalf("segment %d numbered %d", i+1, seg.Number)
		}
		sent := time.Now()
		if got := c.cmd(t, "BODY <"+seg.ID+">"); !strings.HasPrefix(got, "222") {
			t.Fatalf("BODY <%s>: %q", seg.ID, got)
		}
		statusAfter := time.Since(sent)
		if _, err := c.r.Peek(1); err != nil {
			t.Fatal(err)
		}
		if bodyAfter := time.Since(sent); statusAfter >= ttfb || bodyAfter < ttfb {
			t.Errorf("segment %d: status after %v, body after %v; want the %v between them", seg.Number, statusAfter, bodyAfter, ttfb)
		}
		lines := c.body(t)
		off, part := ydecode(t, lines)
		if off != int64(len(rebuilt)) || int64(len(part)) != seg.Bytes {
			t.Fatalf("segment %d: carries %d bytes from offset %d; NZB says %d bytes at %d", seg.Number, len(part), off, seg.Bytes, len(rebuilt))
		}
		rebuilt = append(rebuilt, part...)
		listed += seg.Bytes

		wire := 0
		for _, l := range lines {
			wire += len(l) + 2
		}
		if hit := lastHit(t, st); hit.Layer != config.LayerUsenet || hit.Ref != "<"+seg.ID+">" || hit.Bytes != int64(wire) {
			t.Errorf("hit %+v, want usenet <%s> with the article's %d bytes", hit, seg.ID, wire)
		}
	}
	if listed != int64(len(data)) || sha256.Sum256(rebuilt) != sha256.Sum256(data) {
		t.Errorf("articles decode to %d bytes (NZB lists %d); want the media's %d, byte for byte", len(rebuilt), listed, len(data))
	}
	if got := c.cmd(t, "QUIT"); !strings.HasPrefix(got, "205") {
		t.Errorf("QUIT: %q, want 205", got)
	}
}

func TestConfigMeasuresAllFourLayersAndSurvivesYAML(t *testing.T) {
	media, _ := syntheticMedia(t, 1000)
	dir := t.TempDir()
	st := start(t, fakestack.Options{Media: media, Dir: dir})
	cfg := st.Config()

	for _, l := range config.AllLayers {
		if !cfg.HasLayer(l) {
			t.Errorf("config does not measure %s", l)
		}
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("config is not valid: %v", err)
	}
	if len(cfg.Targets) != 1 || cfg.Targets[0] != st.Target || st.Target.Name != "fake" {
		t.Errorf("targets %+v, want only the stack's target %+v", cfg.Targets, st.Target)
	}
	if rel, err := filepath.Rel(dir, cfg.Results.Dir); err != nil || strings.HasPrefix(rel, "..") {
		t.Errorf("results dir %s is not under %s", cfg.Results.Dir, dir)
	}

	// `nzbff dev-stack` prints this config for `nzbff run -c`.
	text, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Parse(text)
	if err != nil {
		t.Fatalf("printed config does not load: %v\n%s", err, text)
	}
	if err := loaded.Finalize(); err != nil {
		t.Fatalf("printed config does not validate: %v", err)
	}
	if loaded.Jellyfin != cfg.Jellyfin || loaded.Rclone != cfg.Rclone || loaded.Infinidysk != cfg.Infinidysk ||
		loaded.Usenet != cfg.Usenet || loaded.Targets[0] != cfg.Targets[0] || loaded.Results != cfg.Results {
		t.Errorf("config changed across YAML:\n%s", text)
	}
}

func TestCloseStopsServersAndRemovesItsTempDir(t *testing.T) {
	media, _ := syntheticMedia(t, 1000)
	st, err := fakestack.Start(t.Context(), fakestack.Options{Media: media})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if _, err := os.Stat(st.MountDir); !os.IsNotExist(err) {
		t.Errorf("temp dir still there after Close: %v", err)
	}
	for name, addr := range map[string]string{
		"jellyfin":   strings.TrimPrefix(st.JellyfinURL, "http://"),
		"infinidysk": strings.TrimPrefix(st.InfinidyskURL, "http://"),
		"rc":         strings.TrimPrefix(st.RCURL, "http://"),
		"nntp":       st.NNTPAddr,
	} {
		if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			conn.Close()
			t.Errorf("%s still accepts connections after Close", name)
		}
	}
}

func needTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
}

func ffprobe(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "ffprobe", append([]string{"-v", "error"}, args...)...).Output()
	if err != nil {
		t.Fatalf("ffprobe %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func TestGenerateEncodesTheSpecWithTheIndexAtTheEnd(t *testing.T) {
	needTools(t)
	const length, gop = 8 * time.Second, 48
	for container, indexAfterData := range map[string]func(*testing.T, string){"mkv": cuesFollowClusters, "mp4": moovFollowsMdat} {
		t.Run(container, func(t *testing.T) {
			spec := fakestack.MediaSpec{Container: container, Length: length, GOP: gop}
			path, err := fakestack.Generate(t.Context(), "ffmpeg", t.TempDir(), spec)
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Ext(path) != "."+container {
				t.Errorf("generated %s, want a .%s file", path, container)
			}
			if got := ffprobe(t, "-select_streams", "v:0", "-show_entries", "stream=codec_name,width,height", "-of", "csv=p=0", path); got != "h264,1280,720" {
				t.Errorf("video stream is %q, want h264,1280,720", got)
			}
			seconds, _ := strconv.ParseFloat(ffprobe(t, "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", path), 64)
			if d := time.Duration(seconds * float64(time.Second)); (d - length).Abs() > 100*time.Millisecond {
				t.Errorf("duration %v, want %v", d, length)
			}
			var keyframes []int
			for i, flags := range strings.Fields(ffprobe(t, "-select_streams", "v:0", "-show_entries", "packet=flags", "-of", "csv=p=0", path)) {
				if strings.HasPrefix(flags, "K") {
					keyframes = append(keyframes, i)
				}
			}
			if want := []int{0, 48, 96, 144}; fmt.Sprint(keyframes) != fmt.Sprint(want) {
				t.Errorf("keyframes at packets %v, want one every %d: %v", keyframes, gop, want)
			}
			indexAfterData(t, path)

			// Cached: the second call must not need an encoder at all.
			again, err := fakestack.Generate(t.Context(), filepath.Join(t.TempDir(), "no-ffmpeg"), t.TempDir(), spec)
			if err != nil {
				t.Fatalf("second Generate for the same spec ran the encoder again: %v", err)
			}
			a, _ := os.ReadFile(path)
			b, _ := os.ReadFile(again)
			if !bytes.Equal(a, b) {
				t.Error("second Generate for the same spec returned different media")
			}
		})
	}

	if _, err := fakestack.Generate(t.Context(), "ffmpeg", t.TempDir(), fakestack.MediaSpec{Container: "avi"}); err == nil {
		t.Error("generated an unsupported container")
	}
}

// moovFollowsMdat walks the top-level MP4 boxes.
func moovFollowsMdat(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var order []string
	for off := int64(0); ; {
		var head [16]byte
		if _, err := f.ReadAt(head[:8], off); err != nil {
			break
		}
		size := int64(binary.BigEndian.Uint32(head[:4]))
		if size == 1 {
			f.ReadAt(head[8:], off+8)
			size = int64(binary.BigEndian.Uint64(head[8:]))
		}
		order = append(order, string(head[4:8]))
		if size < 8 {
			break
		}
		off += size
	}
	got := strings.Join(order, " ")
	if mdat, moov := strings.Index(got, "mdat"), strings.Index(got, "moov"); mdat < 0 || moov < mdat {
		t.Errorf("MP4 boxes are %q, want moov after mdat (no faststart)", got)
	}
}

// cuesFollowClusters walks the children of the Matroska Segment.
func cuesFollowClusters(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// An EBML element is an id (length from its first byte's leading zero
	// bits, marker kept) then a size (same scheme, marker dropped).
	element := func(at int) (id uint32, payload, size int) {
		idLen := 1
		for data[at]&(0x80>>(idLen-1)) == 0 {
			idLen++
		}
		for _, b := range data[at : at+idLen] {
			id = id<<8 | uint32(b)
		}
		at += idLen
		sizeLen := 1
		for data[at]&(0x80>>(sizeLen-1)) == 0 {
			sizeLen++
		}
		size = int(data[at] & (0xff >> sizeLen))
		for _, b := range data[at+1 : at+sizeLen] {
			size = size<<8 | int(b)
		}
		return id, at + sizeLen, size
	}
	const idHeader, idSegment, idCluster, idCues = 0x1A45DFA3, 0x18538067, 0x1F43B675, 0x1C53BB6B

	id, payload, size := element(0)
	if id != idHeader {
		t.Fatalf("file starts with element %#x, want the EBML header", id)
	}
	id, at, _ := element(payload + size)
	if id != idSegment {
		t.Fatalf("second element is %#x, want Segment", id)
	}
	firstCluster, lastCluster, cues := -1, -1, -1
	for at < len(data) {
		id, payload, size := element(at)
		switch id {
		case idCluster:
			if firstCluster < 0 {
				firstCluster = at
			}
			lastCluster = at
		case idCues:
			cues = at
		}
		at = payload + size
	}
	if firstCluster < 0 || cues < lastCluster {
		t.Errorf("Cues at byte %d, clusters span %d-%d of %d: want the Cues after the last cluster", cues, firstCluster, lastCluster, len(data))
	}
}

func TestStartGeneratesMediaFromSpec(t *testing.T) {
	needTools(t)
	st := start(t, fakestack.Options{Spec: fakestack.MediaSpec{Container: "mp4", Length: 8 * time.Second}})
	if filepath.Ext(st.Target.Path) != ".mp4" || st.Duration != 8*time.Second {
		t.Errorf("target %s with duration %v, want an 8s .mp4", st.Target.Path, st.Duration)
	}
	resp, body := do(t, "GET", targetURL(st), func(r *http.Request) {
		r.SetBasicAuth(st.User, st.Pass)
		r.Header.Set("Range", "bytes=4-7")
	})
	if resp.StatusCode != http.StatusPartialContent || string(body) != "ftyp" {
		t.Errorf("infinidysk serves %q at bytes 4-7 (status %d), want the MP4's ftyp box", body, resp.StatusCode)
	}
}
