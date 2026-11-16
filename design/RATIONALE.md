# nzbff engine + dashboard: design rationale

## Problem

`nzbff` has a settled CLI and config, and `profiler.Run` is a stub. It needs to measure time to first frame and seek latency at each hop of jellyfin → rclone → infinidysk → usenet, keep every run, and show the history on an embedded dashboard. Three things make the shape non-obvious. First, a latency number on its own is useless: the prototype showed that seek 1 costs three round trips because of lazy MKV cues, and the person needs to see that, not just "611 ms". Second, usenet has no frames, but its numbers have to sit next to the video layers. Third, cron runs may overlap, and `serve` reads while a run writes. Constraints carried over from grounding: startup is measured from the client's first upstream request; seek k = mark k − mark k−1; API keys never go on ffprobe's command line; `vfs/forget` drops rclone metadata only; no third-party deps; config/CLI may gain fields but not be reworked.

## Usage (caller's view)

```sh
nzbff run                                  # measure, store, print table
nzbff run -format json -o today.json       # same report as the dashboard API
nzbff serve -listen 127.0.0.1:8484         # dashboard over results.dir
nzbff dev-stack -ttfb 200ms > fake.yaml    # fake stack for development (hidden)
```

```
TARGET  LAYER       PHASE      N  P50     P90     MIN     MAX     REQS  BYTES
foo     jellyfin    startup    5  412ms   440ms   398ms   451ms   1     832K
foo     rclone      startup    5  294ms   301ms   288ms   305ms   1     832K
foo     infinidysk  startup    5  217ms   222ms   215ms   230ms   1     832K
foo     usenet      startup    5  264ms   270ms   259ms   281ms   3     750K
foo     infinidysk  seek 30s   5  611ms   640ms   605ms   660ms   3     1.9M
1 failed: foo/infinidysk #1: timeout after 60s in phase "seek 30s" (ffprobe: signal: killed)
stored run 20261116T101502Z-3f9a1c in ~/.local/state/nzbff/results
```

Dashboard (`/`): **Latest run**: per target, a strip plot for each phase with one row per layer (top of stack first), a dot per sample (warmups hollow, failures ×), the p50 tick, with layers in stack order so the hops read side by side. **Trends**: p50 per (target, phase label) over runs, one line per layer. **Sample**: a waterfall of one sample's ops, with wait and transfer bars, abandoned reads hatched, and mark lines with shaded phases. This view answers why a number is what it is.

Call sites:

```go
// engine (internal/profiler): the whole measurement surface is two calls
tr := planTarget(ctx, p, cfg, mode, t)               // probe.Media + results.NewPlan
tl, err := p.Sample(ctx, layer, t, tr.Plan)          // cold cache, timeout, tap, ffprobe/NNTP inside
w.Add(results.SeriesKey{Target: t.Name, Layer: layer},
      results.Sample{Iteration: i, Warmup: warm, At: at, Timeline: tl, Err: errString(err)})

// e2e test (internal/profiler, later)
st, _ := fakestack.Start(ctx, fakestack.Options{InfinidyskTTFB: 200 * time.Millisecond})
defer st.Close()
cfg := st.Config(); cfg.Profile.Layers = []config.Layer{config.LayerInfinidysk}
_ = profiler.Run(ctx, profiler.Options{Config: cfg, Mode: profiler.ModeAll, Out: &buf, Logger: log})
run := mustLatest(cfg.Results.Dir)
a := results.Analyze(run.Series[0].Samples[0].Timeline, run.Targets[0].Plan)
// assert a.Phases[0].Latency ≈ Requests × 200ms ± transfer, per phase

// dashboard (internal/dashboard)
rep, _ := s.report(info)                             // Load + results.Summarize, memoized by mtime
json.NewEncoder(w).Encode(rep)
```

## Shape

**Data first: one fact model for every layer** (`internal/results`, timeline.go and analyze.go). A `Timeline` holds `Ops` (upstream operations: `read` through the tap, or NNTP `connect`/`auth`/`article`, each with Start, FirstByte, End, Offset, Bytes, Dial, Abandoned) and `Marks` (phase k closed at instant t), all on one monotonic clock per sample. A `Plan` is the ordered phases of one client session: phase 0 is always startup and phase k is `Seeks[k-1]`. `NewPlan` is its only constructor, so Offset and Fraction always come from one duration. Usenet uses the same semantics. Connect and auth fall before mark 0, so they count toward startup. Each article BODY closes a phase. With that, **one pure `Analyze(tl, plan)` derives everything for every layer**: latency (startup = mark0 − first op start; seek k = mark k − mark k−1), op attribution (an op belongs to phase k when mark k−1 < Start ≤ mark k; ops after the last mark are tail read-ahead), requests, bytes, and a wait/transfer/client breakdown. The recorder assigns `Mark.Phase` itself (`len(marks)`), so callers cannot skip or repeat a phase.

**Store facts, derive views.** Run files hold only settings, per-target plans, timelines and errors. `results.Summarize` turns a `Run` into a `Report` on every read, and that `Report` is the CLI's `-format json`, the table/csv source, and the body of `/api/runs/{id}`. Nothing derived is stored, so there is nothing to keep in sync, and a better attribution rule re-scores old runs (per single-source-of-truth). The schemas are in `design/example-run.json` (stored) and `design/example-report.json` (what the dashboard reads).

**One deep measurement module** (`internal/probe`). Its public surface is `New`, `Media` and `Sample`. Behind `Sample` sit the cold-cache `vfs/forget`, the per-sample timeout, a fresh upstream transport per sample, the loopback tap, three byte sources, the ffprobe driver and line parser, and the NNTP client and NZB parser. All of these are unexported, so nothing outside can import them. A layer is one row in the `layers` table: either a `source` (the tap and ffprobe do the rest) or a custom sampler (usenet). A test pins the table's keys to `config.AllLayers` (hand-synced list → build-time check). The tap owns Range semantics, TTFB stamping (at first payload byte, before writing), abandonment and accounting. A `source` only does `open(off, end) → body, size` and has one invariant: no upstream I/O outside `open`. Size comes from the first response, never from a stat or HEAD, so nothing escapes the timeline and nothing undoes `vfs/forget`.

**ffprobe uses `-show_frames`.** ffprobe flushes the decoder at the end of each read interval, so interval k's frame line prints before interval k+1's first request. On the prototype (MKV and MP4, h264 with 2 B-frames, 200 ms injected TTFB) frame and packet lines agreed within 15 ms per interval. Frames measure what the user asked for and fail loudly on undecodable streams. Marks are stamped right after the pipe read, before parsing.

**Engine** (`profiler.Run`). Order is target-major, then **iteration-interleaved layers**: all warmups, then iteration i of every layer, and so on. Provider drift over a long run then hits all layers of iteration i alike, so layers shown side by side are comparable. The engine does not subtract one layer from another: the layers run different workloads (a video session versus whole NNTP articles), so a difference would not isolate a hop. A failed sample is data (`Sample.Err`, with marked phases still counted), and so is an unplannable target (`Target.Err`). Only store I/O or cancellation stops a run. A cancelled run is written as `interrupted` and rendered. When samples failed, the engine returns `*FailedError` after rendering, so cron gets both output and a non-zero exit.

**Persistence: per-actor files, merge at read** (per separate-before-serializing-shared-state). The layout is `results.dir/<UTC-timestamp>-<rand>.json`, one file per run with one `RunWriter` as its only writer. Every write goes to a tmp file and is then renamed. The first publish is a hard link, which is exclusive, so two processes cannot claim the same ID. The engine rewrites the file after every sample, so a crash loses at most one sample. IDs sort by time, so `List` reads no file contents. Readers never see a torn file, and concurrent runs never share a file. `Finish` is idempotent. The dashboard memoizes `Summarize` by (id, mtime). Finished files never change, so that cache is exact, not a later patch.

Validation stays at the existing boundary (`config.Finalize`). The only config change is `results.dir`, which defaults to the XDG state dir. Everything after that is trusted (per boundary-discipline). Tracing the call chain: cli → `profiler.Run` → `probe.Sample` / `results.RunWriter`, three files.

**What the design deliberately does not do:** concurrent samples, transcoding paths (Jellyfin `static=true` only), anything inside a player, and storing derived numbers.

## Synthesis decision

Three runners sketched in parallel (opus high, codex gpt-6.1-sol high, grok-4.7 high). A codex gpt-6.1-sol high cross-judge scored them 26 (codex), 22 (opus) and 15 (grok) of 30 and recommended codex as the base.

**Base: the opus candidate, against the judge's pick.** All three converged on stored same-clock facts, one pure derivation on read, one file or directory per run, and sequential sampling. The base choice came down to three things the judge weighed less. Opus models NNTP as ops and marks like the HTTP layers, so one `Analyze`, one table and one dashboard view cover all four hops, where codex sends usenet down a separate panel and code path. Opus checkpoints each sample with an atomic rewrite, where codex loses a whole unpublished run on a crash, which matters for long cron runs. Opus's wait/transfer/client split answers "why is this number what it is" directly. The judge shares a model with the codex candidate, so its preference was discounted.

**Judge defects in the base, all fixed here:** NNTP TTFB now stamps the first body byte, not the 222 status line. `HopCost` is removed, because it subtracted unlike workloads. Unnamed targets get unique names (`target-N`) at the config boundary, so series keyed by name can't merge.

**Grafts.** From codex: the `timeline` package folded into `results` (one fewer package and hop), and fsync of file and directory on publish. From grok: the report's grid comes from the plan, so a layer that failed every sample stays visible, and percent seek points plan without a known duration (usenet-only runs use the fraction).

**Settled after the sketches:** `-show_frames` over opus's `-show_packets`, measured in the prototype.

**Rejected:** grok's per-sample files plus completion marker (more files, same guarantee as one atomically rewritten run file) and its production self-checks at init (tests do that job). Codex's end-of-run-only publication (crash loses the run).

## Tradeoffs accepted

- We accept re-analyzing timelines on every read in exchange for one source of truth and the ability to re-score history. The cost is small: KBs per sample, and memoized in `serve`.
- We accept that "first frame" includes decoding one keyframe (a few ms of local CPU) in exchange for measuring what a player needs and catching undecodable streams.
- We accept running all seeks in one ffprobe session, in exchange for realism. A player seeks within one session, and seek 1 legitimately pays the index load that later seeks don't. The waterfall shows that cost.
- We accept rewriting the whole run file per sample (O(run) per write) in exchange for crash durability without a log format or a lock.
- We accept a fresh HTTP transport per sample (each sample pays its own connects, shown as `Dial`) in exchange for samples that don't depend on each other's connection pool.
- We accept a fake mount that is a plain directory with no injectable latency. The harness checks the engine, not rclone.

## Alternatives considered

- **Per-measurement process (one ffprobe per phase, `-ss` seek).** Simpler attribution, but each seek would include a fresh open and probe. That misattributes open cost to seek, and the cue/moov loading that real players pay once would land on every phase. Rejected: the interface would be no smaller, and the numbers would answer a different question.
- **A separate usenet result type with its own stats path.** It would be more literal ("connect", "auth", "article TTFB" fields). Rejected because it forks Analyze, Summarize, the table, csv and every dashboard view into two code paths. Modelling NNTP as ops and marks keeps one path, and connect/auth/TTFB are still visible as op rows in the waterfall.
- **An append-only JSONL log (or SQLite) shared by all runs.** Trend queries get cheaper, but every concurrent run writes the same file, which needs locking, and SQLite needs cgo or a third-party driver. Rejected in favour of per-run files plus an exact mtime-keyed memo.
- **Storing derived metrics next to timelines.** Rejected: two sources of truth, and a fix to attribution would need a migration.

## Open questions and risks

- After the warmup, every layer has read the same byte ranges, so infinidysk's and the provider's caches are **warm for every measured sample whatever the order**. Should seek points be jittered per iteration (e.g. ±N GOPs) so each sample reads segments no earlier sample read, which would make "cold provider" seeks measurable? Startup cannot be jittered.
- Is clock skew between the stdout read and the tap's accept a problem? A seek's first request could be stamped a few hundred µs before the previous mark under load. If the fake-stack tests show that, should attribution allow a skew tolerance?
- Should the usenet layer use the largest NZB file even when the release is split into RAR volumes? In that case the fraction → segment mapping is wrong. Should we instead require an unsplit release, or let the target name the file?
- Should overlapping cron runs be serialized with a flock on `results.dir`? They can't corrupt each other, but they do skew each other's latency.
- `Status: running` is ambiguous after a crash. Is "running with an old mtime → shown as stale" enough, or do we want a heartbeat?

## Next implementation step

Build `fakestack` (infinidysk and NNTP first) together with `results.Analyze` and its table tests. Then fill in `probe`'s tap and ffprobe driver until an e2e test shows infinidysk startup ≈ requests × injected TTFB.
