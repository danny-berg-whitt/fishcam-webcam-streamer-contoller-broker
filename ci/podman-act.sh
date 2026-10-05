#!/usr/bin/env bash
# Run podman inside an act job container against the host's Podman, via the
# socket act mounts at /var/run/docker.sock. Fetches Podman's static remote
# client at the service's own version (cached in act's tool cache).
# Usage: ci/podman-act.sh <podman arguments...>
set -euo pipefail

socket=${PODMAN_ACT_SOCKET:-/var/run/docker.sock}

# Each way the socket mount can fail gets its own message and fix.
fail() { printf 'podman-act: %s\n' "$@" >&2; exit 1; }
selinux_hint="On a podman machine (Fedora CoreOS, SELinux enforcing) the job
  container needs SELinux labelling off to use a bind-mounted socket. Add:
    --container-options \"--security-opt label=disable\""

if ! kind=$(stat -c %F "$socket" 2>&1); then
  case "$kind" in
    *"ermission denied"*)
      fail "$socket is mounted but access is denied ($kind)." "$selinux_hint" ;;
    *)
      fail "nothing at $socket, so act mounted no socket." \
           "Run act with --container-daemon-socket <socket path inside the podman VM>;" \
           "podman system connection list shows it at the end of each URI." ;;
  esac
fi
case "$kind" in
  socket) ;;
  directory)
    fail "$socket is a directory, not a socket: the path given to" \
         "--container-daemon-socket doesn't exist inside the podman VM, so an empty" \
         "directory was mounted in its place. Check it with:" \
         "  podman machine ssh ls -l <that path>" ;;
  *) fail "$socket is a $kind, not a socket." ;;
esac

# The compat /version endpoint lists a "Podman Engine" component on Podman.
if ! reply=$(curl -fsS --unix-socket "$socket" http://d/version 2>&1); then
  case "$reply" in
    *"ermission denied"*) fail "can't connect to $socket ($reply)." "$selinux_hint" ;;
    *) fail "can't talk to the service on $socket: $reply" \
            "Is the Podman service running? (podman machine start)" ;;
  esac
fi
version=$(jq -r '(.Components // [])[] | select(.Name == "Podman Engine") | .Version' <<<"$reply" | head -1)
[ -n "$version" ] || fail "the service on $socket isn't Podman."

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "podman-act: unsupported machine $(uname -m)" >&2; exit 1 ;;
esac

dir="${RUNNER_TOOL_CACHE:-$HOME/.cache}/podman-remote/$version-$arch"
bin="$dir/podman-remote"
if [ ! -x "$bin" ]; then
  echo "podman-act: fetching podman-remote $version ($arch)" >&2
  mkdir -p "$dir"
  curl -fsSL "https://github.com/containers/podman/releases/download/v$version/podman-remote-static-linux_$arch.tar.gz" |
    tar -xzO "bin/podman-remote-static-linux_$arch" > "$bin.partial"
  chmod +x "$bin.partial"
  mv "$bin.partial" "$bin"
fi

exec "$bin" --url "unix://$socket" "$@"
