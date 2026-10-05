#!/usr/bin/env bash
# Check the cluster can pull each image from GHCR without credentials, using
# the kubelet's own anonymous requests: a pull token (401 if the package is
# private) then the manifest (404 if the tag is missing). GitHub has no API
# for package visibility, so a private package gets a link to its settings.
#
# Usage: k8s/check-images.sh <registry> <tag> <component>...
# Other registries are skipped. GHCR_URL overrides the base URL (tests).
set -euo pipefail

registry=${1:?usage: k8s/check-images.sh <registry> <tag> <component>...}
tag=${2:?usage: k8s/check-images.sh <registry> <tag> <component>...}
shift 2

case $registry in
  ghcr.io/*) owner=${registry#ghcr.io/} ;;
  *) echo "check-images: $registry isn't GHCR; skipped."; exit 0 ;;
esac
base=${GHCR_URL:-https://ghcr.io}

# Multi- or single-arch manifests, OCI or Docker format.
accept="application/vnd.oci.image.index.v1+json,application/vnd.oci.image.manifest.v1+json"
accept="$accept,application/vnd.docker.distribution.manifest.list.v2+json"
accept="$accept,application/vnd.docker.distribution.manifest.v2+json"

fail=0
for component in "$@"; do
  repo="$owner/fishcam-$component"
  ref="ghcr.io/$repo:$tag"
  # The html_url format GitHub's REST API returns.
  page="https://github.com/users/$owner/packages/container/package/fishcam-$component"

  body=$(curl -sS -w '\n%{http_code}' "$base/token?scope=repository:$repo:pull&service=ghcr.io") \
    || { echo "FAIL  $ref: couldn't reach $base"; fail=1; continue; }
  code=${body##*$'\n'}
  if [ "$code" != 200 ]; then
    echo "FAIL  $ref isn't public (or doesn't exist): GHCR refused an anonymous pull ($code)."
    echo "      To make it public: open $page,"
    echo "      then Package settings -> Danger Zone -> Change visibility -> Public."
    fail=1; continue
  fi
  token=$(sed -n 's/.*"token"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' <<<"${body%$'\n'*}")

  code=$(curl -sS -o /dev/null -w '%{http_code}' -I \
    -H "Authorization: Bearer $token" -H "Accept: $accept" \
    "$base/v2/$repo/manifests/$tag") || code=000
  case $code in
    200) echo "ok    $ref is public" ;;
    404) echo "FAIL  $ref: the package is public but has no '$tag' tag; run make release."; fail=1 ;;
    *)   echo "FAIL  $ref: unexpected HTTP $code fetching the manifest."; fail=1 ;;
  esac
done
exit $fail
