#!/usr/bin/env bash
#
# ut-docs#6935: a sideload (GitHub-release) APK self-updates, so its merged
# manifest must declare android.permission.REQUEST_INSTALL_PACKAGES. The play
# flavor must NOT (check-aab.sh asserts that); nothing else asserted the
# sideload side, so a refactor could silently drop it and break self-update.
#
# Usage: check-android-sideload-install-permission.sh <merged AndroidManifest.xml>...
# Exit 1 if any manifest lacks the permission (XML comments are ignored),
# 2 on no arguments or an unreadable file.
set -euo pipefail

[ "$#" -gt 0 ] || { echo "usage: $0 <AndroidManifest.xml>..." >&2; exit 2; }

fail=0
for m in "$@"; do
  [ -f "${m}" ] || { echo "check-android-sideload-install-permission: ${m} missing" >&2; exit 2; }
  # Strip <!-- ... --> comments (possibly multi-line) before matching.
  if python3 -I - "${m}" <<'PY'
import re, sys
t = re.sub(r"<!--.*?-->", "", open(sys.argv[1], encoding="utf-8").read(), flags=re.S)
sys.exit(0 if re.search(r"<uses-permission\b[^>]*android:name=\"android\.permission\.REQUEST_INSTALL_PACKAGES\"", t) else 1)
PY
  then
    echo "ok: ${m} declares REQUEST_INSTALL_PACKAGES"
  else
    echo "FAIL: ${m} does not declare android.permission.REQUEST_INSTALL_PACKAGES (sideload self-update needs it, ut-docs#6935)" >&2
    fail=1
  fi
done
exit "${fail}"
