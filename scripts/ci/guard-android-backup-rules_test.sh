#!/usr/bin/env bash
#
# Regression test for guard-android-backup-rules.sh (ut-docs#6915): plants
# broken copies of the manifest, the two rule files and restored.go in a
# throwaway sandbox and proves the guard rejects each, then accepts the real
# tree. A guard never shown to fail proves nothing.
set -euo pipefail

# shellcheck source=scripts/ci/guard-test-sandbox.sh
source "$(dirname "${BASH_SOURCE[0]}")/guard-test-sandbox.sh"
enter_guard_test_sandbox android/app/src/main internal/enroll
trap remove_guard_test_sandbox EXIT

GUARD="scripts/ci/guard-android-backup-rules.sh"
MANIFEST="android/app/src/main/AndroidManifest.xml"
EXTRACTION="android/app/src/main/res/xml/data_extraction_rules.xml"
BACKUP="android/app/src/main/res/xml/backup_rules.xml"
RESTORED="internal/enroll/restored.go"
SERVICE="android/app/src/main/java/com/universaltill/pos/TillService.kt"
FAIL_COUNT=0


# Keep pristine copies so each case starts clean (the sandbox is a git repo
# with everything staged, so restore from the index).
restore_all() {
  git checkout -q -- "${MANIFEST}" "${EXTRACTION}" "${BACKUP}" "${RESTORED}" "${SERVICE}" 2>/dev/null || true
}

assert_passes() {
  if bash "${GUARD}" >/dev/null 2>&1; then echo "PASS (accepted): $1"
  else echo "FAIL (should pass): $1"; bash "${GUARD}" || true; FAIL_COUNT=$((FAIL_COUNT + 1)); fi
}
assert_fails() {
  if bash "${GUARD}" >/dev/null 2>&1; then echo "FAIL (should be rejected): $1"; FAIL_COUNT=$((FAIL_COUNT + 1))
  else echo "PASS (rejected): $1"; fi
}

# rewrite FILE SED-EXPR : edit one fixture, run the guard, then restore.
case_fails() {
  local label="$1" file="$2" expr="$3"
  sed -i -E "${expr}" "${file}"
  if cmp -s "${file}" <(git show ":${file}"); then
    echo "FAIL (fixture edit changed nothing): ${label}"; FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    assert_fails "${label}"
  fi
  restore_all
}

assert_passes "real tree"

case_fails "manifest lacks dataExtractionRules" "${MANIFEST}" 's#android:dataExtractionRules="[^"]*"##'
case_fails "manifest lacks fullBackupContent" "${MANIFEST}" 's#android:fullBackupContent="[^"]*"##'
case_fails "manifest allowBackup turned on" "${MANIFEST}" 's#android:allowBackup="false"#android:allowBackup="true"#'
case_fails "extraction rules: no device-transfer block" "${EXTRACTION}" '/<device-transfer>/,/<\/device-transfer>/d'
case_fails "extraction rules: no cloud-backup block" "${EXTRACTION}" '/<cloud-backup>/,/<\/cloud-backup>/d'
case_fails "extraction rules: tls not excluded (both blocks)" "${EXTRACTION}" '/path="tls"/d'
case_fails "extraction rules: secrets not excluded (both blocks)" "${EXTRACTION}" '/path="secrets"/d'
case_fails "extraction rules: an <include> added" "${EXTRACTION}" 's#</cloud-backup>#<include domain="file" path="." /></cloud-backup>#'
case_fails "backup rules: tls not excluded" "${BACKUP}" '/path="tls"/d'
case_fails "backup rules: secrets not excluded" "${BACKUP}" '/path="secrets"/d'
case_fails "backup rules: an <include> added" "${BACKUP}" 's#</full-backup-content>#<include domain="file" path="." /></full-backup-content>#'
case_fails "install marker moved out of secrets/" "${RESTORED}" 's#\{"secrets", "install-id"\}#{"state", "install-id"}#'

case_fails "data root no longer filesDir" "${SERVICE}" 's#Mobile\.start\(filesDir\.absolutePath\)#Mobile.start(getDir("till", 0).absolutePath)#'
case_fails "backup rules: root element renamed" "${BACKUP}" 's#full-backup-content#bogus#g'
case_fails "extraction rules: root element renamed" "${EXTRACTION}" 's#data-extraction-rules#bogus#g'

# Only one of the two blocks losing tls must fail, not just "none has it".
sed -i -E '0,/path="tls"/{/path="tls"/d}' "${EXTRACTION}"
assert_fails "extraction rules: tls missing from the first block only"
restore_all

rm -f "${BACKUP}"
assert_fails "backup rules file missing"
restore_all
rm -f "${EXTRACTION}"
assert_fails "extraction rules file missing"
restore_all

assert_passes "restored after fixtures"

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "guard-android-backup-rules_test: ${FAIL_COUNT} assertion(s) failed" >&2
  exit 1
fi
echo "guard-android-backup-rules_test: all assertions passed"
