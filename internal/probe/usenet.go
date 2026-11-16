package probe

import (
	"context"

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
// cumulative byte offset.
func (p *Prober) sampleUsenet(ctx context.Context, rec *recorder, t config.Target, plan results.Plan) error {
	// TODO: nzb, err := loadNZB(t.NZB); f := nzb.largest(); flow above with
	//   net/textproto on a tls.Conn or net.Conn; any non-2xx/3xx code → op.Err,
	//   return error (phases already marked still count).
	panic("not implemented")
}

// nzbFile is the part of an NZB we use: one file's segments in order.
type nzbFile struct {
	Subject  string
	Segments []nzbSegment // sorted by Number
}

type nzbSegment struct {
	Number    int
	Bytes     int64
	MessageID string // without angle brackets
	Offset    int64  // cumulative bytes of earlier segments
}

// loadNZB parses an .nzb (encoding/xml) into files.
func loadNZB(path string) ([]nzbFile, error) { panic("not implemented") }

// largest returns the file with the most bytes: the media itself, assuming
// the release is not split into RAR volumes (see RATIONALE open questions).
func largest(files []nzbFile) nzbFile { panic("not implemented") }

// segmentAt returns the first segment whose byte range contains
// fraction*total.
func (f nzbFile) segmentAt(fraction float64) nzbSegment { panic("not implemented") }
