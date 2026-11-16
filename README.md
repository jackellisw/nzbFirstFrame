# nzbFirstFrame

`nzbff` measures how long a video takes to show its first frame, and how long each seek takes, at every hop of a usenet streaming stack:

```
jellyfin  ->  rclone mount  ->  infinidysk  ->  usenet provider
```

It measures each hop on its own, stores every run, and serves the history on a local dashboard. When playback feels slow, the rows for each hop show which one adds the delay, and the request log of each sample shows why.

## What it measures

Each hop is a layer. A layer reads the media from its own entry point, so its number includes that hop and everything below it.

| Layer | What nzbff reads | Hops in the number |
|---|---|---|
| `jellyfin` | `GET {jellyfin.url}/Videos/{jellyfin_item_id}/stream?static=true` | Jellyfin, rclone, infinidysk, provider |
| `rclone` | the file `{rclone.mount_path}/{path}` | rclone, infinidysk, provider |
| `infinidysk` | `GET {infinidysk.url}/{path}` with a `Range` header | infinidysk, provider |
| `usenet` | NNTP articles listed in the target's `.nzb` | provider |

The `jellyfin` layer asks for the original file (`static=true`), the way a client that direct-plays does. nzbff does not measure transcoding.

### How a video layer is measured

Each sample of the `jellyfin`, `rclone`, or `infinidysk` layer is one ffprobe session. nzbff starts a small HTTP server on `127.0.0.1` (the tap) and points ffprobe at it. The tap fetches every byte ffprobe asks for from the layer and records each request: when it started, when its first byte arrived, when it ended, its byte offset, and how many bytes it delivered.

In that one session, ffprobe opens the file and decodes one frame, then seeks to each seek point in turn and decodes one frame there. nzbff stamps each frame the moment ffprobe prints it, on the same clock as the requests. If a phase decodes no frame, that phase fails and nzbff stops the session there. A file cut between keyframes does this at startup.

- Startup is the first frame minus the start of the first upstream request. ffprobe spends about 25 ms starting before it asks for anything, and that time is not the stack's.
- Seek k is frame k minus frame k-1. ffprobe sends the next seek's first request about 0.3 ms after it prints a frame, so the gap between two frames is the seek.

All seeks run in one session, as they do in a player. An MKV file keeps its seek index (the cues) at the end of the file, and ffprobe reads it on the first seek, so seek 1 includes that extra read and later seeks don't. An MP4 file with its `moov` box at the end needs the extra read at startup instead.

### How the usenet layer is measured

The provider serves articles, not frames, so the `usenet` layer replays the same plan as article fetches. Each sample is one NNTP session. It connects (with TLS when `usenet.tls` is true), sends `AUTHINFO` when `usenet.username` is set, then fetches with `BODY`:

- for startup, the first article of the largest file in the target's `.nzb`
- for each seek point, the article at the same fraction of that file

A phase ends when the whole article has arrived, because infinidysk needs the whole article to decode it. Startup includes the connect and the login. The usenet row is the provider's floor for one article. The video layers run a different workload, so don't subtract the usenet row from them.

### How a run is scheduled

For each target, nzbff first reads the media duration with one short ffprobe call through the deepest selected video layer (infinidysk, then rclone, then jellyfin). It needs the duration to turn `50%` into a position. `nzbff startup` skips this call. A run with only the `usenet` layer skips it too, and then accepts only percent seek points.

Then nzbff takes `warmup` samples of every layer, followed by `iterations` samples of every layer. It goes iteration by iteration: iteration 0 of every layer, then iteration 1 of every layer, and so on, top of the stack first. If the provider slows down for a while, the slowdown affects every layer of the same iterations, instead of every sample of one layer. `cooldown` passes between samples. `timeout` bounds one sample, open and seeks together.

The warmup reads the same byte ranges as the measured samples. If infinidysk or the provider caches what it serves, those caches already hold every range a measured sample reads, so the numbers describe the warm path.

## Requirements

- ffprobe from FFmpeg, on `PATH` or named in `tools.ffprobe`. nzbff was developed against ffprobe 9.0.
- Go 1.27 to build.
- ffmpeg, only for `nzbff dev-stack` and the tests, which generate test video.
- For the `rclone` layer, run nzbff on a machine that has the rclone mount.

Each layer needs its own settings:

| Layer | Needs |
|---|---|
| `jellyfin` | `jellyfin.url`, `jellyfin.api_key`, and a `jellyfin_item_id` per target |
| `rclone` | `rclone.mount_path` and a `path` per target |
| `infinidysk` | `infinidysk.url`, optional `username` and `password`, and a `path` per target |
| `usenet` | `usenet.host`, `port`, `tls`, `username`, `password`, and an `nzb` per target |

`profile.cold_cache` also needs `rclone.rc_url`.

## Measure your stack

1. Build the binary:

	```sh
	make build        # writes bin/nzbff
	```

2. Write an annotated config to `./nzbff.yaml`. The file is created with mode 0600 because it can hold secrets.

	```sh
	bin/nzbff config init
	```

3. Edit `nzbff.yaml`. Set the address of each hop you want to measure (a URL, or the mount path for rclone) and add one target. A target's `path` is the file's path relative to the rclone mount, and it must be the same path on infinidysk. The `jellyfin_item_id` is the `id=` value in the item's page URL in the Jellyfin web UI. Create an API key in Jellyfin's admin dashboard under **API Keys**.

4. Give nzbff the API key. The example config reads it from the environment:

	```sh
	export NZBFF_JELLYFIN_API_KEY=...
	```

5. Check the config, then look at the plan without measuring anything:

	```sh
	bin/nzbff config validate
	bin/nzbff run -dry-run
	```

	The plan lists the layers, iterations, seek points, targets, and the `results:` directory the run will be stored in.

6. Measure:

	```sh
	bin/nzbff run
	```

	With the defaults, each target takes 24 samples (4 layers, 1 warmup and 5 measured samples each) with a 2 s cooldown between them. nzbff logs one line per sample to stderr, then prints the table to stdout.

To measure one layer or one file without editing the config, use flags:

```sh
bin/nzbff startup -layers rclone -path "movies/Foo (2024)/Foo.mkv"
bin/nzbff seek -target foo -seek 10%,00:42:00 -iterations 10
```

`nzbff startup` measures startup only. `nzbff seek` runs the same session as `nzbff run` and fails without seek points. Its table still has a startup row, because every session opens the file first.

## Try it on a fake stack

`nzbff dev-stack` runs a fake Jellyfin, infinidysk, rclone remote-control endpoint, and NNTP server on `127.0.0.1`, each with injected latency. Use it to see what nzbff reports before you point it at your stack.

1. Start the fake stack. It encodes a 3-minute 720p test video with ffmpeg the first time, and reuses it later.

	```sh
	bin/nzbff dev-stack -dir /tmp/nzbff-dev -o /tmp/nzbff-dev/fake.yaml
	```

	It writes a config for itself to `fake.yaml`, prints its addresses, and runs until you press Ctrl-C. Every Jellyfin and infinidysk response waits 200 ms before its first byte (`-ttfb`), and every article waits 80 ms (`-article-ttfb`). The "rclone mount" is a plain directory with no injected latency.

2. In another terminal, measure it:

	```sh
	bin/nzbff run -c /tmp/nzbff-dev/fake.yaml
	```

	You see a table like this one:

	```
	TARGET  LAYER       PHASE     N  P50    P90    MIN    MAX    REQS  BYTES
	fake    jellyfin    startup   5  211ms  211ms  211ms  211ms  1     867K
	fake    jellyfin    seek 10%  5  613ms  614ms  612ms  614ms  3     1.9M
	fake    jellyfin    seek 50%  5  207ms  208ms  206ms  208ms  1     827K
	fake    jellyfin    seek 90%  5  208ms  208ms  207ms  208ms  1     867K
	fake    rclone      startup   5  4.1ms  4.3ms  4.1ms  4.3ms  1     1.0M
	fake    rclone      seek 10%  5  3.1ms  3.6ms  2.9ms  3.6ms  3     2.0M
	fake    rclone      seek 50%  5  2.3ms  2.4ms  2.3ms  2.4ms  1     1.8M
	fake    rclone      seek 90%  5  2.3ms  2.6ms  2.2ms  2.6ms  1     1.1M
	fake    infinidysk  startup   5  210ms  212ms  206ms  212ms  1     867K
	fake    infinidysk  seek 10%  5  613ms  614ms  611ms  614ms  3     1.6M
	fake    infinidysk  seek 50%  5  207ms  208ms  206ms  208ms  1     967K
	fake    infinidysk  seek 90%  5  208ms  208ms  204ms  208ms  1     891K
	fake    usenet      startup   5  86ms   86ms   84ms   86ms   1     756K
	fake    usenet      seek 10%  5  88ms   88ms   85ms   88ms   1     756K
	fake    usenet      seek 50%  5  87ms   88ms   87ms   88ms   1     756K
	fake    usenet      seek 90%  5  87ms   89ms   84ms   89ms   1     756K
	stored run 20261116T162733Z-e3a2ab in /tmp/nzbff-dev/results
	```

	Each HTTP request costs one 200 ms wait, so startup is one request (about 210 ms). Seek 10% costs three requests (about 613 ms), because the MKV keeps its cues at the end of the file and ffprobe loads them on the first seek. The later seeks cost one request each. The rclone rows show local disk speed, and the usenet rows show the 80 ms article wait plus the transfer.

3. Open the dashboard on the same results:

	```sh
	bin/nzbff serve -c /tmp/nzbff-dev/fake.yaml
	```

	Then open `http://127.0.0.1:8484/`.

With `-container mp4`, the test video is an MP4 with its `moov` box at the end of the file, so startup takes three requests instead of one. Without `-dir`, `dev-stack` uses a temporary directory and removes it on exit. `dev-stack` is not listed in `nzbff -h`.

## Read the table

The table has one row per target, layer, and phase. Layers appear top of the stack first.

| Column | Meaning |
|---|---|
| `N` | Measured samples that reached this phase. Warmups are not counted. |
| `P50`, `P90`, `MIN`, `MAX` | The phase latency over those samples. Percentiles are nearest-rank. |
| `REQS` | The median number of upstream requests in the phase: reads through the tap, or articles for `usenet`. |
| `BYTES` | The median bytes delivered in the phase. This includes read-ahead that ffprobe dropped when it seeked, because the stack still had to fetch those bytes. |

A dash means no measured sample reached the phase. Below the table, nzbff lists every target it could not plan and every sample that failed, warmups included. Each line names the phase the sample did not reach and the error. The last line names the stored run and the directory it is in, and says `(interrupted)` if you stopped the run.

The three video layers run the same session on the same file, so their rows compare directly. In a real stack each layer reads through the ones below it. For example, if Jellyfin's startup p50 is 900 ms and rclone's is 850 ms, Jellyfin adds about 50 ms and the rest comes from further down. Compare rclone with infinidysk next, then infinidysk with the usenet floor.

`REQS` tells you whether a slow phase is many round trips or one slow one. A seek that needs 3 requests on every layer points at the file layout (its index sits at the end), and each of those requests costs a full round trip through every hop below the layer.

`-format json` prints the full report, the same document the dashboard reads. It includes each sample's requests and, per phase, how its time splits into waiting for a first byte, receiving bytes, and ffprobe's own work between requests. `-format csv` prints one row per sample and phase with the same split. `-o FILE` writes the report to a file instead of stdout.

## Read the dashboard

`nzbff serve` serves the dashboard for the results directory, at `http://127.0.0.1:8484/` by default. It only reads the results, so it needs no stack settings. `-results DIR` overrides `results.dir`, and `-listen ADDR` changes the address.

- **Latest run** shows, per target, one strip per phase with a row per layer. Each dot is a sample. A hollow dot is a warmup, an × is a sample that failed before that phase, and the tick is the p50.
- **Sample** opens when you click a dot. It draws the sample's waterfall: one row per upstream request (or NNTP connect, login, and article), with a bar for the wait until the first byte and a bar for the transfer. Hatched bars are reads ffprobe abandoned. Vertical lines mark each frame, and the phases are shaded. This view explains why a number is what it is.
- **Trends** plots the p50 of each phase over all runs, one line per layer.

Pick an older run from the run list. The dashboard warns about a run that is still marked as running but has had no new sample for a while, because the process that wrote it has probably died.

The same data is available as JSON at `/api/runs`, `/api/runs/latest`, and `/api/runs/{id}`. The dashboard has no login. Keep it on `127.0.0.1`, or put it behind a reverse proxy that has one. It works under a sub-path.

## Run it from cron

nzbff exits with:

- 0 when every measured sample completed.
- 1 when a measured sample failed or a target could not be planned. nzbff prints the report first, then one line on stderr such as `nzbff run: run 20261116T031502Z-7c41e9: 2 of 20 samples failed (see report)`. Exit code 1 also covers an interrupted run and any other error.
- 2 for a bad flag or argument.

Cron starts jobs with almost no environment. Put the secrets in the config file, which `config init` creates with mode 0600, or set them as `NAME=value` lines at the top of the crontab:

```cron
NZBFF_JELLYFIN_API_KEY=your-key
15 3 * * *  /usr/local/bin/nzbff run -c /home/you/.config/nzbff/config.yaml >>/home/you/nzbff.log 2>&1
```

Every run is its own file, `results.dir/<UTC start>-<random>.json`. nzbff rewrites it after each sample, so a crash loses at most the sample in flight. Two runs that overlap never write the same file, but they share the same upstream and slow each other down, so leave more time between cron jobs than a run takes. `results.dir` defaults to `$XDG_STATE_HOME/nzbff/results`, or `~/.local/state/nzbff/results` without `XDG_STATE_HOME`.

The log on stderr has one line per sample, at `info` when it completed and at `warn` with the error when it failed:

```
time=2026-11-16T16:27:38.039Z level=INFO msg=sample target=fake layer=jellyfin iteration=0 warmup=false phases="startup 210.9ms, seek 10% 611.9ms, seek 50% 206.7ms, seek 90% 206.7ms"
```

Set `log.format: json` for one JSON object per line.

## What cold_cache does

With `profile.cold_cache: true` (or `-cold-cache`), nzbff calls rclone's remote-control `vfs/forget` for the target's path before each sample of the `jellyfin` and `rclone` layers, the two layers that read through rclone. It needs `rclone.rc_url`, plus `rc_user` and `rc_password` if your rc endpoint requires a login. The call is not part of the measured time. If it fails, the sample fails.

`vfs/forget` drops rclone's directory and attribute cache for that path, so the next open looks the file up again. It never drops cached file data. With `--vfs-cache-mode full`, rclone still serves every byte it has on disk, and the caches in infinidysk and at the provider stay as they were. A cold-cache run therefore measures cold rclone metadata, not cold data.

## What nzbff assumes about infinidysk

infinidysk has no public documentation. nzbff treats it as an HTTP server that rclone mounts, with these properties:

- It serves the file at `{infinidysk.url}/{path}`, where `path` is the same relative path the file has on the rclone mount.
- It answers a `Range` request with `206 Partial Content` and a `Content-Range` header that includes the total size.
- It may require basic auth, which nzbff sends when `infinidysk.username` is set.

If infinidysk answers a read from an offset with `200 OK`, the layer fails with `upstream ignored Range`. If its `Content-Range` has no total size, the layer fails with `upstream sent 206 with unusable Content-Range`.

## Privacy

- nzbff sends the Jellyfin API key in the `X-Emby-Token` header from its own HTTP client. It never puts the key, a password, or a stack URL on ffprobe's command line, which other users on the machine can read with `ps`. ffprobe only ever sees `http://127.0.0.1:<port>/media`.
- If a server redirects to another host, port, or scheme, nzbff follows the redirect without the API key and without basic auth.
- Run files store no secrets. They hold target names, paths, Jellyfin item IDs, byte offsets, `Range` headers, article message IDs, the NNTP server address, and error messages. nzbff writes them with mode 0600, and creates a missing results directory with mode 0700.
- `nzbff config show` prints the effective config with secrets replaced by `********`.
- With `usenet.tls: false`, the usenet password crosses the network in clear text.

## Configuration

nzbff merges settings in this order, later ones winning: built-in defaults, then the config file, then flags.

The config file is the first one found of: `-config` (or `-c`), `$NZBFF_CONFIG`, `./nzbff.yaml`, `<user config dir>/nzbff/config.yaml`, and `~/.config/nzbff/config.yaml`. `nzbff config path` shows which one nzbff uses. With no file, nzbff runs on defaults and flags.

- A value can read the environment as `${VAR}` or `${VAR:-fallback}`, so secrets can stay out of the file. An unset variable without a fallback is an error.
- An unknown key is an error, so a typo does not fall back to a default.
- An empty `profile.layers` means every layer whose address is set: `jellyfin.url`, `rclone.mount_path`, `infinidysk.url`, or `usenet.host`.
- A seek point is a percentage (`50%`), a Go duration (`90s`, `1m30s`), or a timestamp (`01:02:03`, `02:03`).

[`internal/config/nzbff.example.yaml`](internal/config/nzbff.example.yaml) documents every option. Run `nzbff <command> -h` for every flag.

## Build and test

```sh
make build        # bin/nzbff
make test         # go test ./...
```

Tests that need ffmpeg or ffprobe skip when those are not on `PATH`. The end-to-end tests run `nzbff run` against the fake stack with real ffprobe.

```
cmd/nzbff/            entry point: signals and exit code
internal/cli/         commands and flags
internal/config/      schema, defaults, file lookup, environment expansion, validation
internal/profiler/    the run: planning, scheduling, storing each sample
internal/probe/       one sample of one layer: the tap, ffprobe, the sources, NNTP
internal/results/     the run file store, analysis of timelines, table, JSON, and CSV
internal/dashboard/   nzbff serve: the embedded web UI and its JSON API
internal/fakestack/   the fake stack behind nzbff dev-stack and the end-to-end tests
```

`design/RATIONALE.md` explains why the design is shaped this way.
