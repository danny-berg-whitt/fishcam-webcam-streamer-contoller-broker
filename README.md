FishCam Webcam Streamer
==

Webcam streaming service for [the Berg-Whitt FishCam](https://fishcam.berg-whitt.com),
running on MicroK8s on a Raspberry Pi. Three Go services run as containers in
one pod:

- **Streamer** supervises `ffmpeg`, which captures a USB webcam's video and
  audio and publishes H.264/AAC over RTMP to an nginx-rtmp server that serves
  HLS. It restarts `ffmpeg` with exponential backoff and has an internal API
  for muting the microphone.
- **Controller** verifies HMAC-signed requests and forwards them to the
  streamer over pod loopback.
- **Broker** is the public API. It authenticates people by bearer token,
  signs each request for the controller, and logs who did what.

```
client ──Bearer──▶ Broker :8082 ──HMAC──▶ Controller :8080 ──▶ Streamer :8081 ──▶ ffmpeg
                                                                                    │ RTMP
                                                                                    ▼
                                                                         hls-service (nginx-rtmp) ──▶ HLS
```

Muting is audio only: `amixer` switches off the webcam's ALSA capture, so the
stream carries silence while video continues and `ffmpeg` never restarts.

The broker exists so the shared `HMAC_SECRET` never leaves the pod: each
person holds their own token, which can be revoked without touching anyone
else's.

API
--

All endpoints are served by the broker. `/mute`, `/unmute` and `/status`
need a bearer token.

| Method | Path      | Description                        |
|--------|-----------|------------------------------------|
| POST   | `/mute`   | Mute the microphone                |
| POST   | `/unmute` | Unmute the microphone              |
| GET    | `/status` | Stream state, mute state, restarts |
| GET    | `/healthz`| Liveness (unauthenticated, never prefixed) |

With `ROUTE_PREFIX` set (`/webcam` in the ConfigMap), the first three are
served beneath it: `/webcam/mute` and so on. A successful call returns:

```json
{"streaming":true,"muted":true,"uptime":"4h12m30s","restarts":0}
```

### Authentication

**Client → broker:** `Authorization: Bearer <token>`. The broker stores only
`sha256(token)`, so its token file isn't itself a usable credential.

**Broker → controller:**

```
message   = METHOD "\n" PATH "\n" TIMESTAMP "\n" NONCE
signature = hex(HMAC-SHA256(secret, message))

X-Auth-Timestamp: <unix seconds>
X-Auth-Nonce:     <random hex, unique within the window>
Authorization:    HMAC <signature>
```

The controller rejects a timestamp more than `AUTH_MAX_SKEW` (30s) off, a
nonce already seen in that window, or a bad signature. The signature covers
the method and the full prefixed path, so it can't be replayed against
another endpoint, and a forged request doesn't consume the nonce.

Configuration
--

**Streamer** (`k8s/configmap.yaml`):

| Variable | Default | Notes |
|---|---|---|
| `RTMP_URL` | `rtmp://hls-service.default.svc.cluster.local/live/stream` | Publish target |
| `VIDEO_DEVICE` | `auto` | Lowest `/dev/video*` node, or a path |
| `INPUT_FORMAT` | `mjpeg` | |
| `VIDEO_SIZE` / `FRAMERATE` | `1280x720` / `10` | |
| `VIDEO_CODEC` | `libx264` | e.g. `h264_v4l2m2m` on a Pi 4 |
| `PRESET` / `TUNE` | `veryfast` / `zerolatency` | libx264 only; set both to `""` for a hardware encoder |
| `VIDEO_BITRATE` / `BUFSIZE` | `2M` / `4M` | |
| `GOP` | `5` | Keyframe interval, the floor on HLS segment length |
| `ALSA_CARD` | `auto` | The USB capture card, or an id |
| `ALSA_CARD_MATCH` | — | Substring choosing among several microphones |
| `AUDIO_DEVICE` | `auto` | `plughw:CARD=<id>,DEV=0` |
| `AUDIO_SAMPLE_RATE` / `AUDIO_BITRATE` | `44100` / `128k` | |
| `MUTE_CONTROL` | `auto` | The card's only control with a capture switch |
| `START_MUTED` | `false` | |
| `RESTART_INITIAL_BACKOFF` / `RESTART_MAX_BACKOFF` / `RESTART_STABLE_AFTER` | `1s` / `30s` / `1m` | |
| `LISTEN_ADDR` | `:8081` | |

**Controller:**

| Variable | Default | Notes |
|---|---|---|
| `HMAC_SECRET` | — | Required, at least 32 characters; same as the broker's |
| `ROUTE_PREFIX` | — | Same as the broker's |
| `STREAMER_URL` | `http://127.0.0.1:8081` | |
| `AUTH_MAX_SKEW` | `30s` | |
| `STREAMER_TIMEOUT` | `5s` | |
| `LISTEN_ADDR` | `:8080` | Pod-internal |

**Broker:**

| Variable | Default | Notes |
|---|---|---|
| `HMAC_SECRET` | — | Same as the controller's |
| `ROUTE_PREFIX` | — | Served publicly and used for the signed path |
| `CONTROLLER_URL` | `http://127.0.0.1:8080` | |
| `TOKENS_FILE` | `/etc/broker/tokens.json` | `{"<hex sha256(token)>": "<name>"}` |
| `UPSTREAM_TIMEOUT` | `5s` | |
| `LISTEN_ADDR` | `:8082` | |

### Device discovery

ALSA card ids and mixer control names come from each webcam's hardware and
differ between models, so the streamer discovers them and logs the result:

```
[streamer] detected capture card 3:WEBCAM (USB-Audio, C270 HD WEBCAM)
[streamer] detected video device /dev/video0
[streamer] video=/dev/video0 audio=plughw:CARD=WEBCAM,DEV=0 card=WEBCAM codec=libx264
[streamer] detected mute control "Mic" on card WEBCAM
```

Card discovery ignores cards without a capture PCM (such as the Pi's HDMI and
headphone outputs) and prefers USB. With two microphones it refuses to start
rather than guess; set `ALSA_CARD_MATCH` or `ALSA_CARD`. If no mute control is
found, the streamer still streams and only `/mute` fails. Explicit values
skip discovery.

### Other hosts

The images and manifests don't change between hosts, only the ConfigMap. The
Pi 5 has no hardware H.264 encoder, so the default is software encoding
(720p10 fits comfortably). A Pi 4 can use its hardware encoder:

```yaml
VIDEO_CODEC: "h264_v4l2m2m"
PRESET: ""
TUNE: ""
```

Check the camera's formats with `v4l2-ctl --list-formats-ext`. The images
are arm64, so a 32-bit OS needs `PLATFORM=linux/arm/v7` builds. The streamer
image's build checks for libx264 but not for hardware encoders.

Building
--

Images are built with podman and pushed to `<REGISTRY>/fishcam-{streamer,controller,broker}`,
with `REGISTRY` and `TAG` from `deploy.env`.

```sh
make login     # podman login ghcr.io, unless already logged in
make release   # build for linux/arm64 and push
```

`make help` lists all targets and settings. The login password is a classic
token with `write:packages` (from `GHCR_TOKEN`, else prompted). For
reproducible deploys, use an immutable tag, e.g.
`make release deploy TAG=$(git rev-parse --short HEAD)`; the manifests follow
`TAG`.

**Base images.** Go builds use Chainguard's Go image, which is only free as
`latest`, so the Go version floats above `go.mod`'s minimum. The controller
and broker run `FROM scratch`. The streamer runs on Alpine, built from
Alpine's mini root filesystem tarball, which `make build` downloads and
verifies (`streamer/fetch-rootfs.sh`; `ALPINE_BRANCH=v3.24` pins a release).
Alpine's ffmpeg is a fraction of the size of Debian's, and the streamer's
build fails if it lacks any encoder, device or tool the pipeline needs.

**Architectures.** The Go stages cross-compile, but the streamer's runtime
stage runs `apk add` for the target, so build it natively, as on an Apple
Silicon Mac or the Pi, or under emulation.

**Package visibility.** GHCR creates every package as private, and the
cluster pulls without credentials, so make each package public once: on
`https://github.com/users/<owner>/packages/container/package/<name>`, choose
*Package settings → Danger Zone → Change visibility*. GitHub offers no API
for this, and it can't be undone. Public is also the safer choice here: the
images contain only this repository's code and Alpine's packages, while a
pull secret would put a token on the cluster that can read every private
package in your account. `make release` and `make deploy` report any image
that isn't public; `make check-images` runs that check alone.

To keep them private instead, `GHCR_TOKEN=<read:packages token> make
ghcr-pull-secret` creates the `ghcr-pull` secret the deployment refers to,
and `make deploy` then skips the check.

**MicroK8s registry.** To build to the registry on the Pi
(`microk8s enable registry`), which needs no visibility settings:

```sh
make build REGISTRY=<node>:32000
for c in streamer controller broker; do
  podman push --tls-verify=false <node>:32000/fishcam-$c:latest
done
```

Then deploy with `REGISTRY=localhost:32000`, the address the kubelet uses.

The deployment uses `imagePullPolicy: Always`, so a rollout picks up a
re-pushed tag. `make clean` removes local images and the rootfs tarball.

Deploying
--

### Your deployment's settings

```sh
cp deploy.env.example deploy.env   # git-ignored
```

| Setting | Meaning | Example |
|---|---|---|
| `WEBCAM_HOST` | Public hostname of the API | `webcam.example.com` |
| `WEBCAM_NODE_LABEL` | `key=value` label for the node with the webcam | `webcam=attached` |
| `REGISTRY` | Where images are pushed and pulled | `ghcr.io/your-github-user` |
| `TAG` | Image tag | `latest` |
| `WEBCAM_URL` | Optional app server address | `http://localhost:8082` |

`k8s/deployment.yaml` and `k8s/ingress.yaml` are templates. The deploy
targets render them with `k8s/render.sh` into `build/k8s/`, validating each
value first, and apply the result; `make manifests` renders them for
inspection. Command-line values override `deploy.env`.

The app is built from the same file:

```sh
cd client
flutter build apk --dart-define-from-file=../deploy.env
```

A build without it shows that no server is configured and disables its
buttons.

### Deploying to the cluster

`make` uses `kubectl` if installed, else `microk8s kubectl`; override with
`KUBECTL=...`.

```sh
make secret       # create webcam-hmac (never replaces an existing one)
make broker-init  # create webcam-broker-tokens with one user, "admin"
make deploy       # check settings and images, label the node, apply
make status
make logs         # streamer; C=controller or C=broker for the others
```

These can be re-run safely. `make deploy` stops before changing anything if
a setting, secret or image is missing. It labels the node the pod is pinned
to; on a multi-node cluster, name it with `make label-node NODE=<node>`.

The deployment uses the `Recreate` strategy, so two pods never contend for
the one webcam. The streamer runs privileged, which USB device access
requires. The controller and broker run from `scratch` as uid 65534 with a
read-only filesystem, no capabilities, no privilege escalation and the
default seccomp profile. Only the broker's port is exposed by a Service.

### Provisioning a user

Access is a 64-character code given to each person over a channel you
trust; there's no login. There are meant to be one or two users.

```sh
make broker-init                  # first user (NAME=admin by default)
make broker-add-user NAME=alice   # another user
make broker-list-users            # names only
```

Each code is printed once, only after its hash is stored; it can't be
recovered, only replaced. The broker reads the tokens at startup, so after
`broker-add-user`, run `kubectl rollout restart deployment/webcam`.

To revoke someone, remove their entry from the secret (`kubectl get secret
webcam-broker-tokens -o jsonpath='{.data.tokens\.json}' | base64 -d`),
re-create it, and restart the deployment.

Publishing the API
--

```sh
make ingress
```

This publishes `/webcam/mute`, `/webcam/unmute` and `/webcam/status` on
`WEBCAM_HOST`. It's a separate step from `deploy` so that exposing the API
is a deliberate choice. There's no path rewriting: the broker serves the
prefix itself, and `pathType: Exact` keeps `/healthz` unrouted.

```sh
curl -X POST https://$WEBCAM_HOST/webcam/mute -H "Authorization: Bearer $USER_TOKEN"
```

**Health probes.** The broker has readiness and liveness probes; the
controller, liveness only. The streamer's `/healthz` fails whenever ffmpeg is
down, including between restarts, so its liveness probe waits 60s, longer
than `RESTART_MAX_BACKOFF` (CI checks this). It has no readiness probe, which
would cut off the broker just when `/status` should report an outage.

**The Flutter app** (`client/`) keeps the access code in the platform
keystore, so it's entered once per device.

### Debugging the chain

Through the broker, bypassing the ingress:

```sh
kubectl port-forward svc/webcam-broker-service 8082:8082 &
curl http://localhost:8082/webcam/status -H "Authorization: Bearer $USER_TOKEN"
```

Directly to the controller, which has no Service:

```sh
kubectl port-forward pod/<webcam-pod> 8080:8080 &
export HMAC_SECRET=$(kubectl get secret webcam-hmac -o jsonpath='{.data.HMAC_SECRET}' | base64 -d)

TS=$(date +%s); NONCE=$(openssl rand -hex 8)
SIG=$(printf '%s\n%s\n%s\n%s' GET /webcam/status "$TS" "$NONCE" \
      | openssl dgst -sha256 -hmac "$HMAC_SECRET" | awk '{print $NF}')
curl http://localhost:8080/webcam/status \
  -H "X-Auth-Timestamp: $TS" -H "X-Auth-Nonce: $NONCE" -H "Authorization: HMAC $SIG"
```

Testing
--

```sh
make test        # unit tests
make e2e         # the three binaries together
make pod-smoke   # the three images from make build, as a pod
```

- **Unit tests** cover the streamer's configuration, device discovery,
  ffmpeg arguments, restart backoff and API; the controller's HMAC checks,
  forwarding and nonce expiry; and the broker's tokens, signing and
  forwarding.
- **`make e2e`** runs the real binaries with stand-ins for `ffmpeg` and
  `amixer` that record what they're asked to do, so a mute is checked at the
  mixer and a killed `ffmpeg` must be restarted.
- **`make pod-smoke`** runs the images as a podman pod wired like the
  Kubernetes one, using the real ConfigMap. Without a webcam, the image's
  `ffmpeg` and `amixer` run and fail, proving they're present and that
  failures surface through the API. It only uses local images.
- **`ci/check_manifest_refs.py`** catches what a schema check can't: missing
  ConfigMap keys, and a liveness window shorter than the restart backoff.

### Running CI locally with act

Both workflows run under [act](https://github.com/nektos/act) on an arm64
container, so an Apple Silicon Mac needs no x86 emulation. With podman, put
this in `.actrc` (git-ignored):

```
--container-architecture linux/arm64
--container-daemon-socket /run/user/501/podman/podman.sock
--container-options --security-opt label=disable
--platform ubuntu-latest=ghcr.io/catthehacker/ubuntu:act-latest
--bind
--pull=false
```

- The socket path is the end of the URI in `podman system connection list`.
- `label=disable` is needed because the podman machine runs SELinux, which
  blocks the bind-mounted socket.
- `.actrc` lines are split at the first space and quotes aren't stripped, so
  the `--container-options` value is unquoted here.
- Without `--platform`, act uses an image that lacks the jobs' tools.
- `--pull=false` stops parallel jobs re-pulling the runner image, which can
  leave them on different architectures. Pull it once:
  `podman pull --platform linux/arm64 ghcr.io/catthehacker/ubuntu:act-latest`.
- Don't add `--reuse`: act would keep using old containers whatever the
  flags say.

Under act, steps that check `env.ACT` run differently. Flutter comes from a
git checkout (`ci/flutter-sdk.sh`), since its Linux releases are x64-only;
the Android build is skipped; `go test` runs without `-race`; and the image
jobs reach the host's podman through `ci/podman-act.sh`.

Troubleshooting act:

- **A Go toolchain crashes with `SIGSEGV`, or `node` isn't found in a later
  step:** jobs are on mixed architectures. Remove the runner image and any
  `act-` containers, pull the image once as above, and use the `.actrc`.
- **Exit code `-9`:** the podman machine ran out of memory (it gets 2 GiB by
  default). Check with
  `podman machine ssh 'journalctl -k | grep -i oom-kill'`, then raise it with
  `podman machine set --memory 8192` while it's stopped, or limit
  `--concurrent-jobs`.

Run jobs selectively with `-j`, e.g. `act -j broker-image -j pod`.

Troubleshooting
--

**No stream.** Check `make logs`: the supervisor logs every `ffmpeg` exit.
`Failed to resolve hostname hls-service…` means the RTMP server isn't
deployed (`kubectl get svc hls-service`) or `RTMP_URL` is wrong.

**Mute returns 500.** The mixer control is wrong for the card. List the real
names with `amixer -c <card> scontrols` on the host and set `MUTE_CONTROL`.
Video is unaffected.

**Pod exits with "device discovery failed".** No capture card, or more than
one; `arecord -l` on the host shows what the kernel sees. Set
`ALSA_CARD_MATCH` or `ALSA_CARD` to choose.

**Video works but there is no audio.** First check whether the stream itself
is silent. This needs ffmpeg's default info level:

```sh
ffmpeg -hide_banner -nostats -i https://<host>/hls/stream/index.m3u8 \
  -t 15 -vn -af volumedetect -f null - 2>&1 | grep -E 'mean_volume|max_volume'
```

A `max_volume` near -91 dB is digital silence; even a quiet room gives about
-60 dB. If it's silent, record on the host with the deployment scaled down,
since ALSA capture is exclusive:

```sh
kubectl scale deployment/webcam --replicas=0
kubectl wait --for=delete pod -l app=webcam --timeout=60s
ssh <host> 'arecord -D plughw:CARD=<card>,DEV=0 -f cd -d 5 /tmp/mic.wav'
```

Audio in that file means the fault is in the pipeline; silence there too
means it's below ALSA. `amixer -c <card> contents` shows every control.

**Silence with every control reading correctly.** A C922 has been seen to
stream nothing but zeros while reporting `state: RUNNING` and its capture
switch on at full volume. Unplugging and replugging the camera fixes it;
configuration can't.

**401 from the broker.** The access code is wrong or revoked, or the broker
wasn't restarted after `broker-add-user`. `make broker-list-users` shows who
has access.

**401 or 502 between broker and controller.** Clock skew within the pod, or
a container still holding an old `HMAC_SECRET` after rotation; `kubectl
rollout restart deployment/webcam` resolves the latter.

**Choppy video.** Encoding is in software. Lower `FRAMERATE`, `VIDEO_SIZE`
or `VIDEO_BITRATE`, and watch CPU with `kubectl top pod -l app=webcam`.
