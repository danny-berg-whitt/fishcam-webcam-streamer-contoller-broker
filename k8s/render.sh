#!/usr/bin/env bash
# Fill a manifest's ${...} placeholders from the environment (deploy.env,
# via make) and print it, or with -o write each into <dir>.
#
# Usage: k8s/render.sh [-o <dir>] <manifest>...
#
# ${WEBCAM_NODE_LABEL_KEY} and ${WEBCAM_NODE_LABEL_VALUE} are the two halves
# of WEBCAM_NODE_LABEL. Only variables a manifest uses are required; each is
# validated against Kubernetes' rules first.
set -euo pipefail

die() { echo "render: $*" >&2; exit 1; }

out=""
if [ "${1:-}" = "-o" ]; then
  out=${2:?render: -o needs a directory}; shift 2
  mkdir -p "$out"
fi
[ $# -gt 0 ] || die "usage: k8s/render.sh [-o <dir>] <manifest>..."

# Patterns are held in variables and used unquoted in [[ =~ ]], which
# behaves the same from bash 3.2 (macOS's /bin/bash) on.
label='[a-z0-9]([-a-z0-9]*[a-z0-9])?'
dns_re="^$label(\\.$label)*\$"
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
    # check() admits only letters, digits and . _ - : / so no sed special
    # character can reach the replacement.
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
