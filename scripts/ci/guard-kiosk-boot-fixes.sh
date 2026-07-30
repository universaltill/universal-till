#!/usr/bin/env bash
#
# The Pi5 kiosk boot fixes (ut-docs/QUEUE.md, field report 2026-07-30) were
# proven live on a real Pi5 (Debian 13 trixie) via a manual systemd drop-in
# before landing here -- guards against either silently regressing:
#   1. Debian 13 trixie on Pi5 boots with the active VT on tty7, not tty1,
#      so cage's logind session never activates without forcing tty1 first.
#   2. The kiosk service runs under PAMName=login (a logind session) but
#      also installs seatd -- without pinning the backend, libseat picks
#      seatd and fails ("Could not poll connection: Broken pipe").
#   3. ExecStartPre's chvt needs /usr/bin/chvt (package: kbd) -- without
#      it the unit fails to start at all on any image that doesn't already
#      carry kbd.
# (Auto-enabling the kiosk on install was tried and reverted -- apt-get
# inside a dpkg postinst deadlocks on the dpkg lock; see postinstall.sh's
# own note and the follow-up logged in ut-docs/QUEUE.md. This guard only
# covers the setup script's own generated unit, not postinstall.)
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

KIOSK_SETUP="packaging/linux/unitill-kiosk-setup.sh"

# Strip comment-only lines before checking, and capture into a variable
# (never pipe straight into `grep -q`) -- a `grep -v ... | grep -q ...`
# chain can SIGPIPE the upstream `grep -v` once `grep -q` exits on its
# first match, and under this script's `pipefail` that can surface as the
# pipeline's exit status even when the match genuinely succeeded, i.e. a
# spurious guard failure. Capturing first avoids the pipe entirely.
active_lines="$(grep -vE '^[[:space:]]*#' "$KIOSK_SETUP")"

fail=0

if ! grep -qE -- '^ExecStartPre=\+/usr/bin/chvt 1$' <<< "$active_lines"; then
  echo "❌ kiosk-boot-fixes guard: ${KIOSK_SETUP} is missing an active 'ExecStartPre=+/usr/bin/chvt 1'" >&2
  echo "   (without it, cage never gets the active VT on Debian 13 trixie/Pi5)" >&2
  fail=1
fi

if ! grep -qE -- '^Environment=LIBSEAT_BACKEND=logind$' <<< "$active_lines"; then
  echo "❌ kiosk-boot-fixes guard: ${KIOSK_SETUP} is missing an active 'Environment=LIBSEAT_BACKEND=logind'" >&2
  echo "   (without it, libseat wrongly tries the seatd backend and fails)" >&2
  fail=1
fi

if ! grep -qE -- '^apt-get install .*\bkbd\b' <<< "$active_lines"; then
  echo "❌ kiosk-boot-fixes guard: ${KIOSK_SETUP} no longer installs 'kbd' -- chvt (used by ExecStartPre above) would be missing" >&2
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

echo "✓ kiosk-boot-fixes guard: tty1/logind systemd fixes + kbd dependency present"
