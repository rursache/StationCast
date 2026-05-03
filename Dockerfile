FROM golang:1-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/stationcast \
    ./cmd/stationcast

FROM alpine:3.20

RUN apk add --no-cache ffmpeg ca-certificates tzdata su-exec && \
    addgroup -S app && adduser -S -G app -H -h /app app && \
    mkdir -p /music /data && chown -R app:app /music /data

COPY --from=build /out/stationcast /usr/local/bin/stationcast
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

WORKDIR /app

ENV STATIONCAST_MUSIC_DIR=/music \
    STATIONCAST_DATA_DIR=/data \
    STATIONCAST_ADDR=:8000

EXPOSE 8000
VOLUME ["/music", "/data"]

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["/usr/local/bin/stationcast"]
