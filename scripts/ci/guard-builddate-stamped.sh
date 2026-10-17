#!/usr/bin/env bash
#
# Every build that stamps internal/buildinfo.Version must stamp
# internal/buildinfo.BuildDate on the same line (ut-docs#4369, ADR-0160 §6).
# The translator ranks a first-party language bundle against BuildDate: a
# release built without it reads as a source build and lets EVERY dated
# bundle override its built-in strings, including a stale one. `go build`
# never complains about a missing -X, so nothing else would notice.
#
# Scans the build definitions that ship a till (Makefile, .goreleaser.yaml,
# workflows, packaging/, android/app/build.gradle.kts) — not e2e/*.sh, whose
# throwaway tills are source builds on purpose; a line that only mentions
# Version in an echo/error message or a comment is not a stamp.
# Usage: guard-builddate-stamped.sh [repo-root]
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
  # -X <pkg>/buildinfo.Version=<value>: the stamp itself, not prose about it.
  while IFS=: read -r line text; do
    case "${text#"${text%%[![:space:]]*}"}" in
      '#'* | '//'*) continue ;;
    esac
    stamps=$((stamps + 1))
    if ! grep -qF 'internal/buildinfo.BuildDate=' <<<"$text"; then
      echo "::error file=${f},line=${line}::stamps buildinfo.Version without buildinfo.BuildDate on the same line (ADR-0160 §6, ut-docs#4369)"
      fail=1
    fi
  done < <(grep -nE -- '-X[[:space:]]+github\.com/universaltill/universal-till/internal/buildinfo\.Version=' "$f" || true)
done

if [ "$stamps" -eq 0 ]; then
  echo "::error::guard-builddate-stamped: found no buildinfo.Version stamp at all under ${ROOT} — the scan is broken"
  exit 1
fi
if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "✓ guard-builddate-stamped: all ${stamps} Version stamps also stamp BuildDate"
