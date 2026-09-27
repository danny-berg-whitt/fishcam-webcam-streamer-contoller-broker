#!/usr/bin/env bash
# End-to-end smoke test of the request chain the Flutter app uses:
#
#   curl --Bearer--> broker :8082 --HMAC--> controller :8080 --> stub streamer :8081
#
# Runs the real broker and controller binaries against ci/stub_streamer.py.
# Unit tests fake the neighbour on each side; this checks that the broker's
# signing and the controller's verification actually agree.
#
# Usage: ci/e2e-smoke.sh <controller-binary> <broker-binary>
set -euo pipefail

ctl_bin=$1
brk_bin=$2
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)

secret=$(openssl rand -hex 32)
token=$(openssl rand -hex 32)
hash=$(printf '%s' "$token" | sha256sum | awk '{print $1}')
printf '{"%s":"ci"}' "$hash" > "$work/tokens.json"

pids=()
# shellcheck disable=SC2317  # invoked via trap
cleanup() {
  kill "${pids[@]}" 2>/dev/null || true
  echo "--- broker log";     cat "$work/broker.log"     2>/dev/null || true
  echo "--- controller log"; cat "$work/controller.log" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

python3 "$here/stub_streamer.py" & pids+=($!)
HMAC_SECRET=$secret ROUTE_PREFIX=/webcam LISTEN_ADDR=127.0.0.1:8080 \
  STREAMER_URL=http://127.0.0.1:8081 \
  "$ctl_bin" >"$work/controller.log" 2>&1 & pids+=($!)
HMAC_SECRET=$secret ROUTE_PREFIX=/webcam LISTEN_ADDR=127.0.0.1:8082 \
  CONTROLLER_URL=http://127.0.0.1:8080 TOKENS_FILE="$work/tokens.json" \
  "$brk_bin" >"$work/broker.log" 2>&1 & pids+=($!)

for port in 8081 8080 8082; do
  for _ in $(seq 1 40); do
    curl -s -o /dev/null "http://127.0.0.1:$port/healthz" && break
    sleep 0.25
  done
done

fail=0
# check <description> <command...>: passes if the command succeeds.
check() {
  local desc=$1; shift
  if "$@"; then echo "ok    $desc"; else echo "FAIL  $desc"; fail=1; fi
}

base=http://127.0.0.1:8082/webcam
auth="Authorization: Bearer $token"
muted() { curl -fsS "$@" | python3 -c 'import json,sys; print(json.load(sys.stdin)["muted"])'; }
code()  { curl -s -o /dev/null -w '%{http_code}' "$@"; }

check "status starts unmuted"       [ "$(muted -H "$auth" "$base/status")" = False ]
check "mute reaches streamer"       [ "$(muted -X POST -H "$auth" "$base/mute")" = True ]
check "status reflects mute"        [ "$(muted -H "$auth" "$base/status")" = True ]
check "unmute reaches streamer"     [ "$(muted -X POST -H "$auth" "$base/unmute")" = False ]
check "no token rejected at broker" [ "$(code -X POST "$base/mute")" = 401 ]
check "GET cannot mute"             [ "$(code -H "$auth" "$base/mute")" = 405 ]

# The controller must refuse a replayed signature even though it's valid.
ts=$(date +%s); nonce=$(openssl rand -hex 8)
sig=$(printf '%s\n%s\n%s\n%s' POST /webcam/mute "$ts" "$nonce" \
      | openssl dgst -sha256 -hmac "$secret" | awk '{print $NF}')
direct() {
  code -X POST http://127.0.0.1:8080/webcam/mute \
    -H "X-Auth-Timestamp: $ts" -H "X-Auth-Nonce: $nonce" -H "Authorization: HMAC $sig"
}
check "controller accepts a fresh signature"    [ "$(direct)" = 200 ]
check "controller rejects the same one replayed" [ "$(direct)" = 401 ]

exit $fail
