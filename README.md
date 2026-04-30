# StationCast

Lightweight Icecast-style radio server in Go

## Requirements

- Go 1.22+
- ffmpeg in PATH (tested with 8.1)

## Build and run

```sh
go build -o ./bin/stationcast ./cmd/stationcast

STATIONCAST_ADMIN_PASSWORD=changeme \
STATIONCAST_MUSIC_DIR=./music \
STATIONCAST_DATA_DIR=./data \
./bin/stationcast
```

Default address is `:8000`

## Environment

| Var | Default | Notes |
|---|---|---|
| `STATIONCAST_ADMIN_PASSWORD` | required | Admin login password |
| `STATIONCAST_MUSIC_DIR` | `./music` | Music library, source of truth |
| `STATIONCAST_DATA_DIR` | `./data` | SQLite + art cache + HLS segments |
| `STATIONCAST_ADDR` | `:8000` | Listen address |
| `STATIONCAST_PUBLIC_URL` | `` | Used in PLS/M3U URLs, falls back to request Host |
| `STATIONCAST_BITRATE` | `128` | MP3 bitrate kbps |
| `STATIONCAST_STATION_NAME` | `StationCast` | Shown in ICY headers and UI |
| `STATIONCAST_STATION_GENRE` | `Various` | ICY genre header |
| `STATIONCAST_LOUDNORM` | `false` | Per-track ffmpeg loudnorm filter |

## Endpoints

Public
- `GET /` public player UI
- `GET /now-playing` JSON
- `GET /now-playing/sse` SSE updates
- `GET /art/:id` album art
- `GET /stream` MP3 stream with ICY metadata
- `GET /stream.pls` PLS playlist
- `GET /stream.m3u` extended M3U
- `GET /hls.m3u8` HLS playlist for iOS / Apple Safari

Admin (requires login)
- `GET /admin/` dashboard
- `POST /admin/skip` advance to next track
- `POST /admin/mode` set mode (shuffle, sequential, loop)
- `POST /admin/queue` enqueue track id
- `POST /admin/queue/remove` remove from queue
- `POST /admin/files/upload` upload audio file
- `POST /admin/files/rename` rename file
- `POST /admin/files/delete` delete file

## Supported formats

mp3, wav, flac, ogg, oga, m4a, aac

The server transcodes everything to a single 128 kbps MP3 output stream so all listeners receive the same bytes
