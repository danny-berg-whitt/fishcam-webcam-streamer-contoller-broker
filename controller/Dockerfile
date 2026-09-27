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

RUN CGO_ENABLED=0 \
    GOOS=${TARGETOS:-$(go env GOOS)} \
    GOARCH=${TARGETARCH:-$(go env GOARCH)} \
    go build -trimpath -ldflags="-s -w" -o /out/controller .

# ---------- Runtime stage ----------
# Nothing but the statically linked binary: no shell, no libc, no package
# manager, no third-party base image. The controller is CGO-free and its only
# outbound call is plain HTTP to the streamer over pod loopback, so it needs
# neither a CA bundle nor tzdata (log timestamps are UTC without it).
#
# If it ever needs to reach an external HTTPS endpoint, add:
#   COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
FROM scratch

COPY --from=build /out/controller /usr/local/bin/controller

# nobody:nogroup. Numeric because scratch has no /etc/passwd to resolve names.
USER 65534:65534

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/controller"]
