# nzbFirstFrame

`nzbff` profiles **startup time (time to first frame)** and **seek time** across a
usenet streaming stack:

```
jellyfin  ->  rclone mount  ->  infinidysk  ->  usenet provider
```

Each hop can be measured on its own (a *layer*), so latency can be attributed to the
hop that adds it rather than only seen end to end.

| layer        | what is measured                                    |
|--------------|-----------------------------------------------------|
| `jellyfin`   | HTTP stream through the Jellyfin API (full stack)   |
| `rclone`     | file reads on the rclone mount (skips Jellyfin)     |
| `infinidysk` | HTTP reads straight from infinidysk (skips rclone)  |
| `usenet`     | raw NNTP connect / article latency (provider floor) |

> **Status:** scaffolding only — CLI, flags and config loading are in place;
> measurement is not implemented yet (`-dry-run` shows the resolved plan).

## Build

```sh
make build        # -> bin/nzbff
make test
```

## Usage

```sh
nzbff config init                 # write an annotated ./nzbff.yaml
export NZBFF_JELLYFIN_API_KEY=...
nzbff config validate
nzbff run -dry-run                # startup + seeks
nzbff startup -layers rclone -path "movies/Foo (2024)/Foo.mkv"
nzbff seek -target foo -seek 10%,00:42:00 -iterations 10 -cold-cache
nzbff config show                 # effective config, secrets redacted
```

Run `nzbff <command> -h` for every flag.

## Configuration

Precedence, lowest to highest: **built-in defaults → config file → CLI flags**.

The config file is the first of: `-config`/`-c`, `$NZBFF_CONFIG`, `./nzbff.yaml`,
`<user config dir>/nzbff/config.yaml`, `~/.config/nzbff/config.yaml`
(`nzbff config path` shows which one is used). With none found, defaults + flags are used.

- Values can reference the environment as `${VAR}` or `${VAR:-fallback}` — keep secrets
  out of the file. Unset variables without a fallback are an error.
- Unknown keys are rejected, so typos don't silently fall back to defaults.
- `profile.layers` left empty means "every layer whose section is configured".
- Seek points accept percentages (`50%`), Go durations (`90s`, `1m30s`) or timestamps
  (`01:02:03`, `02:03`).

See [`internal/config/nzbff.example.yaml`](internal/config/nzbff.example.yaml) for every option.

## Layout

```
cmd/nzbff/            entrypoint (signal handling, exit code)
internal/cli/         subcommands, flag -> config overrides
internal/config/      schema, defaults, file discovery, env expansion, validation
internal/profiler/    measurement (stub)
internal/version/     build info
```
