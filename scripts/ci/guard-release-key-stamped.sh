#!/usr/bin/env bash
#
# Every build that stamps internal/buildinfo.MarketplacePublicKey must stamp
# internal/buildinfo.ReleasePublicKeys on the same line (ADR-0178). The
# self-updater's signature check is armed only by that stamp; a release build
# that forgets it silently ships an updater that trusts SHA-256 alone, and
# `go build` never complains about a missing -X.
#
# Scans the same build definitions as guard-builddate-stamped.sh. A line that
# only mentions the symbol (comment, echo, a grep that verifies it) is not a
# stamp: only `-X <pkg>/buildinfo.MarketplacePublicKey=` is.
# Usage: guard-release-key-stamped.sh [repo-root]
set -euo pipefail

ROOT="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
cd "${ROOT}"

files=()
for f in Makefile .goreleaser.yaml android/app/build.gradle.kts; do
  [ -f "$f" ] && files+=("$f")
done
while IFS= read -r -d '' f; do files+=("$f"); done < <(find .github/workflows packaging -type f \( -name '*.yml' -o -name '*.yaml' -o -name '*.sh' \) -print0 2>/dev/null)

fail=0
stamps=0
for f in "${files[@]}"; do
  while IFS=: read -r line text; do
    case "${text#"${text%%[![:space:]]*}"}" in
      '#'* | '//'*) continue ;;
    esac
    stamps=$((stamps + 1))
    if ! grep -qF 'internal/buildinfo.ReleasePublicKeys=' <<<"$text"; then
      echo "::error file=${f},line=${line}::stamps buildinfo.MarketplacePublicKey without buildinfo.ReleasePublicKeys on the same line (ADR-0178)"
      fail=1
    fi
  done < <(grep -nE -- '-X[[:space:]]+github\.com/universaltill/universal-till/internal/buildinfo\.MarketplacePublicKey=' "$f" || true)
done

if [ "$stamps" -eq 0 ]; then
  echo "::error::guard-release-key-stamped: found no buildinfo.MarketplacePublicKey stamp at all under ${ROOT} — the scan is broken"
  exit 1
fi
if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "✓ guard-release-key-stamped: all ${stamps} MarketplacePublicKey stamps also stamp ReleasePublicKeys"
