#!/usr/bin/env bash
# Version helpers for the packaging scripts (source it).
#   lumino_version  display version: $VERSION, else git describe, without "v"
#   pkg_version V   Debian/RPM-safe version that sorts correctly:
#                     1.2.0           → 1.2.0
#                     1.2.0-rc1       → 1.2.0~rc1          (before 1.2.0)
#                     1.2.0-3-gabc123 → 1.2.0+git3.gabc123 (after 1.2.0)
#                     abc1234[-dirty] → 0.0.0~gitabc1234   (no tag yet)
#   num_version V   numeric a.b.c for Windows/macOS resources (0.0.0 if none)

lumino_version() {
  local v="${VERSION:-}"
  if [[ -z "$v" ]]; then
    v="$(git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)"
  fi
  echo "${v#v}"
}

pkg_version() {
  local v="${1#v}" dirty="" base rest
  if [[ "$v" == *-dirty ]]; then
    v="${v%-dirty}"
    dirty=".dirty"
  fi
  if [[ ! "$v" =~ ^[0-9]+(\.[0-9]+)*($|-) ]]; then
    echo "0.0.0~git${v//[^A-Za-z0-9.]/.}${dirty}"
    return
  fi
  base="${v%%-*}"
  rest="${v#"$base"}"
  rest="${rest#-}"
  if [[ "$rest" =~ ^(.*-)?([0-9]+)-g([0-9a-f]+)$ ]]; then
    local pre="${BASH_REMATCH[1]%-}" n="${BASH_REMATCH[2]}" h="${BASH_REMATCH[3]}"
    [[ -n "$pre" ]] && base="${base}~${pre//-/.}"
    echo "${base}+git${n}.g${h}${dirty}"
  elif [[ -n "$rest" ]]; then
    echo "${base}~${rest//-/.}${dirty}"
  else
    echo "${base}${dirty:+~local${dirty}}"
  fi
}

num_version() {
  local v="${1#v}"
  if [[ "$v" =~ ^([0-9]+(\.[0-9]+){0,2}) ]]; then
    echo "${BASH_REMATCH[1]}"
  else
    echo "0.0.0"
  fi
}
