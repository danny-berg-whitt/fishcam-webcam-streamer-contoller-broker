#!/usr/bin/env bash
# Download Alpine's mini root filesystem for the streamer image and verify
# it against Alpine's published SHA-256.
#
#   streamer/fetch-rootfs.sh [amd64|arm64 ...]    (default: this machine's)
#
# ALPINE_BRANCH (default latest-stable) pins a release, e.g. v3.22.
set -euo pipefail

branch=${ALPINE_BRANCH:-latest-stable}
mirror=${ALPINE_MIRROR:-https://dl-cdn.alpinelinux.org/alpine}
dest="$(cd "$(dirname "$0")" && pwd)/rootfs"

# TARGETARCH name -> Alpine's.
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

# Print "<file> <sha256>" for the minirootfs entry of latest-releases.yaml.
# Entries are a lone "-" line then two-space-indented "key: value" lines; the
# multi-line desc is indented further, so the anchored patterns skip it.
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
