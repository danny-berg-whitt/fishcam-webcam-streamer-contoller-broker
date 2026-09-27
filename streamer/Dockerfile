# ---------- Build stage ----------
# Runs on the build host's native architecture and cross-compiles to the
# target, so an amd64 workstation builds arm64 images at full speed. Alpine
# rather than bookworm because this stage never ships — it is pure tooling,
# and the smaller builder means smaller pulls and smaller orphaned images.
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS build

# Supplied automatically by buildx / podman. Empty for a plain `docker build`,
# in which case the build falls back to the host architecture.
ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

COPY go.mod ./
COPY *.go ./

# CGO off makes this a fully static binary, so it runs unchanged on musl.
RUN CGO_ENABLED=0 \
    GOOS=${TARGETOS:-$(go env GOOS)} \
    GOARCH=${TARGETARCH:-$(go env GOARCH)} \
    go build -trimpath -ldflags="-s -w" -o /out/streamer .

# ---------- Runtime stage ----------
# Alpine rather than debian-slim purely for size. Debian's ffmpeg package
# depends on the whole optional feature surface — LLVM and Mesa for OpenCL,
# flite for speech synthesis, pocketsphinx for speech recognition, SDL2, X11,
# Wayland, JACK — none of which a headless V4L2 -> x264 -> RTMP pipeline
# touches. Alpine splits ffmpeg into per-library packages and builds far less
# maximally.
FROM alpine:3

RUN apk add --no-cache \
      ffmpeg \
      alsa-utils \
      ca-certificates

# Alpine's ffmpeg build options are not guaranteed across releases, and a
# missing encoder would otherwise surface as a dead stream at 3am rather than
# a failed build. Assert every piece the pipeline actually needs.
RUN set -eux; \
    ffmpeg -hide_banner -encoders | grep -q 'libx264'; \
    ffmpeg -hide_banner -encoders | grep -q ' aac '; \
    ffmpeg -hide_banner -devices  | grep -q 'video4linux2'; \
    ffmpeg -hide_banner -devices  | grep -q 'alsa'; \
    ffmpeg -hide_banner -muxers   | grep -q 'flv'; \
    command -v amixer

COPY --from=build /out/streamer /usr/local/bin/streamer

EXPOSE 8081

ENTRYPOINT ["/usr/local/bin/streamer"]
