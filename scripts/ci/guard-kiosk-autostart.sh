#!/usr/bin/env bash
#
# The Pi5 kiosk-autostart fix (ut-docs/QUEUE.md, field report 2026-07-30)
# has two systemd fixes proven live on a real Pi5 (Debian 13 trixie) plus a
# postinstall auto-enable step -- guards against any of the three silently
# regressing:
#   1. Debian 13 trixie on Pi5 boots with the active VT on tty7, not tty1,
#      so cage's logind session never activates without forcing tty1 first.
#   2. The kiosk service runs under PAMName=login (a logind session) but
#      also installs seatd -- without pinning the backend, libseat picks
#      seatd and fails ("Could not poll connection: Broken pipe").
#   3. Without postinstall auto-enabling the kiosk on Pi hardware, a shop
#      owner would have to run a manual command by hand -- the whole point
#      of this fix is that they never do.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

KIOSK_SETUP="packaging/linux/unitill-kiosk-setup.sh"
POSTINSTALL="packaging/scripts/postinstall.sh"

# Strip comment-only lines first -- a bare `grep -q` would still "pass" on a
# directive that got commented out (e.g. `# ExecStartPre=...`), since the
# substring is still there. This project's guards have been caught by this
# exact false-pass class before (guard-emoji-font.sh, 2026-07-29). Checking
# against active (non-comment) lines only closes it.
active_lines() { grep -vE '^[[:space:]]*#' "$1"; }

fail=0

if ! active_lines "$KIOSK_SETUP" | grep -q -- 'ExecStartPre=+/usr/bin/chvt 1'; then
  echo "❌ kiosk-autostart guard: ${KIOSK_SETUP} is missing an active 'ExecStartPre=+/usr/bin/chvt 1'" >&2
  echo "   (without it, cage never gets the active VT on Debian 13 trixie/Pi5)" >&2
  fail=1
fi

if ! active_lines "$KIOSK_SETUP" | grep -q -- 'Environment=LIBSEAT_BACKEND=logind'; then
  echo "❌ kiosk-autostart guard: ${KIOSK_SETUP} is missing an active 'Environment=LIBSEAT_BACKEND=logind'" >&2
  echo "   (without it, libseat wrongly tries the seatd backend and fails)" >&2
  fail=1
fi

if ! active_lines "$POSTINSTALL" | grep -qi 'device-tree/model'; then
  echo "❌ kiosk-autostart guard: ${POSTINSTALL} is missing active Raspberry Pi detection (/proc/device-tree/model)" >&2
  fail=1
fi

if ! active_lines "$POSTINSTALL" | grep -q 'unitill-kiosk-setup'; then
  echo "❌ kiosk-autostart guard: ${POSTINSTALL} never actively invokes unitill-kiosk-setup -- kiosk won't auto-enable" >&2
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

echo "✓ kiosk-autostart guard: tty1/logind systemd fixes present, postinstall auto-enables kiosk on Pi hardware"
