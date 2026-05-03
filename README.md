<p align="center">
  <img src="internal/httpx/static/icon.svg" alt="StationCast" width="128">
</p>

# StationCast

A small, self-contained internet radio server in Go. Drop audio files into a directory and they're broadcast live to every listener, Icecast/SHOUTcast style, with a simple admin and a public listener UI

## Features

- **Filesystem-first library**: drop `mp3`, `wav`, `flac`, `ogg`, `oga`, `m4a`, or `aac` files into the music directory and they appear live via `fsnotify`. Rename, move, or delete on disk and the library follows. No import step, no rescan button
- **True broadcast**: every listener receives identical bytes at the same time, with gapless encoding across track boundaries
- **Stream in any format a client supports**:
  - `GET /stream` (alias `/stream.mp3`) - direct MP3 with optional ICY metadata, for browsers, VLC, foobar2000, smart speakers, Sonos
  - `GET /stream.pls` - PLS playlist file, the most common format for hardware internet radios
  - `GET /stream.m3u` - extended M3U pointing at `/stream`
  - `GET /hls.m3u8` - HLS playlist with rolling TS segments, for iOS Safari and Apple TV
- **Audio chain controls** for consistent perceived volume across a mixed library:
  - `STATIONCAST_REPLAYGAIN` reads precomputed ReplayGain ID3 tags (eg from `rsgain easy`) and applies them per track
  - `STATIONCAST_LOUDNORM` runs ffmpeg's EBU R128 loudness normalization at -16 LUFS / -1.5 dBTP true-peak
  - `STATIONCAST_GAIN_DB` applies a final dB boost on top
- **Per-track metadata to every client**: ID3 tag title / artist / album are extracted on file scan and propagated live to listeners via ICY `StreamTitle` on `/stream`, the `/now-playing` JSON endpoint, the `/now-playing/sse` Server-Sent Events stream, and `navigator.mediaSession` on the public player (lock-screen now-playing, hardware media keys, OS-level controls)
- **Album art with iTunes fallback**: when `STATIONCAST_ITUNES_ART=true` (default), iTunes Search API artwork is preferred per `(artist, album)` pair, with embedded ID3 art used as a fallback when iTunes has no result, and a placeholder when neither is available. All artwork is cached on disk under `data/art/`
- **Web admin**: live now-playing, shuffle / sequential / loop modes, manual queue, upload / rename / delete files, listener count
- **Public player**: live-updating now-playing over Server-Sent Events, one-click copy of every stream URL, auto-selects HLS on iOS Safari and direct MP3 elsewhere
- **Security**: optional reCAPTCHA v3 on the login form (action match + score >= 0.5), `STATIONCAST_MAX_LISTENERS` hard cap that returns HTTP 503 to excess `/stream` connections
- **Container-ready**: single binary, multi-arch (amd64/arm64) Docker image with `PUID`/`PGID` support for NAS bind mounts, only `ffmpeg` as a runtime dependency
- **Resilient broadcast**: slow listeners are dropped before they back up the encoder so per-stream lag never affects the rest; pause injects silence so listeners stay connected

## Quick start

### Docker (recommended)

```sh
docker run -d --name stationcast --restart unless-stopped \
  -p 8000:8000 \
  -v $(pwd)/music:/music \
  -v $(pwd)/data:/data \
  -e STATIONCAST_ADMIN_PASSWORD=changeme \
  ghcr.io/rursache/stationcast:latest
```

Drop audio files into `./music/`, open `http://localhost:8000/`, sign in to `/admin/login` with the password above

### docker-compose

```yaml
services:
  stationcast:
    image: ghcr.io/rursache/stationcast:latest
    restart: unless-stopped
    ports:
      - "8000:8000"
    volumes:
      - ./music:/music
      - ./data:/data
    environment:
      STATIONCAST_ADMIN_PASSWORD: changeme
```

A ready-to-edit `docker-compose.yml` is in the repo

### From source

Requires Go 1.25+ and `ffmpeg` in `PATH`

```sh
go build -o ./bin/stationcast ./cmd/stationcast
STATIONCAST_ADMIN_PASSWORD=changeme \
STATIONCAST_MUSIC_DIR=./music \
STATIONCAST_DATA_DIR=./data \
./bin/stationcast
```

## How it works

```
filesystem (watched)
        │
        ▼
scheduler ── shuffle / sequential / loop, manual queue
        │
        ▼
ffmpeg per-track decoder  ──► PCM s16le 44.1k stereo
        │
        ▼
realtime pacer (176_400 B/s token bucket)
        │
        ▼
single long-lived ffmpeg PCM ──► MP3 encoder (gapless across tracks)
        │
        ▼
broadcast hub (ring fan-out, slow-listener drop)
        │
        ├── /stream     direct MP3 + ICY metadata
        └── ffmpeg HLS subprocess ──► /hls.m3u8 + rolling TS segments
```

- The filesystem is the source of truth, SQLite is just a cache index
- `fsnotify` picks up file add / remove / rename in real time
- The encoder runs continuously, decoder swaps happen mid-pipe, output is gapless
- Slow listeners are dropped before they back up the broadcast, keeping latency bounded
- Pause means injecting silence PCM, listeners stay connected
- iTunes Search API is consulted once per `(artist, album)` for missing art and the result is cached on disk

The output is a single 128 kbps MP3 at 44.1 kHz stereo regardless of input format, so every listener receives identical bytes (true broadcast)

## Listening

| Client | Use this URL |
|---|---|
| iOS Safari, Apple TV | `https://your.host/hls.m3u8` |
| macOS Safari, Chrome, Firefox | `https://your.host/stream` |
| VLC, foobar2000, Sonos, smart speakers | `https://your.host/stream` or `/stream.pls` or `/stream.m3u` |
| Hardware internet radios | `/stream.pls` (most common) |

The public web UI at `/` auto-picks HLS on iOS Safari and the direct MP3 stream elsewhere, so for browser listeners just point them at the root

## Configuration

### Environment variables

| Variable | Default | Notes |
|---|---|---|
| `STATIONCAST_ADMIN_PASSWORD` | required | Password for `/admin/login` |
| `STATIONCAST_MUSIC_DIR` | `./music` | Audio library, source of truth, watched live |
| `STATIONCAST_DATA_DIR` | `./data` | SQLite index, album art cache, HLS segments |
| `STATIONCAST_ADDR` | `:8000` | Listen address |
| `STATIONCAST_PUBLIC_URL` | `` (request host) | External base URL used in PLS/M3U files |
| `STATIONCAST_BITRATE` | `128` | MP3 output bitrate, kbps |
| `STATIONCAST_STATION_NAME` | `StationCast` | Shown in ICY headers, the public UI, and as the MediaSession fallback when a track has no album tag |
| `STATIONCAST_STATION_GENRE` | `Various` | ICY genre header |
| `STATIONCAST_LOUDNORM` | `false` | Apply per-track ffmpeg `loudnorm` so volume does not jump between tracks. Dynamic real-time analysis of the decoded PCM, targets -16 LUFS / -1.5 dBTP |
| `STATIONCAST_REPLAYGAIN` | `false` | Apply ReplayGain track offsets from ID3 tags before loudnorm. Pairs with files tagged by `rsgain easy` (or similar). Tracks without RG tags pass through unchanged. Best combined with `STATIONCAST_LOUDNORM=true` so loudnorm catches the rest |
| `STATIONCAST_GAIN_DB` | `0` | Source volume boost in dB (range -20 to +20). Applied after loudnorm so it stacks. Aggressive positive values combined with loudnorm can clip the output (loudnorm targets a true-peak of -1.5 dBTP, so anything above +1 dB will start to push peaks above 0 dB) |
| `STATIONCAST_ITUNES_ART` | `true` | Fetch missing album art from the iTunes Search API when artist + album tags exist |
| `STATIONCAST_MAX_LISTENERS` | `256` | Hard cap on concurrent `/stream` connections. Excess listeners get HTTP 503. Set to `0` for unlimited (not recommended) |
| `STATIONCAST_RECAPTCHA_SITE_KEY` | `` | Optional Google reCAPTCHA v3 site key. When set together with the secret, the login form runs an invisible v3 challenge with action `login` |
| `STATIONCAST_RECAPTCHA_SECRET_KEY` | `` | Optional Google reCAPTCHA v3 secret. Verified against the siteverify endpoint on every login attempt, requiring `success=true`, matching action, and score >= 0.5 |
| `PUID` | `1000` (Docker only) | Numeric uid the in-container `app` user runs as. Set to match the host owner of your bind-mounted `/data` (and `/music`) on a NAS or rootless host |
| `PGID` | `1000` (Docker only) | Numeric gid the in-container `app` group uses. Pair with `PUID`. The entrypoint chowns `/data` to these ids on every startup |

### Endpoints

#### Public

| Path | What it serves |
|---|---|
| `GET /` | Public web player |
| `GET /now-playing` | Current track JSON |
| `GET /now-playing/sse` | Server-Sent Events stream of now-playing changes |
| `GET /art/:id` | Album art for a track id |
| `GET /stream` (alias `/stream.mp3`) | MP3 stream with optional ICY metadata (`Icy-MetaData: 1` request header) |
| `GET /stream.pls` | PLS playlist file pointing at `/stream` |
| `GET /stream.m3u` | Extended M3U pointing at `/stream` |
| `GET /hls.m3u8` | HLS playlist (rolling window) for iOS / Apple Safari |

#### Admin (cookie session, login required)

| Method | Path | Action |
|---|---|---|
| `GET` | `/admin/` | Dashboard |
| `POST` | `/admin/skip` | Advance to next track |
| `POST` | `/admin/mode` | Set mode (`shuffle`, `sequential`, `loop`) |
| `POST` | `/admin/queue` | Enqueue a track id |
| `POST` | `/admin/queue/remove` | Remove queue entry by index |
| `POST` | `/admin/files/upload` | Upload an audio file into the music directory |
| `POST` | `/admin/files/rename` | Rename a file |
| `POST` | `/admin/files/delete` | Delete a file |

## Persistence

Everything in `STATIONCAST_DATA_DIR`:

- `stationcast.db` SQLite index of tracks, queue, history, settings
- `art/<id>.jpg` extracted or downloaded album art
- `hls/playlist.m3u8` and `hls/seg-*.ts` rolling HLS segments

Safe to delete: HLS segments and the SQLite file (it will rebuild from the music directory). Album art is rebuilt on next play if embedded, or re-fetched on next library scan if from iTunes

## License

MIT
