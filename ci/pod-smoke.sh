#!/usr/bin/env bash
# Run the three images as a podman pod wired like k8s/deployment.yaml
# (shared localhost, only 8082 published, the same hardening, secrets and
# ConfigMap) and exercise the API through the broker. With no webcam, the
# image's real ffmpeg and amixer run and fail, proving they're present and
# that failures surface correctly.
#
# Usage: ci/pod-smoke.sh <name> <streamer-image> <controller-image> <broker-image>
# Uses $PODMAN if set (ci/podman-act.sh under act).
set -euo pipefail

name=$1 streamer_img=$2 controller_img=$3 broker_img=$4
podman=${PODMAN:-podman}
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)

# shellcheck disable=SC2317  # invoked via trap
cleanup() {
  for c in streamer controller broker; do
    echo "--- $c log"
    "$podman" logs "$name-$c" 2>&1 | tail -n 40 || true
  done
  "$podman" pod rm -f "$name" >/dev/null 2>&1 || true
  "$podman" secret rm "$name-hmac" "$name-tokens" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

# --- Secrets ---------------------------------------------------------------
token=$(openssl rand -hex 32)
hash=$(printf '%s' "$token" | sha256sum | awk '{print $1}')
printf '{"%s":"ci"}' "$hash" > "$work/tokens.json"
openssl rand -hex 32 | tr -d '\n' > "$work/hmac"
"$podman" secret rm "$name-hmac" "$name-tokens" >/dev/null 2>&1 || true
"$podman" secret create "$name-hmac" "$work/hmac" >/dev/null
"$podman" secret create "$name-tokens" "$work/tokens.json" >/dev/null

# --- The ConfigMap's data, as the streamer's environment --------------------
awk '/^data:/ { d = 1; next }
     d && /^[^ #]/ { d = 0 }
     d && /^  [A-Z_]+:/ {
       key = $1; sub(/:$/, "", key)
       val = $0; sub(/^  [A-Z_]+:[ ]*/, "", val); gsub(/^"|"$/, "", val)
       print key "=" val
     }' "$here/../k8s/configmap.yaml" > "$work/config.env"
grep -q '^ROUTE_PREFIX=/webcam$' "$work/config.env" \
  || { echo "pod-smoke: couldn't read k8s/configmap.yaml" >&2; exit 1; }

# Never pull: a published image would test something other than this tree.
run=(run -d --pull=never --pod "$name")
hardened=(--read-only --user 65534:65534 --cap-drop ALL --security-opt no-new-privileges)

"$podman" pod rm -f "$name" >/dev/null 2>&1 || true
"$podman" pod create --name "$name" -p 8082:8082 >/dev/null

# Explicit devices skip discovery; faster restarts keep the test short.
"$podman" "${run[@]}" --name "$name-streamer" \
  --env-file "$work/config.env" \
  -e ALSA_CARD=Webcam -e MUTE_CONTROL=Mic -e VIDEO_DEVICE=/dev/video0 \
  -e RESTART_INITIAL_BACKOFF=200ms -e RESTART_MAX_BACKOFF=1s \
  "$streamer_img" >/dev/null

"$podman" "${run[@]}" --name "$name-controller" "${hardened[@]}" \
  --secret "$name-hmac,type=env,target=HMAC_SECRET" \
  -e ROUTE_PREFIX=/webcam -e STREAMER_URL=http://127.0.0.1:8081 \
  "$controller_img" >/dev/null

"$podman" "${run[@]}" --name "$name-broker" "${hardened[@]}" \
  --secret "$name-hmac,type=env,target=HMAC_SECRET" \
  --secret "$name-tokens,type=mount,target=/etc/broker/tokens.json,mode=0444" \
  -e ROUTE_PREFIX=/webcam -e CONTROLLER_URL=http://127.0.0.1:8080 \
  "$broker_img" >/dev/null

fail=0
check() {
  local desc=$1; shift
  if "$@"; then echo "ok    $desc"; else echo "FAIL  $desc"; fail=1; fi
}
# shellcheck disable=SC2317  # called indirectly, via check
wait_for() {
  local n=$(( $1 * 4 )); shift
  for _ in $(seq 1 "$n"); do "$@" && return 0; sleep 0.25; done
  return 1
}
base=http://localhost:8082
auth="Authorization: Bearer $token"
code()  { curl -s -o /dev/null -w '%{http_code}' "$@"; }
# shellcheck disable=SC2317  # called indirectly, via restarts_seen
field() { python3 -c "import json,sys; print(json.load(sys.stdin)[\"$1\"])"; }
# shellcheck disable=SC2317  # called indirectly, via check
# Capture first: `podman logs | grep -q` can fail with SIGPIPE under pipefail.
logs_have() {
  local out; out=$("$podman" logs "$name-$1" 2>&1 || true)
  grep -q -- "$2" <<<"$out"
}
# shellcheck disable=SC2317  # called indirectly, via check
restarts_seen() {
  local r; r=$(curl -fsS -H "$auth" "$base/webcam/status" | field restarts) || return 1
  [ "$r" -ge 1 ]
}
# shellcheck disable=SC2317  # called indirectly, via check
broker_up() { [ "$(code "$base/healthz")" = 200 ]; }
# shellcheck disable=SC2317  # called indirectly, via check
running() { [ "$("$podman" inspect -f '{{.State.Running}}' "$name-$1" 2>/dev/null)" = true ]; }

check "broker is up"                       wait_for 30 broker_up
for c in streamer controller broker; do
  check "$c container stays running"       running "$c"
done
check "streamer read the ConfigMap"        logs_have streamer "codec=libx264"
check "status answers through all three"   [ "$(code -H "$auth" "$base/webcam/status")" = 200 ]
check "the image's ffmpeg is supervised"   wait_for 20 restarts_seen
# "exit status" means the binary ran; a missing one fails to start instead.
check "the image's ffmpeg ran and exited"  logs_have streamer "ffmpeg exited after .*err=exit status"

# The real amixer fails (no sound card) and surfaces as a 502.
check "mute failure surfaces as 502"       [ "$(code -X POST -H "$auth" "$base/webcam/mute")" = 502 ]
check "the image's amixer ran"             logs_have streamer "amixer -c Webcam set Mic nocap: exit status"

check "no token rejected"                  [ "$(code -X POST "$base/webcam/mute")" = 401 ]
check "controller isn't published"         [ "$(code http://localhost:8080/healthz)" = 000 ]
check "streamer isn't published"           [ "$(code http://localhost:8081/healthz)" = 000 ]

exit $fail
