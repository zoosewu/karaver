# syntax=docker/dockerfile:1

# Build stages run on the build machine's native platform and cross-compile
# (the frontend is arch-independent, and the Go binary is pure Go with CGO
# disabled). Only the default image's apt-get install runs as the target arch.

FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS server
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
COPY --from=web /web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/zkaraver .

# Tag "slim": Alpine, ~30 MB, without key change (no ffmpeg). Build it with
#   docker build --target slim .   (or ZKARAVER_TARGET=slim with docker-compose.build.yml)
FROM alpine:3.22 AS slim
COPY --from=server /out/zkaraver /usr/local/bin/zkaraver
ENV LISTEN=:8080 DATA_DIR=/data MEDIA_DIR=/media
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["zkaraver", "healthcheck"]
ENTRYPOINT ["zkaraver"]

# Default image (tag "latest"): with ffmpeg + Rubber Band, which renders songs at
# other keys ahead of time (server/internal/keys). Debian, because Alpine's ffmpeg
# is built without librubberband. Has a shell for troubleshooting
# (`docker compose exec zkaraver sh`).
# Runs as root so a bind-mounted ./data that Docker creates (root-owned) is writable;
# set `user:` in compose to run as another uid if the directory is owned accordingly.
FROM debian:bookworm-slim AS full
# DEBIAN_MIRROR (e.g. http://ftp.tw.debian.org/debian) speeds up slow downloads.
ARG DEBIAN_MIRROR=
RUN if [ -n "$DEBIAN_MIRROR" ]; then \
      sed -i "s|URIs: http://deb.debian.org/debian\$|URIs: $DEBIAN_MIRROR|" /etc/apt/sources.list.d/debian.sources; \
    fi \
    && apt-get update && apt-get install -y --no-install-recommends ffmpeg \
    && rm -rf /var/lib/apt/lists/*
COPY --from=server /out/zkaraver /usr/local/bin/zkaraver
ENV LISTEN=:8080 DATA_DIR=/data MEDIA_DIR=/media
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["zkaraver", "healthcheck"]
ENTRYPOINT ["zkaraver"]
