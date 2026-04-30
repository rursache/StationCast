<p align="center">
  <img src="internal/httpx/static/icon.svg" alt="StationCast" width="128">
</p>

# StationCast

A small, self-contained internet radio server in Go. Streams a continuous audio feed from a directory of music files in Icecast/SHOUTcast style, with a simple admin and a public listener UI

- Single binary, single subprocess dependency: `ffmpeg`
- Direct MP3 stream with ICY metadata, plus PLS, M3U, and HLS endpoints
- All listeners hear the same audio at the same time (true broadcast, not on-demand)
- Web admin: skip, mode (shuffle / sequential / loop), queue, upload, rename, delete
- Public player: album art, current + next track, volume, play/pause, copy-paste stream URLs, live updates over SSE
- Optional iTunes Search lookup for missing album art when the file has artist + album tags
- Designed to run 24/7 in Docker

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

## Environment variables

| Variable | Default | Notes |
|---|---|---|
| `STATIONCAST_ADMIN_PASSWORD` | required | Password for `/admin/login` |
| `STATIONCAST_MUSIC_DIR` | `./music` | Audio library, source of truth, watched live |
| `STATIONCAST_DATA_DIR` | `./data` | SQLite index, album art cache, HLS segments |
| `STATIONCAST_ADDR` | `:8000` | Listen address |
| `STATIONCAST_PUBLIC_URL` | `` (request host) | External base URL used in PLS/M3U files |
| `STATIONCAST_BITRATE` | `128` | MP3 output bitrate, kbps |
| `STATIONCAST_STATION_NAME` | `StationCast` | Shown in ICY headers, public UI, MediaSession |
| `STATIONCAST_STATION_GENRE` | `Various` | ICY genre header |
| `STATIONCAST_LOUDNORM` | `false` | Apply per-track ffmpeg `loudnorm` so volume does not jump between tracks |
| `STATIONCAST_GAIN_DB` | `0` | Source volume boost in dB (range -20 to +20). Applied after loudnorm so it stacks. Aggressive positive values combined with loudnorm can clip the output (loudnorm targets a true-peak of -1.5 dBTP, so anything above +1 dB will start to push peaks above 0 dB) |
| `STATIONCAST_ITUNES_ART` | `true` | Fetch missing album art from the iTunes Search API when artist + album tags exist |

## Endpoints

### Public

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

### Admin (cookie session, login required)

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

## Client compatibility

| Client | Use this URL |
|---|---|
| iOS Safari, Apple TV | `https://your.host/hls.m3u8` |
| macOS Safari, Chrome, Firefox | `https://your.host/stream` |
| VLC, foobar2000, Sonos, smart speakers | `https://your.host/stream` or `/stream.pls` or `/stream.m3u` |
| Hardware internet radios | `/stream.pls` (most common) |

The public web UI auto-selects HLS on iOS Safari and the direct MP3 stream elsewhere

## Supported input formats

`mp3`, `wav`, `flac`, `ogg`, `oga`, `m4a`, `aac`

The server transcodes all of them into a single 128 kbps MP3 output at 44.1 kHz stereo so every listener receives identical bytes (true broadcast)

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

- Source of truth is the filesystem, SQLite is just a cache index
- `fsnotify` picks up file add / remove / rename in real time
- iTunes Search API is consulted once per (artist, album) for missing art and the result is cached on disk
- The encoder runs continuously, decoder swaps happen mid-pipe, output is gapless
- Slow listeners are dropped before they back up the broadcast, keeping latency bounded
- Pause means injecting silence PCM, listeners stay connected

## Persistence

Everything in `STATIONCAST_DATA_DIR`:

- `stationcast.db` SQLite index of tracks, queue, history, settings
- `art/<id>.jpg` extracted or downloaded album art
- `hls/playlist.m3u8` and `hls/seg-*.ts` rolling HLS segments

Safe to delete: HLS segments and the SQLite file (it will rebuild from the music directory). Album art is rebuilt on next play if embedded, or re-fetched on next library scan if from iTunes

## Releases

Tag a commit `vX.Y.Z` and the GitHub Action builds and publishes a multi-arch image (`linux/amd64`, `linux/arm64`) to `ghcr.io/rursache/stationcast`

```sh
git tag v1.0.0
git push origin v1.0.0
```

## License

MIT
