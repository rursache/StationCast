# syntax=docker/dockerfile:1.7

# Run the Go build on the runner's native architecture and let Go cross-
# compile to TARGETARCH. CGO is off so this needs no toolchain. Avoiding
# QEMU here drops the multi-arch build from ~12 minutes to a couple
FROM --platform=$BUILDPLATFORM golang:1-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -trimpath \
    -ldflags="-s -w -X main.version=$VERSION" \
    -o /out/stationcast \
    ./cmd/stationcast

FROM alpine:3.20

RUN apk add --no-cache ffmpeg ca-certificates tzdata su-exec shadow && \
    addgroup -S app && adduser -S -G app -H -h /app app && \
    mkdir -p /music /data && chown -R app:app /music /data

COPY --from=build /out/stationcast /usr/local/bin/stationcast
COPY --chmod=0755 docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

WORKDIR /app

ENV STATIONCAST_MUSIC_DIR=/music \
    STATIONCAST_DATA_DIR=/data \
    STATIONCAST_ADDR=:8000

EXPOSE 8000
VOLUME ["/music", "/data"]

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["/usr/local/bin/stationcast"]
