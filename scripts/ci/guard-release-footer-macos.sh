#!/usr/bin/env bash
#
# ut-docs#6024 (follow-up to ut-docs#3803): the GitHub release notes footer in
# .goreleaser.yaml must not carry the stale macOS Gatekeeper advice. On macOS 15+
# right-click -> Open no longer overrides Gatekeeper; the unsigned portable
# archive needs System Settings -> Privacy & Security -> "Open Anyway", and the
# signed, notarized .dmg needs no workaround at all.
#
# Kept deliberately simple and robust: ANY "right-click" / "right click" /
# "control-click" / "ctrl-click" / "secondary-click" (case-insensitive) in
# .goreleaser.yaml fails (nothing in that file has a legitimate use for it, and
# a same-line/next-line "near Open" check would miss a re-wrapped paragraph),
# as does the "until we ship a signed build" promise. The footer must also still
# name the .dmg and "Open Anyway".
#
# Usage: guard-release-footer-macos.sh [FILE]   (default: ../../.goreleaser.yaml)
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
file="${1:-$here/../../.goreleaser.yaml}"
[ -f "$file" ] || { echo "❌ guard-release-footer-macos: $file not found" >&2; exit 1; }

fail=0
if grep -inE '(right|control|ctrl|secondary)[- ]?click' "$file" >&2; then
  echo "❌ guard-release-footer-macos: $file tells users to right/control-click; macOS 15+ ignores that — use System Settings → Privacy & Security → Open Anyway (ut-docs#6024, #3803)" >&2
  fail=1
fi
if grep -inE 'until[[:space:]]+we[[:space:]]+ship[[:space:]]+a[[:space:]]+signed[[:space:]]+build' "$file" >&2; then
  echo "❌ guard-release-footer-macos: stale 'until we ship a signed build' — the .dmg is signed and notarized (ut-docs#6024)" >&2
  fail=1
fi
if ! grep -qE 'macOS-arm64\.dmg' "$file"; then
  echo "❌ guard-release-footer-macos: release footer no longer names the macOS .dmg (ut-docs#6024)" >&2
  fail=1
fi
if ! grep -q 'Open Anyway' "$file"; then
  echo "❌ guard-release-footer-macos: release footer no longer mentions 'Open Anyway' for the portable archive (ut-docs#6024)" >&2
  fail=1
fi
[ "$fail" -eq 0 ] || exit 1
echo "✅ guard-release-footer-macos: footer names the .dmg and Open Anyway, no right/control-click advice"
