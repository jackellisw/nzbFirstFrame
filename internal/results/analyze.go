package results

import "time"

// PhaseResult is the derived view of one phase of one sample.
type PhaseResult struct {
	Phase int  `json:"phase"`
	OK    bool `json:"ok"` // the phase was marked

	// Latency is the phase's wall time on the sample clock:
	//   startup:  Marks[0].At - Ops[0].Start   (the client's first upstream
	//             request, not process spawn: ffprobe spends ~25ms before it)
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

	// Breakdown of Latency. Wait = sum over attributed ops of the time no
	// byte was flowing to the client because it was waiting on the first byte
	// of an op (overlaps counted once). Client = Latency - Wait - Transfer:
	// time the client spent between ops (parsing, seeking, decoding).
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
func Analyze(tl Timeline, plan Plan) Analysis {
	// TODO:
	//   res := make([]PhaseResult, plan.Len())
	//   lo := -inf
	//   for k := range res:
	//     res[k].Phase = k
	//     if k >= len(tl.Marks): continue           // unmarked → !OK, no ops
	//     hi := tl.Marks[k].At
	//     for i, op := range tl.Ops where lo < op.Start <= hi: attribute i
	//     start := lo; if k == 0 { start = first attributed op Start (or hi if none) }
	//     res[k].Latency = hi - start; OK = true
	//     Wait/Transfer: sweep [start,hi] marking intervals [op.Start, op.FirstByte)
	//       as wait and [op.FirstByte, min(op.End,hi)) as transfer; union, transfer wins
	//       on overlap (bytes were flowing). Client = Latency - Wait - Transfer.
	//     lo = hi
	//   TailBytes = sum Bytes of ops with Start > last mark
	panic("not implemented")
}
