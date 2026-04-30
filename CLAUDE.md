# Notes for Claude / contributors

Conventions and workflows for working in this repo. Read this before making non-trivial changes

## Cutting a release

A release is one push:

```sh
git tag -a vX.Y.Z -m "<release notes - becomes the GitHub Release body>"
git push origin vX.Y.Z
```

Two GitHub Actions fire on `v*` tag push:

| Workflow | What it does |
|---|---|
| `.github/workflows/release.yml` | Cross-builds linux amd64/arm64 + darwin amd64/arm64 binaries, generates SHA256SUMS, extracts the tag annotation as release notes, creates the GitHub Release with binaries attached |
| `.github/workflows/docker.yml` | Builds and pushes a multi-arch container image to `ghcr.io/rursache/stationcast` tagged `vX.Y.Z`, `X.Y`, `X`, and `latest` |

The tag annotation message becomes the release body. For multi-paragraph notes use a heredoc:

```sh
git tag -a v1.2.0 -m "$(cat <<'EOF'
Headline change in one line

Details across several lines work fine here. Markdown is rendered on the GitHub Release page
EOF
)"
```

Both workflows can also be triggered manually from the Actions tab via `workflow_dispatch` (useful for re-running a failed release without retagging)

If a release exists at the same tag, `release.yml` deletes and recreates it so re-runs are idempotent. The Docker workflow always overwrites the same tag, so re-pushing the same image digest is fine

## Re-releasing a tag (uncommon)

If the v1.0.0 image needs a rebuild without bumping version (eg. UI fix landed but version is unchanged):

```sh
git tag -d v1.0.0
git push --delete origin v1.0.0
git tag -a v1.0.0 -m "..." HEAD
git push origin v1.0.0
```

Both workflows fire as if it were a fresh tag

## Project layout

```
cmd/stationcast/     entrypoint
internal/audio/      ffmpeg decoder + encoder + real-time pacer
internal/broadcast/  hub fan-out, ICY metadata injection, HLS subprocess
internal/playlist/   library scan, scheduler, iTunes art
internal/storage/    SQLite migrations
internal/files/      filesystem ops with traversal guards
internal/httpx/      router, handlers, templates, static, auth
internal/config/     env loader
```

## Source of truth

The filesystem at `STATIONCAST_MUSIC_DIR` is canonical. SQLite is just an index that fsnotify keeps in sync. Never write a path to the DB that does not exist on disk

## Audio pipeline invariants

- One long-lived `ffmpeg` encoder process per server, PCM in -> MP3 out
- Per-track `ffmpeg` decoder spawned by `scheduler.Pick` on demand
- `realtimeWriter` throttles PCM writes to the encoder to exactly `sampleRate * channels * 2` bytes/sec
- `pcmSource` never returns EOF; it returns silence when the library is empty or the current decoder ends
- Hub fan-out drops slow listeners rather than back-pressuring the encoder

## Adding a new env var

1. Add the field to `internal/config/config.go` `Config` struct
2. Read it in `Load()` with a sensible default and validation
3. Document it in the `README.md` env table
4. Wire it into the relevant component constructor or call site
5. Log it in the `starting` slog line in `cmd/stationcast/main.go` so misconfiguration is visible

## Testing

- `go test ./...` runs unit tests including the unicode filename roundtrip
- The CI workflow runs `go vet`, `go test -race`, builds, and a smoke test that captures real audio bytes from the running server
- Manual end-to-end: drop files in `./music/`, run the binary, hit `/stream` with `curl -m 5 -o /tmp/x.mp3` and check with `ffprobe`

## Conventions

- No emojis or trailing periods in committed text (commit messages, doc, comments) per repo style
- Short comments only when intent is non-obvious, never to restate the code
- Prefer editing existing files over creating new ones
- Cross-platform: anything Linux must work; macOS is the dev target. Avoid macOS-only paths or syscalls
