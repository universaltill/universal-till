#!/usr/bin/env bash
#
# The Microsoft Store runs the installer silently (`/S`, ut-docs#6407): fail
# unless the NSIS script can complete with no UI and no elevation prompt.
#
#   check-installer-silent.sh <installer.nsi>
#
# NSIS honours /S itself, so this checks what would break it: a MessageBox
# with no silent default (/SD), `SilentInstall normal` (forces the UI),
# an elevated install (a UAC prompt blocks a silent run), or a missing
# RequestExecutionLevel (the default can prompt).
set -euo pipefail

f="${1:?usage: check-installer-silent.sh <installer.nsi>}"
[ -f "$f" ] || { echo "::error::check-installer-silent: no such file: $f" >&2; exit 1; }
fail=0
# Drop `;` and `#` comment lines.
code="$(grep -vE '^[[:space:]]*[;#]' "$f" || true)"

if grep -iE '^[[:space:]]*MessageBox' <<<"$code" | grep -viE '/SD[[:space:]]+ID' >/dev/null; then
  echo "::error::$f: a MessageBox has no /SD default, so a silent install (/S) would block on it" >&2
  fail=1
fi
if grep -qiE '^[[:space:]]*SilentInstall[[:space:]]+normal' <<<"$code"; then
  echo "::error::$f: SilentInstall normal forces the UI" >&2
  fail=1
fi
if ! grep -qiE '^[[:space:]]*RequestExecutionLevel[[:space:]]+(user|admin|highest)' <<<"$code"; then
  echo "::error::$f: no RequestExecutionLevel" >&2
  fail=1
elif grep -qiE '^[[:space:]]*RequestExecutionLevel[[:space:]]+(admin|highest)' <<<"$code"; then
  echo "::error::$f: an elevated install prompts for UAC, which a silent Store install cannot answer" >&2
  fail=1
fi
[ "$fail" = 0 ] || exit 1
echo "ok: $f supports a silent install (/S)"
