#!/usr/bin/env bash
# Run `flutter` from a git checkout of the stable channel, cloning it on
# first use. Usage: ci/flutter-sdk.sh <flutter arguments...>
#
# This is what the client workflow uses under act on an arm64 Linux
# container (for example on Apple Silicon with no x86 emulation). The
# workflow normally installs Flutter with subosito/flutter-action, which
# downloads Flutter's release archives, and those are x64-only on Linux. A
# git checkout instead fetches a Dart SDK and engine artifacts for whatever
# architecture it runs on.
#
# The checkout lives under RUNNER_TOOL_CACHE, which act keeps in a
# persistent volume (act-toolcache), so only the first run pays for the
# clone and the SDK download.
set -euo pipefail

root="${RUNNER_TOOL_CACHE:-$HOME/.cache}/flutter-git/stable"

if [ ! -x "$root/bin/flutter" ]; then
  echo "Cloning Flutter (stable) into $root" >&2
  mkdir -p "$(dirname "$root")"
  # A blobless clone keeps full history and tags, which flutter needs to
  # work out its own version. A --depth 1 clone reports 0.0.0-unknown and
  # then fails pub's SDK constraints.
  git clone --quiet --filter=blob:none --branch stable \
    https://github.com/flutter/flutter.git "$root"
fi

export PATH="$root/bin:$PATH"
exec flutter "$@"
