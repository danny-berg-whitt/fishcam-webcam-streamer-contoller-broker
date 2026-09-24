# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/broker .

# Same rationale as the Controller: no shell, no libc, no package manager,
# nothing an attacker could use if this process were ever compromised.
FROM scratch
COPY --from=build /out/broker /broker
USER 65534:65534
ENTRYPOINT ["/broker"]
