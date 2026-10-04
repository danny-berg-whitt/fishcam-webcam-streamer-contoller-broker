#!/usr/bin/env bash
# End-to-end test of the whole request chain the Flutter app uses, with all
# three real binaries:
#
#   curl --Bearer--> broker :8082 --HMAC--> controller :8080 --> streamer :8081
#                                                                  |-> ffmpeg
#                                                                  '-> amixer
#
# Only the two programs the streamer drives are stand-ins: an `ffmpeg` that
# idles until stopped and an `amixer` that records what it's asked to do. So
# a mute is checked at the mixer, not just in a response, and killing ffmpeg
# exercises the streamer's restart logic through the public API.
#
# Usage: ci/e2e-smoke.sh <controller-binary> <broker-binary> <streamer-binary>
set -euo pipefail

ctl_bin=$1
brk_bin=$2
str_bin=$3
work=$(mktemp -d)

secret=$(openssl rand -hex 32)
token=$(openssl rand -hex 32)
hash=$(printf '%s' "$token" | sha256sum | awk '{print $1}')
printf '{"%s":"ci"}' "$hash" > "$work/tokens.json"

# --- Stand-ins for the programs the streamer runs -------------------------
mkdir "$work/bin"
cat > "$work/bin/amixer" <<EOF
#!/bin/sh
echo "\$*" >> "$work/amixer.log"
EOF
cat > "$work/bin/ffmpeg" <<EOF
#!/bin/sh
echo "\$\$" > "$work/ffmpeg.pid"
echo "\$*" >> "$work/ffmpeg.args"
trap 'exit 0' INT TERM
while :; do sleep 0.1; done
EOF
chmod +x "$work/bin/amixer" "$work/bin/ffmpeg"
# Exist from the start, so a streamer that never runs fails the checks
# below one by one instead of aborting the script under set -e.
: > "$work/amixer.log"; : > "$work/ffmpeg.args"; : > "$work/ffmpeg.pid"

pids=()
# shellcheck disable=SC2317  # invoked via trap
cleanup() {
  kill "${pids[@]}" 2>/dev/null || true
  wait 2>/dev/null || true
  for log in streamer controller broker; do
    echo "--- $log log"; cat "$work/$log.log" 2>/dev/null || true
  done
  rm -rf "$work"
}
trap cleanup EXIT

# The webcam's devices are given explicitly, which skips discovery (there's
# no camera here); everything else is the streamer's own default.
PATH="$work/bin:$PATH" \
  ALSA_CARD=Webcam MUTE_CONTROL=Mic VIDEO_DEVICE=/dev/video0 \
  LISTEN_ADDR=127.0.0.1:8081 RESTART_INITIAL_BACKOFF=200ms \
  "$str_bin" >"$work/streamer.log" 2>&1 & pids+=($!)
HMAC_SECRET=$secret ROUTE_PREFIX=/webcam LISTEN_ADDR=127.0.0.1:8080 \
  STREAMER_URL=http://127.0.0.1:8081 \
  "$ctl_bin" >"$work/controller.log" 2>&1 & pids+=($!)
HMAC_SECRET=$secret ROUTE_PREFIX=/webcam LISTEN_ADDR=127.0.0.1:8082 \
  CONTROLLER_URL=http://127.0.0.1:8080 TOKENS_FILE="$work/tokens.json" \
  "$brk_bin" >"$work/broker.log" 2>&1 & pids+=($!)

# The streamer's healthz only turns 200 once ffmpeg is running.
for port in 8081 8080 8082; do
  for _ in $(seq 1 40); do
    curl -fs -o /dev/null "http://127.0.0.1:$port/healthz" && break
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
field() { python3 -c "import json,sys; print(json.load(sys.stdin)[\"$1\"])"; }
get()   { curl -fsS -H "$auth" "$base/$1"; }
post()  { curl -fsS -X POST -H "$auth" "$base/$1"; }
code()  { curl -s -o /dev/null -w '%{http_code}' "$@"; }
last_amixer() { tail -n 1 "$work/amixer.log"; }
# wait_for <seconds> <command...>: retry until the command succeeds.
# shellcheck disable=SC2317  # called indirectly, via check
wait_for() {
  local n=$(( $1 * 4 )); shift
  for _ in $(seq 1 "$n"); do "$@" && return 0; sleep 0.25; done
  return 1
}

# --- Start-up ---------------------------------------------------------------
check "streamer sets the mic live at start-up" \
  [ "$(head -n 1 "$work/amixer.log" 2>/dev/null)" = "-q -c Webcam set Mic cap" ]
check "ffmpeg captures the configured devices" \
  grep -q -- "-i /dev/video0 .*-i plughw:CARD=Webcam,DEV=0" "$work/ffmpeg.args"
check "ffmpeg publishes to the default RTMP URL" \
  grep -q -- "rtmp://hls-service.default.svc.cluster.local/live/stream" "$work/ffmpeg.args"
check "status reports streaming, unmuted" \
  [ "$(get status | field streaming)/$(get status | field muted)" = "True/False" ]

# --- Mute and unmute, checked at the mixer ----------------------------------
check "mute reports muted"          [ "$(post mute | field muted)" = True ]
check "mute switched capture off"   [ "$(last_amixer)" = "-q -c Webcam set Mic nocap" ]
check "status reflects mute"        [ "$(get status | field muted)" = True ]
check "unmute reports unmuted"      [ "$(post unmute | field muted)" = False ]
check "unmute switched capture on"  [ "$(last_amixer)" = "-q -c Webcam set Mic cap" ]

# --- The guards in front of it ----------------------------------------------
calls_before=$(wc -l < "$work/amixer.log")
check "no token rejected at broker" [ "$(code -X POST "$base/mute")" = 401 ]
check "GET cannot mute"             [ "$(code -H "$auth" "$base/mute")" = 405 ]
check "rejected calls never reached the mixer" \
  [ "$(wc -l < "$work/amixer.log")" = "$calls_before" ]

ts=$(date +%s); nonce=$(openssl rand -hex 8)
sig=$(printf '%s\n%s\n%s\n%s' POST /webcam/mute "$ts" "$nonce" \
      | openssl dgst -sha256 -hmac "$secret" | awk '{print $NF}')
direct() {
  code -X POST http://127.0.0.1:8080/webcam/mute \
    -H "X-Auth-Timestamp: $ts" -H "X-Auth-Nonce: $nonce" -H "Authorization: HMAC $sig"
}
check "controller accepts a fresh signature"     [ "$(direct)" = 200 ]
check "controller rejects the same one replayed" [ "$(direct)" = 401 ]
post unmute >/dev/null

# --- ffmpeg dies; the streamer restarts it ----------------------------------
first_pid=$(cat "$work/ffmpeg.pid")
kill -9 "$first_pid"
# shellcheck disable=SC2317  # called indirectly, via check
restarted() {
  local pid; pid=$(cat "$work/ffmpeg.pid")
  [ "$pid" != "$first_pid" ] && [ "$(get status | field streaming)" = True ]
}
check "a killed ffmpeg is restarted"  wait_for 10 restarted
check "status counts the restart"     [ "$(get status | field restarts)" = 1 ]
check "mute still works after it"     [ "$(post mute | field muted)" = True ]

exit $fail
