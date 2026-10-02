# syntax=docker/dockerfile:1

# Build stages run on the build machine's native platform and cross-compile,
# so multi-arch images don't need QEMU emulation (the frontend is arch-independent,
# and the Go binary is pure Go with CGO disabled).

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
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/karaver .

# distroless/static: no shell, no package manager. Runs as root so a bind-mounted
# ./data that Docker creates (root-owned) is writable; set `user:` in compose to
# run as another uid if the directory is owned accordingly.
FROM gcr.io/distroless/static-debian12
COPY --from=server /out/karaver /karaver
ENV LISTEN=:8080 DATA_DIR=/data MEDIA_DIR=/media
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/karaver", "healthcheck"]
ENTRYPOINT ["/karaver"]
