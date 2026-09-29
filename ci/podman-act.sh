#!/usr/bin/env bash
# Run podman commands from inside an act job container against the host's
# Podman service. Usage: ci/podman-act.sh <podman arguments...>
#
# act's job image has no podman, but act mounts the host's container-engine
# socket at /var/run/docker.sock (see --container-daemon-socket). This
# fetches Podman's own static remote client, at the same version the
# service reports, and points it at that socket. Matching versions avoids
# client/server API drift; the download is cached under RUNNER_TOOL_CACHE,
# which act keeps in its persistent act-toolcache volume.
#
# Everything runs on the host's Podman: images built here land in the
# podman machine's store, and containers started here run beside the job
# container, sharing its (host) network.
set -euo pipefail

socket=${PODMAN_ACT_SOCKET:-/var/run/docker.sock}
if [ ! -S "$socket" ]; then
  echo "podman-act: no socket at $socket; run act with --container-daemon-socket" >&2
  exit 1
fi

# The compat /version endpoint lists a "Podman Engine" component on Podman.
version=$(curl -fsS --unix-socket "$socket" http://d/version |
  jq -r '(.Components // [])[] | select(.Name == "Podman Engine") | .Version' | head -1)
if [ -z "$version" ]; then
  echo "podman-act: the service on $socket isn't Podman" >&2
  exit 1
fi

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
