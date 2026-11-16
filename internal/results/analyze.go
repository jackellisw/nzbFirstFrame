package results

import (
	"slices"
	"time"
)

// PhaseResult is the derived view of one phase of one sample.
type PhaseResult struct {
	Phase int  `json:"phase"`
	OK    bool `json:"ok"` // the phase was marked

	// Latency is the phase's wall time on the sample clock:
	//   startup:  Marks[0].At - Start of the first op attributed to phase 0
	//             (the client's first upstream request, not process spawn:
	//             ffprobe spends ~25ms before it); 0 if it has no ops
	//   seek k:   Marks[k].At - Marks[k-1].At  (the next seek's first request
	//             goes out ~0.3ms after the previous frame line)
	// Zero when !OK.
	Latency time.Duration `json:"latency_ns"`

	// Ops are indices into Timeline.Ops attributed to this phase. Attribution
	// rule: op i belongs to phase k when Marks[k-1].At < Ops[i].Start <=
	// Marks[k].At (startup's lower bound is -inf). Ops after the last mark are
	// trailing read-ahead and belong to no phase.
	Ops []int `json:"ops"`

	Requests int   `json:"requests"` // len(Ops) of kind read/article
	Bytes    int64 `json:"bytes"`

	// Breakdown of Latency over the phase window, from the attributed ops
	// only. Transfer = union of time any op was delivering bytes (FirstByte
	// to End, where an abandoned op's delivery ends no later than the Start
	// of the next op in the timeline). Wait = union of time an op was
	// waiting for its first byte (an op that never got one waits Start to
	// End), minus Transfer: overlaps are counted once and flowing bytes win.
	// Client = Latency - Wait - Transfer: time the client spent between ops
	// (parsing, seeking, decoding).
	Wait     time.Duration `json:"wait_ns"`
	Transfer time.Duration `json:"transfer_ns"`
	Client   time.Duration `json:"client_ns"`
}

// Analysis is the derived view of one sample.
type Analysis struct {
	Phases []PhaseResult `json:"phases"`
	// TailBytes is read-ahead after the last mark: real upstream cost that no
	// phase needed.
	TailBytes int64 `json:"tail_bytes"`
	Bytes     int64 `json:"bytes"`
}

// Analyze derives per-phase results from a timeline. It is pure and total:
// any Timeline (including an empty one from a failed sample) yields one
// PhaseResult per plan phase, with OK=false for unmarked phases.
//
// Every op is either attributed to exactly one marked phase or counted in
// TailBytes (including all ops of a sample with no marks), so
// Bytes == sum(Phases[k].Bytes) + TailBytes.
func Analyze(tl Timeline, plan Plan) Analysis {
	res := Analysis{Phases: make([]PhaseResult, plan.Len())}
	next := 0 // first op not yet attributed; Ops are sorted by Start
	var lo time.Duration
	for k := range res.Phases {
		pr := &res.Phases[k]
		pr.Phase = k
		if k >= len(tl.Marks) {
			continue
		}
		hi := tl.Marks[k].At
		for ; next < len(tl.Ops) && tl.Ops[next].Start <= hi; next++ {
			op := tl.Ops[next]
			pr.Ops = append(pr.Ops, next)
			pr.Bytes += op.Bytes
			if op.Kind == OpRead || op.Kind == OpArticle {
				pr.Requests++
			}
		}
		start := lo
		if k == 0 {
			start = hi
			if len(pr.Ops) > 0 {
				start = tl.Ops[pr.Ops[0]].Start
			}
		}
		pr.OK = true
		pr.Latency = hi - start
		pr.Wait, pr.Transfer = split(tl.Ops, pr.Ops, start, hi)
		pr.Client = max(pr.Latency-pr.Wait-pr.Transfer, 0)
		lo = hi
	}
	for i, op := range tl.Ops {
		res.Bytes += op.Bytes
		if i >= next {
			res.TailBytes += op.Bytes
		}
	}
	return res
}

// split measures, within [from, to], the union of time the given ops spent
// waiting for a first byte and the union of time bytes were flowing.
// Transfer wins where they overlap: while any op delivers bytes the client
// is not stalled. An op that never got a byte waits from Start to End.
func split(ops []Op, idx []int, from, to time.Duration) (wait, transfer time.Duration) {
	type span struct {
		a, b     time.Duration
		transfer bool
	}
	var spans []span
	add := func(a, b time.Duration, transfer bool) {
		a, b = max(a, from), min(b, to)
		if a < b {
			spans = append(spans, span{a, b, transfer})
		}
	}
	for _, i := range idx {
		op := ops[i]
		if op.FirstByte == 0 {
			add(op.Start, op.End, false)
			continue
		}
		end := op.End
		// A client that seeks away stops reading, but the kernel keeps
		// accepting the abandoned read's bytes into the socket buffer while
		// the next request waits. Those bytes serve nobody, and counting
		// them as flowing would hide that wait.
		if op.Abandoned && i+1 < len(ops) {
			end = min(end, ops[i+1].Start)
		}
		add(op.Start, op.FirstByte, false)
		add(op.FirstByte, end, true)
	}
	if len(spans) == 0 {
		return 0, 0
	}
	cuts := make([]time.Duration, 0, 2*len(spans))
	for _, s := range spans {
		cuts = append(cuts, s.a, s.b)
	}
	slices.Sort(cuts)
	cuts = slices.Compact(cuts)
	for i := 1; i < len(cuts); i++ {
		a, b := cuts[i-1], cuts[i]
		covered, flowing := false, false
		for _, s := range spans {
			if s.a <= a && b <= s.b {
				covered = true
				flowing = flowing || s.transfer
			}
		}
		switch {
		case flowing:
			transfer += b - a
		case covered:
			wait += b - a
		}
	}
	return wait, transfer
}
