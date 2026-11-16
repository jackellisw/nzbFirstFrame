package results

import (
	"errors"
	"fmt"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

// OpKind says which wire operation an Op records.
type OpKind string

const (
	OpRead    OpKind = "read"    // one HTTP Range request served by the local tap
	OpConnect OpKind = "connect" // NNTP TCP (+TLS) connect up to the server greeting
	OpAuth    OpKind = "auth"    // NNTP AUTHINFO USER/PASS exchange
	OpArticle OpKind = "article" // NNTP BODY for one segment
)

// Op is one upstream operation. All instants are offsets from the sample's
// clock origin.
//
// Invariants (enforced by the recorder in package probe, relied on by Analyze):
//   - Start <= End.
//   - FirstByte == 0 means no payload byte ever arrived; otherwise
//     Start < FirstByte <= End. TTFB is derived as FirstByte-Start.
//   - Bytes counts payload delivered to the client (ffprobe, or our NNTP
//     reader), including read-ahead the client later abandons.
type Op struct {
	Kind      OpKind        `json:"kind"`
	Start     time.Duration `json:"start_ns"`
	FirstByte time.Duration `json:"first_byte_ns,omitempty"`
	End       time.Duration `json:"end_ns"`

	// Offset is the byte offset in the media file the op starts at: the
	// Range start for reads, the segment's cumulative offset for articles.
	// -1 for connect/auth.
	Offset int64 `json:"offset"`
	Bytes  int64 `json:"bytes"`

	// Dial is the part of the wait spent opening a new upstream connection
	// (TCP + TLS); 0 when a pooled connection was reused. Reads only.
	Dial time.Duration `json:"dial_ns,omitempty"`

	// Abandoned is set when the client closed the response before the
	// requested range was fully delivered (ffprobe seeking away).
	Abandoned bool `json:"abandoned,omitempty"`

	// Ref is a human-readable handle for the request, for display only:
	// "bytes=93143794-" or "<part12@example>".
	Ref string `json:"ref,omitempty"`
	Err string `json:"err,omitempty"`
}

// Mark closes a phase: the instant the client had what that phase asked for.
//
// Invariant: within a Timeline, Marks[i].Phase == i (a phase is marked at most
// once, in plan order) and At is strictly increasing. A missing tail of marks
// means the sample failed partway; the phases that were marked still count.
type Mark struct {
	Phase int           `json:"phase"`
	At    time.Duration `json:"at_ns"`
	// PTS is the presentation time (seconds) of the frame ffprobe reported,
	// used to check that a seek landed near its target. NaN-free: -1 when the
	// layer has no PTS (usenet) or ffprobe printed N/A.
	PTS float64 `json:"pts"`
}

// Timeline is everything one sample observed.
//
// Invariant: Ops are sorted by Start (the recorder appends on completion and
// sorts on snapshot); Marks follow the Mark invariant.
type Timeline struct {
	Ops   []Op          `json:"ops"`
	Marks []Mark        `json:"marks"`
	End   time.Duration `json:"end_ns"` // client exited / connection closed
}

// Seek is one resolved seek point.
type Seek struct {
	Point    string        `json:"point"`     // as configured: "50%", "00:42:00"
	Offset   time.Duration `json:"offset_ns"` // absolute position in the media
	Fraction float64       `json:"fraction"`  // Offset/duration, 0..1; used by the usenet layer
}

// Plan is the ordered list of phases every sample of a target runs, in one
// client session: phase 0 is always startup (the open), phase k>0 is
// Seeks[k-1]. Startup is not optional because every seek session begins with
// an open, and recording it is free.
type Plan struct {
	Seeks []Seek `json:"seeks"`
}

// Len is the number of phases including startup.
func (p Plan) Len() int { return 1 + len(p.Seeks) }

// Label names phase i for display: "startup", "seek 50%".
func (p Plan) Label(i int) string {
	if i == 0 {
		return "startup"
	}
	return "seek " + p.Seeks[i-1].Point
}

// ErrNoDuration is returned by NewPlan when seeks are requested but the media
// duration could not be resolved.
var ErrNoDuration = errors.New("media duration unknown; cannot resolve seek points")

// NewPlan resolves seek points against the media duration. It is the only
// constructor of Plan, so every Seek has Offset and Fraction consistent with
// one duration. Points past the end are clamped to duration-1s (ffprobe
// returns no frame past EOF, which would read as a failure).
//
// duration <= 0 means unknown. Percent points then get Fraction only
// (Offset 0), which is all the usenet layer needs; an absolute point with an
// unknown duration is ErrNoDuration.
func NewPlan(points []config.SeekPoint, duration time.Duration) (Plan, error) {
	plan := Plan{Seeks: make([]Seek, 0, len(points))}
	for _, p := range points {
		s := Seek{Point: p.String()}
		switch {
		case duration > 0:
			s.Offset = min(p.Resolve(duration), max(duration-time.Second, 0))
			s.Fraction = float64(s.Offset) / float64(duration)
		case p.IsPercent:
			s.Fraction = p.Percent / 100
		default:
			return Plan{}, fmt.Errorf("seek point %q: %w", p, ErrNoDuration)
		}
		plan.Seeks = append(plan.Seeks, s)
	}
	return plan, nil
}
