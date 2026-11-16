package probe

import (
	"cmp"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"unicode/utf8"
)

// nzbFile is the part of an NZB we use: one file's segments in order.
type nzbFile struct {
	Subject  string
	Segments []nzbSegment // sorted by Number, never empty
}

type nzbSegment struct {
	Number    int
	Bytes     int64
	MessageID string // without angle brackets
	Offset    int64  // cumulative bytes of earlier segments
}

// size is the file's total article bytes.
func (f nzbFile) size() int64 {
	last := f.Segments[len(f.Segments)-1]
	return last.Offset + last.Bytes
}

// loadNZB parses an .nzb (encoding/xml) into its files that have segments.
// It returns at least one file or an error.
func loadNZB(path string) ([]nzbFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("nzb: %w", err)
	}
	defer f.Close()

	var doc struct {
		Files []struct {
			Subject  string `xml:"subject,attr"`
			Segments []struct {
				Number int    `xml:"number,attr"`
				Bytes  int64  `xml:"bytes,attr"`
				ID     string `xml:",chardata"`
			} `xml:"segments>segment"`
		} `xml:"file"`
	}
	dec := xml.NewDecoder(f)
	dec.CharsetReader = nzbCharset
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("nzb %s: %w", path, err)
	}

	var files []nzbFile
	for _, xf := range doc.Files {
		out := nzbFile{Subject: xf.Subject}
		for _, xs := range xf.Segments {
			id := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(xs.ID), "<"), ">")
			if id == "" || xs.Bytes < 0 {
				return nil, fmt.Errorf("nzb %s: file %q: segment %d has no message-id or a negative size", path, xf.Subject, xs.Number)
			}
			out.Segments = append(out.Segments, nzbSegment{Number: xs.Number, Bytes: xs.Bytes, MessageID: id})
		}
		slices.SortStableFunc(out.Segments, func(a, b nzbSegment) int { return cmp.Compare(a.Number, b.Number) })
		// Indexers sometimes list the same part twice (reposts); the first
		// stands for the part.
		out.Segments = slices.CompactFunc(out.Segments, func(a, b nzbSegment) bool { return a.Number == b.Number })
		var offset int64
		for i := range out.Segments {
			out.Segments[i].Offset = offset
			offset += out.Segments[i].Bytes
		}
		if len(out.Segments) > 0 {
			files = append(files, out)
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("nzb %s: no file with segments", path)
	}
	return files, nil
}

// nzbCharset lets encoding/xml read the ISO-8859-1 declaration older
// indexers put on their NZBs; it only understands UTF-8 by itself.
func nzbCharset(label string, in io.Reader) (io.Reader, error) {
	switch strings.ToLower(label) {
	case "iso-8859-1", "latin1", "us-ascii":
		return &latin1Reader{in: in}, nil
	}
	return nil, fmt.Errorf("unsupported charset %q", label)
}

type latin1Reader struct {
	in  io.Reader
	out []byte // decoded UTF-8 not yet handed out
}

func (l *latin1Reader) Read(p []byte) (int, error) {
	if len(l.out) == 0 {
		raw := make([]byte, 4<<10)
		n, err := l.in.Read(raw)
		for _, b := range raw[:n] {
			l.out = utf8.AppendRune(l.out, rune(b))
		}
		if n == 0 {
			return 0, err
		}
	}
	n := copy(p, l.out)
	l.out = l.out[n:]
	return n, nil
}

// largest returns the file with the most bytes: the media itself, assuming
// the release is not split into RAR volumes (see RATIONALE open questions).
func largest(files []nzbFile) nzbFile {
	return slices.MaxFunc(files, func(a, b nzbFile) int { return cmp.Compare(a.size(), b.size()) })
}

// segmentAt returns the first segment whose byte range contains
// fraction*total, or the last segment when the fraction points at the end.
func (f nzbFile) segmentAt(fraction float64) nzbSegment {
	target := int64(fraction * float64(f.size()))
	for _, s := range f.Segments {
		if target < s.Offset+s.Bytes {
			return s
		}
	}
	return f.Segments[len(f.Segments)-1]
}
