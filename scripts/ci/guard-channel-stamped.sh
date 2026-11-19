#!/usr/bin/env bash
#
# Every build that stamps internal/buildinfo.Version must stamp its OWN install
# channel on the same line (ADR-0194, ut-docs#6405): buildinfo.Channel=<one of
# sideload|play|appstore|direct> and the matching CI-inspectable twin
# buildinfo.ChannelStamp=ut-channel:<same>. A build without a literal channel
# would silently ship as the default and a Store build could keep a purchase
# link or self-update (or a sideload build lose them). Literal values only: a
# variable or an unknown word is refused, so the channel is a build constant
# and never read from anywhere at build time.
#
# .goreleaser.yaml is checked per build id: the Windows builds (`windows`,
# `desktop-windows`: the portable zip AND the NSIS installer's contents, which
# is also the Microsoft Store binary) are `direct`, every other build is
# `sideload`. Play and App Store artifacts are not built here yet.
#
# Usage: guard-channel-stamped.sh [repo-root]
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
B='github\.com/universaltill/universal-till/internal/buildinfo\.'
for f in "${files[@]}"; do
  id=""
  while IFS= read -r row; do
    n="${row%%:*}"; text="${row#*:}"
    trimmed="${text#"${text%%[![:space:]]*}"}"
    case "$trimmed" in
      '#'* | '//'*) continue ;;
    esac
    if [ "$f" = .goreleaser.yaml ]; then
      # The build id this ldflags line belongs to: the nearest `  - id:` above.
      id="$(awk -v n="$n" 'NR>n{exit} /^  - id: /{id=$3} END{print id}' "$f")"
    fi
    if ! grep -qE -- "-X[[:space:]]+${B}Version=" <<<"$text"; then continue; fi
    stamps=$((stamps + 1))
    ch="$(sed -nE "s#.*-X[[:space:]]+${B}Channel=(sideload|play|appstore|direct)([[:space:]\"']|\$).*#\\1#p" <<<"$text")"
    if [ -z "$ch" ]; then
      echo "::error file=${f},line=${n}::stamps buildinfo.Version without a literal buildinfo.Channel=<sideload|play|appstore|direct> (ADR-0194)"
      fail=1
      continue
    fi
    if ! grep -qE -- "-X[[:space:]]+${B}ChannelStamp=ut-channel:${ch}([[:space:]\"']|\$)" <<<"$text"; then
      echo "::error file=${f},line=${n}::buildinfo.Channel=${ch} without the matching buildinfo.ChannelStamp=ut-channel:${ch}"
      fail=1
    fi
    if [ "$f" = .goreleaser.yaml ]; then
      case "$id" in
        windows | desktop-windows) want=direct ;;
        *) want=sideload ;;
      esac
      if [ "$ch" != "$want" ]; then
        echo "::error file=${f},line=${n}::goreleaser build '${id}' stamps channel '${ch}', expected '${want}' (ADR-0194)"
        fail=1
      fi
    fi
  done < <(grep -nE -- "-X[[:space:]]+${B}Version=" "$f" || true)
done

if [ "$stamps" -eq 0 ]; then
  echo "::error::guard-channel-stamped: found no buildinfo.Version stamp at all under ${ROOT} — the scan is broken"
  exit 1
fi
if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "✓ guard-channel-stamped: all ${stamps} Version stamps carry their own literal channel"
