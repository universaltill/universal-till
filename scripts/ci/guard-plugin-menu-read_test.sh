#!/usr/bin/env bash
#
# Regression test for guard-plugin-menu-read.sh (ut-docs#489): proves the
# guard actually flags an unlocked read of Pm.Installed[, Pm.MenuPlugins[ or
# Menu planted under internal/pages, proves it's receiver-name-agnostic (this
# package uses "d"/"dp"/"deps" inconsistently), proves it does NOT
# false-positive on a comment merely mentioning these fields as prose or on
# the locked accessor methods themselves, proves test files are exempt, and
# proves the guard still passes on the real, unmodified codebase.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-plugin-menu-read.sh"
FIXTURE_DIR="internal/pages"
FAIL_COUNT=0

fixtures=()
cleanup() {
  local status=$?
  if [[ ${#fixtures[@]} -gt 0 ]]; then
    for f in "${fixtures[@]}"; do
      [[ -n "${f}" && -f "${f}" ]] && rm -f "${f}"
    done
  fi
  exit "${status}"
}
trap cleanup EXIT

plant() {
  local name="$1" content="$2" suffix="${3:-.go}"
  local path="${FIXTURE_DIR}/zz_guard_test_${name}${suffix}"
  fixtures+=("${path}")
  printf '%s\n' "${content}" >"${path}"
}

expect_fail() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_plugin_menu_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_plugin_menu_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_plugin_menu_test_out.$$
}

expect_pass() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_plugin_menu_test_out.$$ 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat /tmp/guard_plugin_menu_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_plugin_menu_test_out.$$
}

clear_fixture() {
  local name="$1" suffix="${2:-.go}"
  rm -f "${FIXTURE_DIR}/zz_guard_test_${name}${suffix}"
  fixtures=()
}

# An unlocked Pm.Installed[ read is the exact bug class ut-docs#478 fixed —
# a deliberate reintroduction must be rejected.
plant "UnlockedInstalled" 'package pages

func zzGuardTestHandler(d *common.Deps, id string) {
	_ = d.Pm.Installed[id]
}'
expect_fail "unlocked d.Pm.Installed[...] read"
clear_fixture "UnlockedInstalled"

# Same for Pm.MenuPlugins[ — the third field, the one the original #478
# sweep missed per its own review round 1.
plant "UnlockedMenuPlugins" 'package pages

func zzGuardTestHandler(d *common.Deps, key string) {
	_ = d.Pm.MenuPlugins[key]
}'
expect_fail "unlocked d.Pm.MenuPlugins[...] read"
clear_fixture "UnlockedMenuPlugins"

# Same for Menu itself.
plant "UnlockedMenu" 'package pages

func zzGuardTestHandler(d *common.Deps) []common.MenuItem {
	return d.Menu
}'
expect_fail "unlocked d.Menu read"
clear_fixture "UnlockedMenu"

# A different *common.Deps receiver variable name — registerShiftsAPI/
# registerInventoryAPI/registerPluginStore use "dp"/"deps" elsewhere in this
# package, so the guard must not be fooled by those names either.
plant "UnlockedInstalledAltReceiver" 'package pages

func zzGuardTestHandler(dp *common.Deps, id string) {
	_ = dp.Pm.Installed[id]
}'
expect_fail "unlocked dp.Pm.Installed[...] read (non-\"d\" receiver)"
clear_fixture "UnlockedInstalledAltReceiver"

# A comment that merely mentions these fields as prose (the style deps.go
# itself already uses) must NOT trip the guard — only actual code counts.
plant "CommentOnlyMention" 'package pages

// Read call sites MUST go through MenuSnapshot / InstalledPlugin /
// MenuPluginByKey, never touch Menu/Pm.Installed/Pm.MenuPlugins directly.
func zzGuardTestHandler(d *common.Deps, id string) (plugins.Plugin, bool) {
	return d.InstalledPlugin(id)
}'
expect_pass "a comment that only mentions the guarded fields as prose"
clear_fixture "CommentOnlyMention"

# The locked accessors themselves — the correct, post-#478 pattern — must
# never trip the guard.
plant "LockedAccessorsUsed" 'package pages

func zzGuardTestHandler(d *common.Deps, id, key string) {
	_ = d.MenuSnapshot()
	_, _ = d.InstalledPlugin(id)
	_, _ = d.MenuPluginByKey(key)
}'
expect_pass "the locked MenuSnapshot/InstalledPlugin/MenuPluginByKey accessors"
clear_fixture "LockedAccessorsUsed"

# Test files exercise the locked accessors under controlled goroutine
# conditions and are the test subject, not a regression risk — exempt.
plant "TestFileExempt" 'package pages

func TestZzGuardFixture(t *testing.T) {
	d := &common.Deps{}
	_ = d.Pm.Installed["x"]
}' "_test.go"
expect_pass "an unlocked read inside a _test.go file"
clear_fixture "TestFileExempt" "_test.go"

# Baseline: the guard must still pass on the real, unmodified codebase.
if ! bash "${GUARD}" >/tmp/guard_plugin_menu_test_out.$$ 2>&1; then
  echo "❌ FAIL: guard rejects the clean codebase (false positive introduced)" >&2
  cat /tmp/guard_plugin_menu_test_out.$$ >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
else
  echo "✓ guard still passes on the clean codebase"
fi
rm -f /tmp/guard_plugin_menu_test_out.$$

if [[ "${FAIL_COUNT}" -gt 0 ]]; then
  echo "❌ guard-plugin-menu-read_test.sh: ${FAIL_COUNT} case(s) failed" >&2
  exit 1
fi

echo "✓ guard-plugin-menu-read_test.sh: all cases passed"
exit 0
