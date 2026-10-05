#!/usr/bin/env bash
# Fill in a manifest's ${...} placeholders from the environment and print it.
#
# Usage: k8s/render.sh [-o <dir>] <manifest>...
#   Without -o, the rendered manifests go to stdout as one YAML stream.
#   With -o, each is written to <dir>/<file name>.
#
# Placeholders and the variables they come from (normally deploy.env, which
# the Makefile loads and exports):
#   ${WEBCAM_HOST}              WEBCAM_HOST
#   ${WEBCAM_NODE_LABEL_KEY}    WEBCAM_NODE_LABEL, the part before '='
#   ${WEBCAM_NODE_LABEL_VALUE}  WEBCAM_NODE_LABEL, the part after '='
#   ${REGISTRY}  ${TAG}         REGISTRY, TAG
#
# Only the variables a manifest actually uses are required, and each is
# checked against what Kubernetes accepts, so a missing or malformed value
# stops here with its name rather than reaching the cluster.
set -euo pipefail

die() { echo "render: $*" >&2; exit 1; }

out=""
if [ "${1:-}" = "-o" ]; then
  out=${2:?render: -o needs a directory}; shift 2
  mkdir -p "$out"
fi
[ $# -gt 0 ] || die "usage: k8s/render.sh [-o <dir>] <manifest>..."

# Patterns are kept in variables and used unquoted in [[ =~ ]], the form
# that behaves the same from bash 3.2 (macOS's /bin/bash) onwards.
# One DNS label: lowercase alphanumerics and '-', not starting or ending with '-'.
label='[a-z0-9]([-a-z0-9]*[a-z0-9])?'
dns_re="^$label(\\.$label)*\$"
# A label key's name part, and a label value (which may also be empty).
name_re='^[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$'
tag_re='^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$'
registry_re='^[a-z0-9][a-z0-9._:/-]*$'

value_of() {
  case $1 in
    WEBCAM_NODE_LABEL_KEY|WEBCAM_NODE_LABEL_VALUE)
      local l=${WEBCAM_NODE_LABEL:-}
      [ -n "$l" ] || die "WEBCAM_NODE_LABEL is not set (see deploy.env.example)"
      [[ $l == *=* ]] || die "WEBCAM_NODE_LABEL must be key=value, got '$l'"
      if [ "$1" = WEBCAM_NODE_LABEL_KEY ]; then echo "${l%%=*}"; else echo "${l#*=}"; fi ;;
    *)
      local v=${!1:-}
      [ -n "$v" ] || die "$1 is not set (see deploy.env.example)"
      echo "$v" ;;
  esac
}

check() {
  local var=$1 v=$2
  case $var in
    WEBCAM_HOST)
      if ! { [[ $v =~ $dns_re ]] && [ ${#v} -le 253 ]; }; then
        die "WEBCAM_HOST '$v' isn't a valid lowercase hostname"
      fi ;;
    WEBCAM_NODE_LABEL_KEY)
      # Optional DNS-subdomain prefix and '/', then the name.
      local prefix="" key_name=$v
      if [[ $v == */* ]]; then prefix=${v%/*}; key_name=${v##*/}; fi
      if [ -n "$prefix" ]; then
        if ! { [[ $prefix =~ $dns_re ]] && [ ${#prefix} -le 253 ]; }; then
          die "node label key prefix '$prefix' isn't a valid DNS subdomain"
        fi
      fi
      [[ $key_name =~ $name_re ]] \
        || die "node label key '$v' is invalid: the name after any '/' must be 1-63 letters, digits, '-', '_' or '.', starting and ending alphanumeric" ;;
    WEBCAM_NODE_LABEL_VALUE)
      [ -z "$v" ] || [[ $v =~ $name_re ]] \
        || die "node label value '$v' is invalid: up to 63 letters, digits, '-', '_' or '.', starting and ending alphanumeric" ;;
    TAG)
      [[ $v =~ $tag_re ]] || die "TAG '$v' isn't a valid image tag" ;;
    REGISTRY)
      if ! { [[ $v =~ $registry_re ]] && [[ $v != */ ]]; }; then
        die "REGISTRY '$v' isn't a valid registry path (lowercase, no trailing '/')"
      fi ;;
  esac
}

first=1
for file in "$@"; do
  text=$(<"$file")
  for var in WEBCAM_HOST WEBCAM_NODE_LABEL_KEY WEBCAM_NODE_LABEL_VALUE REGISTRY TAG; do
    placeholder="\${$var}"
    [[ $text == *"$placeholder"* ]] || continue
    value=$(value_of "$var")
    check "$var" "$value"
    # check() limits every value to letters, digits and . _ - : / so none of
    # sed's special characters (| & \\ newline) can reach the replacement.
    text=$(printf '%s\n' "$text" | sed "s|\\\${$var}|$value|g")
  done
  if leftover=$(grep -oE '\$\{[A-Za-z_][A-Za-z0-9_]*\}' <<<"$text" | sort -u | tr '\n' ' ') && [ -n "$leftover" ]; then
    die "$file: unknown placeholder(s): $leftover"
  fi
  if [ -n "$out" ]; then
    printf '%s\n' "$text" > "$out/$(basename "$file")"
  else
    [ $first -eq 1 ] || echo "---"
    printf '%s\n' "$text"
  fi
  first=0
done
