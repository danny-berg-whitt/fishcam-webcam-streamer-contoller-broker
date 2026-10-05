#!/usr/bin/env bash
# Run `flutter` from a git checkout of stable, cloned on first use into the
# tool cache. Used under act on arm64 Linux, where Flutter's release archives
# (x64-only) won't run; a checkout fetches artifacts for its own architecture.
# Usage: ci/flutter-sdk.sh <flutter arguments...>
set -euo pipefail

root="${RUNNER_TOOL_CACHE:-$HOME/.cache}/flutter-git/stable"

if [ ! -x "$root/bin/flutter" ]; then
  echo "Cloning Flutter (stable) into $root" >&2
  mkdir -p "$(dirname "$root")"
  # Blobless, not shallow: flutter needs the tags to know its own version.
  git clone --quiet --filter=blob:none --branch stable \
    https://github.com/flutter/flutter.git "$root"
fi

export PATH="$root/bin:$PATH"
exec flutter "$@"
