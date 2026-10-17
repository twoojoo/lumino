#!/usr/bin/env bash
# Writes THIRD_PARTY_NOTICES.txt (default dist/THIRD_PARTY_NOTICES.txt): the
# licenses of everything linked into lumino, the vendored Gio with a list of
# lumino's modifications, and where MPL-2.0 sources are available.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
OUT="${1:-dist/THIRD_PARTY_NOTICES.txt}"
GO_LICENSES="github.com/google/go-licenses/v2@v2.0.1"
mkdir -p "$(dirname "$OUT")"

# shellcheck disable=SC2016 # literal ${…} in the sed pattern
GIO_VERSION="$(sed -n 's/^GIO_VERSION="\${GIO_VERSION:-\(.*\)}"/\1/p' scripts/vendor-gio.sh)"

# modules whose license go-licenses doesn't recognize: "module|license"
MANUAL=("github.com/segmentio/asm|MIT-0")

patches() { for p in "$1"/*.patch; do echo "  - $(basename "$p")"; done; }

{
  cat <<HDR
Lumino — third-party notices
============================

Lumino includes software developed by third parties under the licenses
reproduced below. None of them is copyleft for Lumino's own code; files
under the MPL-2.0 are used unmodified.

Modified components
-------------------
* Gio $GIO_VERSION (https://gioui.org, used under the Unlicense):
  vendored in third_party/gio with these changes:
$(patches third_party/_gio-patches)

Code derived from usql (https://github.com/xo/usql, MIT, Copyright (c)
2016-2024 Kenneth Shaw): the statement splitting rules of internal/db/sqlx
were inspired by usql's stmt package.

MPL-2.0 components
------------------
The source code of the MPL-2.0 libraries listed below is available,
unmodified, at the URLs given with each of them.

HDR
  ignore=(--ignore github.com/twoojoo/lumino)
  for m in "${MANUAL[@]}"; do ignore+=(--ignore "${m%%|*}"); done
  GOFLAGS=-mod=mod go run "$GO_LICENSES" report "${ignore[@]}" \
    --template packaging/notices.tpl ./cmd/lumino 2>/dev/null
  # licenses go-licenses can't classify, reproduced from the module
  for m in "${MANUAL[@]}"; do
    mod="${m%%|*}" lic="${m#*|}"
    dir="$(GOFLAGS=-mod=mod go list -m -f '{{.Dir}}' "$mod")"
    ver="$(GOFLAGS=-mod=mod go list -m -f '{{.Version}}' "$mod")"
    printf '\n================================================================================\n'
    printf '%s %s\nLicense: %s\n' "$mod" "$ver" "$lic"
    printf -- '--------------------------------------------------------------------------------\n'
    cat "$dir/LICENSE"
  done
} > "$OUT"
echo "wrote $OUT ($(grep -c '^License:' "$OUT") components)"
