#!/usr/bin/env bash
#
# Regression test for guard-android-external-links.sh (ut-docs#1647): proves
# the guard actually rejects the shape of the original bug, rather than
# merely passing on the fixed source. A source-level guard that has never
# been shown to fail is indistinguishable from one whose grep silently
# stopped matching.
#
# The bug it must catch is small and easy to reintroduce during an unrelated
# refactor of shouldOverrideUrlLoading: an off-origin branch that blocks the
# navigation and then drops the URL on the floor, leaving every external link
# in the till UI silently dead on Android.
#
# Plants each regression in a byte-for-byte backup of the REAL MainActivity.kt
# (so the guard runs against its real hardcoded path), runs the real guard,
# asserts pass/fail, and always restores from the backup — restore-from-backup
# rather than an undo-patch, so a killed process at worst leaves a *.backup
# file behind rather than a corrupted source file. Same shape and same
# reasoning as guard-webkit-version_test.sh.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-android-external-links.sh"
MAIN_ACTIVITY="android/app/src/main/java/com/universaltill/pos/MainActivity.kt"
BACKUP="${MAIN_ACTIVITY}.guard_extlinks_test_backup"
FAIL_COUNT=0

cp "${MAIN_ACTIVITY}" "${BACKUP}"
cleanup() {
  local status=$?
  cp "${BACKUP}" "${MAIN_ACTIVITY}"
  rm -f "${BACKUP}"
  exit "${status}"
}
trap cleanup EXIT

# Each fixture starts from the pristine backup, so the fixtures can't
# interact and a failing assertion can't leave a half-mutated file behind
# for the next one.
plant() {
  cp "${BACKUP}" "${MAIN_ACTIVITY}"
  python3 - "$@" <<'PY'
import io
import sys

path = "android/app/src/main/java/com/universaltill/pos/MainActivity.kt"
old, new = sys.argv[1], sys.argv[2]
s = io.open(path, encoding="utf-8").read()
if s.count(old) != 1:
    sys.exit(f"fixture anchor matched {s.count(old)} times, expected exactly 1: {old!r}")
io.open(path, "w", encoding="utf-8").write(s.replace(old, new))
PY
}

expect_fail() {
  local label="$1"
  if bash "${GUARD}" >/dev/null 2>&1; then
    echo "❌ FAIL: expected the guard to reject ${label}, but it passed" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
}

expect_pass() {
  local label="$1"
  if bash "${GUARD}" >/dev/null 2>&1; then
    echo "✓ guard correctly accepted ${label}"
  else
    echo "❌ FAIL: expected the guard to accept ${label}, but it failed" >&2
    bash "${GUARD}" >&2 || true
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

# 0. Baseline: the real, fixed source must pass. Without this, every
#    assertion below could be passing for the wrong reason.
cp "${BACKUP}" "${MAIN_ACTIVITY}"
expect_pass "the committed MainActivity.kt"

# 1. THE ORIGINAL BUG: block the off-origin navigation, hand it to nobody.
#    This is the exact source shape the product owner's tablet was running.
plant '                        if (request?.isForMainFrame == true) {
                            openInSystemBrowser(target)
                        }
' ''
expect_fail "an off-origin branch that blocks without handing off (the original ut-docs#1647 bug)"

# 2. The hand-off exists but nothing calls it — a dead function reads as a
#    fix in a diff and behaves exactly like bug #1 on the device.
plant '                            openInSystemBrowser(target)
' '                            val unused = target
'
expect_fail "openInSystemBrowser defined but never called from the block branch"

# 3. The origin confinement itself removed. ut-docs#1254's hole: the WebView
#    would load the off-origin page in place, exposing window.AndroidKiosk to
#    a page this app never authored.
plant 'if (target.authority != allowedHost) {' 'if (false) {'
expect_fail "the allowedHost origin confinement being removed"

# 4. The blocked branch returning false instead of true. Reopens the same
#    ut-docs#1254 hole from the other direction — the WebView loads it anyway.
plant '                        return true // block: refuse to navigate off-origin' '                        return false'
expect_fail "the blocked branch returning false (WebView loads the off-origin page in place)"

# 5. The scheme allowlist dropped, so an intent:// or custom-scheme URL from
#    web content would reach Intent.ACTION_VIEW — an arbitrary app-launch
#    primitive handed to any page that can render a link.
plant '        if (scheme != "http" && scheme != "https") {
            return
        }
' ''
expect_fail "the http/https scheme allowlist being dropped"

# 6. FLAG_ACTIVITY_NEW_TASK dropped: the browser lands in the till's own task
#    stack instead of alongside it — "opens in another browser instance, does
#    not change the current POS screen" is the requirement in the product
#    owner's own words.
plant '                Intent(Intent.ACTION_VIEW, target).apply {
                    addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                },' '                Intent(Intent.ACTION_VIEW, target),'
expect_fail "the external launch losing FLAG_ACTIVITY_NEW_TASK"

# 7. Comments alone must not satisfy the guard — the guard strips comment
#    lines before matching, so a reverted fix can't hide behind prose that
#    still describes it (same lesson as guard-kiosk-launch-flags.sh).
plant '                            openInSystemBrowser(target)
' '                            // openInSystemBrowser(target)
'
expect_fail "the hand-off reduced to a comment"

# 7b. The main-frame restriction dropped: an off-origin <iframe> on any page
#     would spawn a browser on every page load, over the live sale screen.
plant 'if (request?.isForMainFrame == true) {' 'if (true) {'
expect_fail "the browser hand-off no longer being restricted to main-frame navigations"

# 8. A failed hand-off swallowed in silence. A device with no browser app
#    would then behave exactly as the tablet did before this fix — which is
#    the symptom, not merely a related one.
plant '            Toast.makeText(this, R.string.external_link_failed, Toast.LENGTH_SHORT).show()
' ''
expect_fail "a hand-off failure swallowed without telling the operator"

cp "${BACKUP}" "${MAIN_ACTIVITY}"
if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "❌ guard-android-external-links_test: ${FAIL_COUNT} assertion(s) failed" >&2
  exit 1
fi
echo "✓ guard-android-external-links_test: all assertions passed"
