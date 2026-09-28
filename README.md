FishCam Webcam Streamer
==

Webcam streaming service for [the Berg-Whitt FishCam](https://fishcam.berg-whitt.com),
running on a single-node MicroK8s cluster on a Raspberry Pi 5.

Three components, all written in Go, deployed as three containers in one pod:

- **Streamer** — supervises an `ffmpeg` process that captures audio and video
  from a USB webcam and publishes H.264/AAC over RTMP to the in-cluster
  nginx-rtmp server, which republishes it as HLS. Restarts `ffmpeg` with
  exponential backoff if it dies, and exposes a small **internal** HTTP API
  for mute control.
- **Controller** — authenticates callers with a time-based HMAC signature
  and a replay-protecting nonce, then forwards mute/unmute and status
  requests to the streamer over pod loopback. No longer public — see
  "Broker" below.
- **Broker** — the public API. Authenticates callers with a per-user bearer
  token, signs the request to the Controller itself, and logs which user did
  what. It is the only component with a public listener.

```
client ──Bearer──▶ Broker :8082 ──HMAC──▶ Controller :8080 ──loopback──▶ Streamer :8081 ──▶ ffmpeg
                                                                                              │ RTMP
                                                                                              ▼
                                                                          hls-service (nginx-rtmp) ──▶ HLS
```

Mute is **audio only**: `amixer` toggles the webcam's ALSA capture switch, so
the microphone goes silent while video keeps flowing and the `ffmpeg` process
is never interrupted. Viewers see no stall or reconnect.

Why a broker in front of the Controller
--

The Controller's HMAC scheme — a single shared secret, signed per-request —
is well suited to a small number of trusted operators calling from a shell,
which is exactly what it was designed for. It's a poor fit for a mobile app
used by named people you may want to add, remove, or hold individually
accountable, because there both the secret and the app's install base are
identity-agnostic. The Broker adds that layer without changing the
Controller at all: it holds `HMAC_SECRET`, one or two people each hold a
bearer token that maps to their name, and only the Broker ever computes a
signature. Revoking one person's access means removing one line from the
Broker's token file; it never touches `HMAC_SECRET` or requires
re-provisioning anyone else.

Why software encoding
--

The Pi 5 has no hardware H.264 encoder (the `h264_v4l2m2m` path available on
the Pi 4 is gone), and these webcams deliver MJPEG rather than H.264, so encoding
is done by `libx264` with `-preset veryfast -tune zerolatency`. 720p10 fits
comfortably on the Pi 5; raise `FRAMERATE` or `VIDEO_SIZE` in the ConfigMap
only as far as CPU headroom allows.

API
--

All three action endpoints require a bearer token and are served by the
**Broker**, not the Controller directly.

| Method | Path      | Description                        |
|--------|-----------|-------------------------------------|
| POST   | `/mute`   | Mute the microphone                |
| POST   | `/unmute` | Unmute the microphone              |
| GET    | `/status` | Stream state, mute state, restarts |
| GET    | `/healthz`| Liveness (unauthenticated, never prefixed) |

Set `ROUTE_PREFIX` and the first three are served beneath it — the cluster
deployment uses `/webcam`, giving `/webcam/mute` and so on. Both the Broker
and the Controller are configured with the same `ROUTE_PREFIX`: the Broker
serves it on the public side for the same reason the Controller always did
(see "Publishing the API" below), and uses it again to build the internal
Controller path it signs and forwards to.

A successful call returns the streamer's current state:

```json
{"streaming":true,"muted":true,"uptime":"4h12m30s","restarts":0}
```

### Authentication

**Public side (client → Broker):** a bearer token.

```
Authorization: Bearer <token>
```

The Broker never stores the raw token — only `sha256(token)` — so a copy of
its token file or a `kubectl get secret` dump is not itself a usable
credential. There's no session and no login step: a token is a person's
identity from the moment it's issued (see "Provisioning a user" below), and
a device holds it indefinitely until it's cleared or revoked.

**Internal side (Broker → Controller):** unchanged HMAC scheme.

```
message   = METHOD "\n" PATH "\n" TIMESTAMP "\n" NONCE
signature = hex(HMAC-SHA256(secret, message))
```

sent as three headers:

```
X-Auth-Timestamp: <unix seconds>
X-Auth-Nonce:     <random hex, unique within the window>
Authorization:    HMAC <signature>
```

Requests are rejected if the timestamp is more than `AUTH_MAX_SKEW` (default
30s) from the server clock, if the nonce has already been used inside that
window, or if the signature does not match. Because the signature covers the
method and path, a captured `/mute` signature cannot be replayed against
`/unmute`. A request that fails signature verification does not consume its
nonce, so a forged request cannot lock out a legitimate one. Only the Broker
ever produces this signature now; `HMAC_SECRET` is shared between the Broker
and Controller containers via the same `webcam-hmac` Secret and never leaves
the pod.

Configuration
--

**Streamer** (`k8s/configmap.yaml`):

| Variable | Default | Notes |
|---|---|---|
| `RTMP_URL` | `rtmp://hls-service.default.svc.cluster.local/live/stream` | Publish target |
| `VIDEO_DEVICE` | `auto` | Lowest `/dev/video*` node, or an explicit path |
| `INPUT_FORMAT` | `mjpeg` | Capture format |
| `VIDEO_SIZE` / `FRAMERATE` | `1280x720` / `10` | |
| `VIDEO_CODEC` | `libx264` | `h264_v4l2m2m` on a Pi 4 |
| `PRESET` / `TUNE` | `veryfast` / `zerolatency` | libx264 only — set both empty for a hardware encoder |
| `VIDEO_BITRATE` / `BUFSIZE` | `2M` / `4M` | |
| `GOP` | `5` | Keyframe interval; sets the floor on HLS segment length |
| `ALSA_CARD` | `auto` | Detected USB capture card, or an explicit id |
| `ALSA_CARD_MATCH` | — | Substring to disambiguate when several mics are attached |
| `AUDIO_DEVICE` | `auto` | Derived as `plughw:CARD=<id>,DEV=0` |
| `AUDIO_SAMPLE_RATE` / `AUDIO_BITRATE` | `44100` / `128k` | |
| `MUTE_CONTROL` | `auto` | The card's only control with a capture switch |
| `START_MUTED` | `false` | |
| `RESTART_INITIAL_BACKOFF` / `RESTART_MAX_BACKOFF` / `RESTART_STABLE_AFTER` | `1s` / `30s` / `1m` | |
| `LISTEN_ADDR` | `:8081` | Internal API |

### Device discovery

The settings that actually differ between machines are the ALSA card and the
video node, so the streamer discovers both at startup and logs what it found:

```
[streamer] detected capture card 2:Webcam (USB-Audio, C922 Pro Stream Webcam)
[streamer] detected video device /dev/video0
[streamer] video=/dev/video0 audio=plughw:CARD=Webcam,DEV=0 card=Webcam codec=libx264
[streamer] detected mute control "Mic" on card Webcam
```

This matters because these names are derived from the hardware, not chosen
for their role, and they are not guessable. The C922 Pro Stream's USB product
string is "C922 Pro Stream Webcam", and `snd-usb-audio` turns that into the
card id `Webcam` — not `C922`. Its one mixer control is called `Mic`, not
`Capture`. Both were wrong in the first version of this project, and both are
now discovered.

Card discovery reads `/proc/asound/cards`, discards cards with no capture PCM
(the Pi's HDMI and headphone outputs), and prefers USB — a webcam microphone
is always USB. With two microphones attached it **refuses to start** rather
than pick one at random; set `ALSA_CARD_MATCH` to a substring like `c270`, or
name the card outright in `ALSA_CARD`.

Mute-control discovery enumerates the card's controls and keeps the ones with
an ALSA capture switch. Unlike the card, this one is not required to stream:
if it fails, the streamer logs a warning and carries on, and only `/mute`
stops working. Any explicit value skips discovery entirely.

### Running on more than one host

The images and manifests are host-independent; only the ConfigMap changes.
For a Pi 4 with a C270, the differences are the encoder and the camera's
capabilities:

```yaml
VIDEO_CODEC: "h264_v4l2m2m"   # the Pi 4 has a hardware encoder; the Pi 5 does not
PRESET: ""                    # libx264-only flags — the hardware encoder rejects them
TUNE: ""
VIDEO_SIZE: "1280x720"        # confirm with v4l2-ctl --list-formats-ext
```

The ALSA card needs no entry at all — discovery handles it.

Two things to check before deploying to a second host. If it runs 32-bit
Raspberry Pi OS, add `linux/arm/v7` to `PLATFORMS`, since arm64 images will
not run. And confirm the Alpine ffmpeg build there has `h264_v4l2m2m`; the
Dockerfile asserts libx264 but not the hardware encoder, as that one is
host-specific.

**Controller**:

| Variable | Default | Notes |
|---|---|---|
| `HMAC_SECRET` | — | Required; at least 32 characters. Shared with the Broker. |
| `ROUTE_PREFIX` | — | e.g. `/webcam`; signed paths include it. Shared with the Broker. |
| `STREAMER_URL` | `http://127.0.0.1:8081` | |
| `AUTH_MAX_SKEW` | `30s` | Accepted clock skew |
| `STREAMER_TIMEOUT` | `5s` | Upstream call timeout |
| `LISTEN_ADDR` | `:8080` | No longer exposed by any Service — loopback only |

**Broker**:

| Variable | Default | Notes |
|---|---|---|
| `HMAC_SECRET` | — | Required; same value as the Controller's |
| `ROUTE_PREFIX` | — | e.g. `/webcam`; served publicly and used to build the signed Controller path |
| `CONTROLLER_URL` | `http://127.0.0.1:8080` | |
| `TOKENS_FILE` | `/etc/broker/tokens.json` | Mounted secret: `sha256(token)` hex → username |
| `UPSTREAM_TIMEOUT` | `5s` | Call timeout to the Controller |
| `LISTEN_ADDR` | `:8082` | Public listener |

Building
--

Images are built for **arm64** — the Pi 5 cluster is the only place this
service runs — and pushed to Docker Hub under `drwhitt/`. Adding **amd64**
(x86 workstations, CI, kind/minikube) is one variable away, and produces a
multi-arch manifest covering both. The same build machinery now also
produces `drwhitt/fishcam-broker`, built the same way as the Controller (see
"Image size" below) — a Go binary with no runtime-stage `RUN`, so it
cross-builds without emulation.

The architecture of the build host does not matter. The Go build stage runs
natively (`FROM --platform=$BUILDPLATFORM`) and cross-compiles to
`$TARGETARCH`, so an x86 laptop produces arm64 images at native speed — no
QEMU emulation of the compiler.

```sh
make login                                     # once per machine
make build                                     # arm64 -> drwhitt/fishcam-*:latest
make build PLATFORMS=linux/arm64,linux/amd64   # both, via emulation
```

`make help` lists every target and the current variable values.

The container engine is auto-detected — `docker` (via buildx) or `podman`
(via `build --manifest` + `manifest push`) — and `make` fails with an
explanation if neither is installed or usable. Force a specific one with
`make build ENGINE=podman`.

With docker, multi-platform builds need buildx's `docker-container` driver,
since the default driver only emits single-arch images; `make` creates that
builder (named `fishcam`) on first use and reuses it afterwards.

**Image size.** The streamer runs on Alpine rather than debian-slim, for one
reason: Debian's `ffmpeg` package depends on its entire optional feature
surface — LLVM and Mesa for OpenCL, `flite` for speech *synthesis*,
`pocketsphinx` for speech *recognition*, SDL2, X11, Wayland, JACK — about
200 packages and ~424 MB, none of which a headless V4L2 → x264 → RTMP
pipeline touches. Alpine splits ffmpeg into per-library packages and builds
far less maximally. The Broker, like the Controller, is `FROM scratch` and
never touches ffmpeg at all, so this doesn't apply to it — it's a single
static binary either way.

Because Alpine's ffmpeg build options aren't guaranteed across releases, the
Dockerfile asserts what the pipeline needs — the libx264 and aac encoders,
the v4l2 and alsa input devices, the flv muxer, and `amixer` — and fails the
build if any is missing. A codec that quietly vanished would otherwise show
up as a dead stream rather than a broken build.

**Emulation.** The Go build stage is never emulated — it runs natively and
cross-compiles. But the streamer's *runtime* stage installs ffmpeg with
`apk`, which executes inside a target-architecture container, so building
arm64 on an x86 host (or the reverse) needs QEMU binfmt handlers:

```sh
make binfmt   # docker: registers them; podman: checks and tells you the package
```

Docker Desktop and `podman machine` already include these. On a Linux host
with podman, install `qemu-user-static`. The Controller and Broker images
are both `FROM scratch` with no `RUN` in their runtime stage, so both
cross-build without emulation either way — and building on the Pi itself
sidesteps the question entirely.

This is why the default is arm64 alone: on an Apple Silicon Mac that pass is
native and quick, while adding amd64 puts the streamer's `apk` install
through QEMU.

**Other registries.** `REGISTRY` and `TLS_VERIFY` cover the alternatives. To
use the MicroK8s built-in registry instead (faster iteration, nothing leaves
the LAN — enable it with `microk8s enable registry`):

```sh
make build REGISTRY=fishcam.local:32000 TLS_VERIFY=false PLATFORMS=linux/arm64
```

It serves plain HTTP, hence `TLS_VERIFY=false` — podman does not trust
unencrypted registries implicitly the way docker trusts `localhost`. The
image references in `k8s/deployment.yaml` would then need to change to
`localhost:32000/...`, which is how the kubelet on the Pi reaches it.

For a quick local smoke test without pushing anything:

```sh
make compile       # cross-compile all three binaries for amd64 and arm64, no engine needed
make build-native  # single image set for this host, tagged :dev, not pushed
```

Or by hand. For a single architecture, plain build and push — this is what
`make build` runs by default, and it reports upload progress per blob:

```sh
podman build --platform linux/arm64 \
  --tag docker.io/drwhitt/fishcam-streamer:latest ./streamer
podman push docker.io/drwhitt/fishcam-streamer:latest
```

Only a genuine multi-arch build needs a manifest list, which podman builds
and pushes in two steps:

```sh
podman build --platform linux/arm64,linux/amd64 \
  --manifest docker.io/drwhitt/fishcam-streamer:latest ./streamer
podman manifest push --all docker.io/drwhitt/fishcam-streamer:latest \
  docker://docker.io/drwhitt/fishcam-streamer:latest
```

`make build` picks between the two automatically based on whether
`PLATFORMS` names more than one target.

With docker, buildx does both at once:

```sh
docker buildx build --platform linux/arm64,linux/amd64 \
  --tag docker.io/drwhitt/fishcam-streamer:latest --push ./streamer
```

Because the tag is mutable (`:latest`), the deployment sets
`imagePullPolicy: Always` so a rollout picks up a freshly pushed image. Use
`make build TAG=$(git rev-parse --short HEAD)` if you would rather pin
versions, and update the manifest to match.

Starting over
--

```sh
make clean       # this project's Go test cache + locally built images
make rebuild     # clean, then rebuild every layer with --no-cache
make distclean   # also Go's global build cache and dangling engine layers
```

`clean` is deliberately project-scoped: it removes the images built from
these Dockerfiles (streamer, controller, and broker) and the cached `go
test` results, and leaves everything else on the machine alone. `distclean`
reaches into caches shared with your other work — it frees real disk space
but makes the next build of *any* project slower.

Neither touches images already pushed to a registry. Delete those through
Docker Hub's web UI.

Deploying
--

`make` finds `kubectl` on its own: a plain `kubectl` if one is on PATH (a
workstation whose `~/.kube/config` points at the cluster), falling back to
`microk8s kubectl` when run on the Pi. Override with `make deploy
KUBECTL=...` for anything else. `make help` prints which one it picked.

```sh
make secret       # generates webcam-hmac with openssl rand -hex 32
make broker-init  # generates webcam-broker-tokens with one user ("admin")
make deploy       # labels the webcam node, then applies the manifests
make status
```

`deploy` labels the node the pod's `nodeSelector` looks for
(`fishcam.berg-whitt.com/webcam=c922`) before applying anything. On a
single-node cluster there is only one candidate, so it labels it; already
labelled is a no-op. With several nodes it stops and asks, since only you
know which one has the camera plugged in:

```sh
kubectl label node <node> fishcam.berg-whitt.com/webcam=c922
```

Run that step alone with `make label-node`. Skipping it entirely leaves the
pod `Pending` — `kubectl describe pod` reports `node(s) didn't match Pod's
node affinity/selector`.

Or by hand:

```sh
kubectl create secret generic webcam-hmac \
  --from-literal=HMAC_SECRET=$(openssl rand -hex 32)
kubectl apply -f k8s/configmap.yaml
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/service.yaml
```

The streamer container runs privileged and mounts `/dev/video0` and
`/dev/snd` from the host — unavoidable for USB device access. The Controller
no longer has a network listener reachable from outside the pod; the
Broker — the only component with a public listener — is hardened in
proportion: its image is `FROM scratch` — one static binary, no shell, no
libc, no package manager — and it runs as uid 65534 with a read-only root
filesystem, no privilege escalation, all capabilities dropped, and the
default seccomp profile. The Controller keeps the same hardening even
though it's now loopback-only, since defense in depth costs nothing here.
Only the Broker's port 8082 is exposed by a Service; the Controller's 8080
and the streamer's 8081 stay inside the pod.

The deployment uses the `Recreate` strategy: there is one physical webcam, so
two pods must never contend for it during a rollout.

For the same reason the pod carries a `nodeSelector` for
`fishcam.berg-whitt.com/webcam=c922`. The camera is attached to one host, so
the pod has to run there; the selector is what makes that explicit. On a
single-node cluster it changes nothing, but if a node is ever added it turns
a baffling runtime failure — ffmpeg dying on a missing `/dev/video0` — into
an obvious `Pending: node(s) didn't match node selector`.

### Provisioning a user

The intended scale here is one or two trusted people, so there's no login
flow — a person's access *is* a 64-character token given to them directly
(over Signal, read aloud, whatever channel you already trust), and the app
just holds it.

```sh
make broker-init                  # first user, defaults to NAME=admin
make broker-add-user NAME=alice   # any additional user (there are only ever one or two)
make broker-list-users            # see who currently has access (no token values shown)
```

Each command prints the raw token exactly once — the Broker never stores it,
only `sha256(token)`, so if you lose it before writing it down there's no
way to recover it; mint a fresh one instead. `broker-add-user` restarts the
broker container to pick up the change, since the token file is only read at
startup.

To revoke someone, edit the secret (`kubectl get secret
webcam-broker-tokens -o jsonpath='{.data.tokens\.json}' | base64 -d`, remove
their entry, re-create the secret) and restart the broker. This never
touches `HMAC_SECRET` or the Controller, and doesn't affect anyone else's
token.

Testing
--

Unit tests cover config parsing, ffmpeg argument construction, the HMAC
scheme (skew, replay, cross-path reuse, forged-nonce protection), the
Controller's proxy behaviour, and the Broker's token lookup and signing:

```sh
make test
```

### Running CI locally with act

Both GitHub Actions workflows (`.github/workflows/`) also run locally with
[act](https://github.com/nektos/act), natively on an arm64 container, so
an Apple Silicon Mac needs no x86 emulation (no Rosetta, no QEMU). With
podman:

```sh
# One-time cleanup if act has previously run here with another architecture
podman rmi -f ghcr.io/catthehacker/ubuntu:act-latest
podman volume rm act-toolcache

act --container-architecture linux/arm64 \
    --container-daemon-socket <socket path inside the podman VM>
```

`--container-architecture` must be `os/arch`. act splits the value on `/`,
so a bare `arm64` never selects arm64: the image comes from whatever
architecture was already pulled. The socket path is the tail of the URI
that `podman system connection list` shows, e.g.
`/run/user/501/podman/podman.sock`.

To confirm the image is really arm64 before running:

```sh
podman run --rm ghcr.io/catthehacker/ubuntu:act-latest \
  sh -c 'uname -m; node -p process.arch'   # expect: aarch64, arm64
```

What differs under act (steps check `env.ACT`, which act sets and GitHub
doesn't):

- Flutter comes from a git checkout of the stable channel
  (`ci/flutter-sdk.sh`) instead of `subosito/flutter-action`, because
  Flutter's Linux release downloads are x64 only. The first run clones it
  and downloads the SDK; the `act-toolcache` volume keeps it for later runs.
- The Android build is skipped: act's image has no Android SDK, and
  Google's Linux Android build tools are x86-64 only.
- `go test` runs without `-race`, since act's image has no C compiler.
- Docker builds use no `type=gha` cache, which needs GitHub's cache service.

The image-building jobs drive podman through `docker buildx`, which is the
least reliable part of this setup. If they fail, run the rest by job:

```sh
act -j broker -j manifests -j go-component -j e2e   # server
act -j analyze-and-test -j build                    # client
```

Publishing the API
--

`make ingress` publishes the Broker at `/webcam/mute`, `/webcam/unmute` and
`/webcam/status`, alongside the cluster's `/hls` and `/fishswitch` routes.
It is a separate target from `deploy` on purpose: exposing the mute API to
the internet is a decision worth making deliberately.

```sh
make ingress
```

**The Broker serves the `/webcam` prefix itself** — `ROUTE_PREFIX` in the
deployment — rather than sitting behind a path-rewriting middleware like its
siblings do, for the same reason the Controller always did: a client's
signed/authenticated request path and the path the server actually receives
have to be identical, or every request fails with a 401 that looks nothing
like a routing problem. The Broker then reuses the same `ROUTE_PREFIX` to
build the path it signs when calling the Controller internally, so the
prefix only needs to be set in one place in the ConfigMap and both
containers pick it up.

`pathType: Exact` keeps `/healthz` off the public internet; it stays at the
root for the kubelet, which probes the pod directly.

Calling it directly (with a user's bearer token):

```sh
curl -X POST https://fishcam.berg-whitt.com/webcam/mute \
  -H "Authorization: Bearer $USER_TOKEN"
```

Neither client needs to know about HMAC signing anymore — that's entirely
internal to the Broker now:

```sh
CONTROLLER=https://fishcam.berg-whitt.com PREFIX=/webcam TOKEN=$USER_TOKEN ./client/webcamctl.sh mute
go run client/webcamctl.go -controller https://fishcam.berg-whitt.com -prefix /webcam -token $USER_TOKEN -action mute
```

**Flutter app** (`client/flutter/`): a small Material app that stores the
user's token in the platform keystore (`flutter_secure_storage`) after
first entry, so it's only asked for once per device.

```
lib/
  main.dart          — app entry point
  home_screen.dart    — status display, mute/unmute/refresh, "switch user"
  webcam_client.dart  — bearer-token HTTP client for the Broker
  token_dialog.dart   — Material dialog that collects and validates the token
  token_storage.dart  — Keychain/Keystore-backed persistence
```

What protects the mute endpoint now: possession of a per-user token that
never touches disk unhashed on the server side, Traefik's rate-limit
middleware, and — one hop in — the same 32-byte shared secret, 30-second
timestamp window, and single-use nonces the Controller always enforced, now
applied by the Broker rather than by whoever's calling. Note also that an
unauthenticated request never reaches the Controller's nonce cache — the
Broker's own token check runs first — so a flood of junk against the public
endpoint cannot grow it.

Against a running deployment, bypassing the Broker to talk to the Controller
directly (useful for isolating whether a problem is in the Broker or
further down):

```sh
kubectl port-forward svc/webcam-broker-service 8082:8082 &

curl -X GET http://localhost:8082/webcam/status \
  -H "Authorization: Bearer $USER_TOKEN"
```

To go one hop further and call the Controller's HMAC API directly (mainly
for debugging the Broker itself — there's no Service for this, so it needs
a pod-level port-forward):

```sh
kubectl port-forward pod/<webcam-pod-name> 8080:8080 &

export HMAC_SECRET=$(kubectl get secret webcam-hmac \
  -o jsonpath='{.data.HMAC_SECRET}' | base64 -d)

TS=$(date +%s); NONCE=$(openssl rand -hex 8)
SIG=$(printf '%s\n%s\n%s\n%s' GET /webcam/status "$TS" "$NONCE" \
      | openssl dgst -sha256 -hmac "$HMAC_SECRET" | awk '{print $NF}')
curl http://localhost:8080/webcam/status \
  -H "X-Auth-Timestamp: $TS" -H "X-Auth-Nonce: $NONCE" -H "Authorization: HMAC $SIG"
```

Troubleshooting
--

**No stream.** Check the streamer logs (`make logs`). `ffmpeg` writes its
errors to stderr, and the supervisor logs every exit and restart. Confirm the
RTMP server is reachable: `kubectl get svc hls-service`.

**Mute returns 500.** The card was found but the control name is wrong for
it. The streamer logs which card it selected at startup; run
`amixer -c <card> scontrols` on the host to list the real control names and
update `MUTE_CONTROL`. Video is unaffected — the streamer logs a warning and
keeps streaming.

**Pod exits with "device discovery failed".** No capture card was found, or
more than one was. `arecord -l` on the host shows what the kernel sees. If
two microphones are attached, set `ALSA_CARD_MATCH` to a substring naming the
one you want, or put its id in `ALSA_CARD`. This failure is deliberate: it
stops at startup with a clear message instead of streaming silence from the
wrong microphone.

**Video works but there is no audio.** First establish whether the stream
carries silence or your player is at fault. `volumedetect` prints at ffmpeg's
*info* level, so do not pass `-v error` or you will see nothing at all:

```sh
ffmpeg -hide_banner -nostats -i https://<host>/hls/stream/index.m3u8 \
  -t 15 -vn -af volumedetect -f null - 2>&1 | grep -E 'mean_volume|max_volume'
```

A `max_volume` of about -91 dB is digital silence: every sample is zero. A
live microphone in a quiet room still floats a noise floor near -60 dB, so
-91 dB means no signal reached the encoder rather than a quiet subject.

If it is silent, work down the stack. Scale the deployment to zero first —
ALSA capture is exclusive, and a running pod holds the device:

```sh
kubectl scale deployment/webcam --replicas=0
kubectl wait --for=delete pod -l app=webcam --timeout=60s
ssh <host> 'arecord -D plughw:CARD=<card>,DEV=0 -f cd -d 5 /tmp/mic.wav'
```

Measure that file the same way. Audio there but silence in the stream puts
the fault in the pipeline. Silence there too puts it below ALSA, and the
mixer is worth ruling out — `amixer -c <card> contents` shows every control,
including any the simplified `scontrols` view merges away. On a C922 there
are only two writable controls, a capture switch and a capture volume.

**Silence with every control reading correctly.** The C922 has been observed
enumerating normally, opening its capture stream normally, reporting
`state: RUNNING` in `/proc/asound/<card>/pcm0c/sub0/status` with `appl_ptr`
tracking `hw_ptr`, showing its capture switch `[on]` at full volume — and
emitting nothing but zero samples regardless. Unplugging the camera and
plugging it back in clears it. Re-enumeration is the fix; no amount of
configuration is, because every setting already reads correct. Record with
the deployment scaled to zero after replugging to confirm before scaling
back up.

**401 from every call (client → Broker).** Usually a wrong or revoked access
code. Re-check with the person who provisioned it, or run `make
broker-list-users` to confirm the token was actually added; if it was
removed or never restarted into the broker container, re-run `kubectl
rollout restart deployment/webcam` after `broker-add-user`.

**401 or 502 from the Broker when calling the Controller internally.**
Usually clock skew between the pod and its own node (rare, but check `date`
inside the pod with `kubectl exec`), or `HMAC_SECRET` drifting out of sync
between the Broker and Controller containers — both read the same
`webcam-hmac` Secret, so this should only happen if one container is
running a stale pod that hasn't picked up a secret rotation; a `kubectl
rollout restart deployment/webcam` resolves it.

**Choppy video.** The Pi 5 is encoding in software. Lower `FRAMERATE`, drop
to `640x480`, or reduce `VIDEO_BITRATE`; watch CPU with
`kubectl top pod -l app=webcam`.
