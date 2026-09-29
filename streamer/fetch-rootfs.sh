#!/usr/bin/env bash
# Download Alpine's official mini root filesystem for the streamer image and
# verify it against the SHA-256 checksum Alpine publishes alongside it.
#
#   streamer/fetch-rootfs.sh [amd64|arm64 ...]    (default: this machine's)
#
# The Containerfile builds its runtime stage from this tarball with ADD
# instead of pulling an `alpine` image, so the image has no dependency on
# Docker Hub or any Docker-published base image. Everything after that is
# ordinary Alpine: `apk add` installs from Alpine's own package mirrors.
#
# ALPINE_BRANCH picks the release branch (default latest-stable, the same
# thing the old `alpine:3` tag tracked). Pin it for reproducible builds,
# e.g. ALPINE_BRANCH=v3.22.
set -euo pipefail

branch=${ALPINE_BRANCH:-latest-stable}
mirror=${ALPINE_MIRROR:-https://dl-cdn.alpinelinux.org/alpine}
dest="$(cd "$(dirname "$0")" && pwd)/rootfs"

# Map a container architecture name (as in TARGETARCH) to Alpine's name.
alpine_arch() {
  case "$1" in
    amd64 | x86_64) echo x86_64 ;;
    arm64 | aarch64) echo aarch64 ;;
    *) echo "unsupported architecture: $1" >&2; return 1 ;;
  esac
}

# Container architecture name for this machine.
native_arch() {
  case "$(uname -m)" in
    x86_64 | amd64) echo amd64 ;;
    aarch64 | arm64) echo arm64 ;;
    *) echo "unsupported machine: $(uname -m)" >&2; return 1 ;;
  esac
}

sha256_of() {
  if command -v sha256sum >/dev/null; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}' # macOS
  fi
}

# Print "<file> <sha256>" for the minirootfs entry of a latest-releases.yaml.
# Alpine's scripts/mkimage-yaml.sh writes each entry as a lone "-" line
# followed by two-space-indented "key: value" lines (the multi-line desc is
# indented further, so the anchored patterns below never match inside it).
minirootfs_entry() {
  awk '
    function flush() {
      if (flavor == "alpine-minirootfs" && file ~ /^alpine-minirootfs-.*\.tar\.gz$/ && sum != "")
        print file, sum
      flavor = file = sum = ""
    }
    /^-[[:space:]]*$/ { flush(); next }
    /^  flavor: /     { flavor = $2 }
    /^  file: /       { file = $2 }
    /^  sha256: /     { sum = $2 }
    END               { flush() }
  '
}

[ $# -gt 0 ] || set -- "$(native_arch)"
mkdir -p "$dest"

for arch in "$@"; do
  a=$(alpine_arch "$arch")
  base="$mirror/$branch/releases/$a"

  read -r file want < <(curl -fsSL "$base/latest-releases.yaml" | minirootfs_entry)
  if [ -z "${file:-}" ] || [ -z "${want:-}" ]; then
    echo "no minirootfs entry in $base/latest-releases.yaml" >&2
    exit 1
  fi

  out="$dest/alpine-minirootfs-$arch.tar.gz"
  if [ -f "$out" ] && [ "$(cat "$out.name" 2>/dev/null)" = "$file" ] &&
     [ "$(sha256_of "$out")" = "$want" ]; then
    echo "$file already present and verified"
    continue
  fi

  tmp="$out.partial"
  curl -fsSL "$base/$file" -o "$tmp"
  got=$(sha256_of "$tmp")
  if [ "$got" != "$want" ]; then
    rm -f "$tmp"
    echo "checksum mismatch for $file: got $got, want $want" >&2
    exit 1
  fi
  mv "$tmp" "$out"
  echo "$file" > "$out.name"
  echo "fetched $file ($arch), sha256 verified"
done
