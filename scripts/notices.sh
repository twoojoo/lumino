#!/usr/bin/env bash
# Writes THIRD_PARTY_NOTICES.txt (default dist/THIRD_PARTY_NOTICES.txt): the
# licenses of everything linked into lumino, the vendored k9s and Gio with a
# list of lumino's modifications, and where MPL-2.0 sources are available.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
OUT="${1:-dist/THIRD_PARTY_NOTICES.txt}"
GO_LICENSES="github.com/google/go-licenses/v2@v2.0.1"
mkdir -p "$(dirname "$OUT")"

# shellcheck disable=SC2016 # literal ${…} in the sed pattern
GIO_VERSION="$(sed -n 's/^GIO_VERSION="\${GIO_VERSION:-\(.*\)}"/\1/p' scripts/vendor-gio.sh)"

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
* k9s $(cut -d' ' -f1 third_party/k9s/UPSTREAM) (https://github.com/derailed/k9s, Apache-2.0):
  internal packages vendored in third_party/k9s with rewritten import paths,
  a grype-free vulnerability scanner overlay, and these changes:
$(patches third_party/_k9s-patches)
* Gio $GIO_VERSION (https://gioui.org, used under the Unlicense):
  vendored in third_party/gio with these changes:
$(patches third_party/_gio-patches)

"k9s" is a project of its authors; Lumino is not affiliated with or
endorsed by it.

MPL-2.0 components
------------------
The source code of the MPL-2.0 libraries listed below is available,
unmodified, at the URLs given with each of them.

================================================================================
github.com/derailed/k9s $(cut -d' ' -f1 third_party/k9s/UPSTREAM)
License: Apache-2.0
https://github.com/derailed/k9s/blob/$(cut -d' ' -f1 third_party/k9s/UPSTREAM)/LICENSE
--------------------------------------------------------------------------------
HDR
  cat third_party/k9s/LICENSE
  GOFLAGS=-mod=mod go run "$GO_LICENSES" report --ignore github.com/giovanni/lumino \
    --template packaging/notices.tpl ./cmd/lumino 2>/dev/null
} > "$OUT"
echo "wrote $OUT ($(grep -c '^License:' "$OUT") components)"
