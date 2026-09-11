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
# Invoked indirectly via `trap ... EXIT`, not a direct call -- shellcheck cannot see that (SC2317 false positive).
# shellcheck disable=SC2317
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

# ADR-0088 added two more fields reassigned in the same critical section:
# Deps.MenuAmendments (beside Menu) and Pm.LayoutAmendments (in Reload).
plant "UnlockedMenuAmendments" 'package pages

func zzGuardTestHandler(d *common.Deps) []uislot.Amendment {
	return d.MenuAmendments
}'
expect_fail "unlocked d.MenuAmendments read"
clear_fixture "UnlockedMenuAmendments"

plant "UnlockedLayoutAmendments" 'package pages

func zzGuardTestHandler(dp *common.Deps) []uislot.Amendment {
	return dp.Pm.LayoutAmendments
}'
expect_fail "unlocked dp.Pm.LayoutAmendments read"
clear_fixture "UnlockedLayoutAmendments"

# ut-docs#1911: Deps.ItemsAmendments is MenuAmendments' Items-slot twin,
# reassigned in the same ReloadPlugins critical section — must be caught
# exactly the same way.
plant "UnlockedItemsAmendments" 'package pages

func zzGuardTestHandler(d *common.Deps) []uislot.Amendment {
	return d.ItemsAmendments
}'
expect_fail "unlocked d.ItemsAmendments read"
clear_fixture "UnlockedItemsAmendments"

# ut-docs#1912: Deps.RailAmendments is the Rail-slot twin, reassigned in the
# same critical section — same treatment (the #1911 review found exactly
# this gap for Items; don't reproduce it for Rail).
plant "UnlockedRailAmendments" 'package pages

func zzGuardTestHandler(deps *common.Deps) []uislot.Amendment {
	return deps.RailAmendments
}'
expect_fail "unlocked deps.RailAmendments read"
clear_fixture "UnlockedRailAmendments"

# ...while the provider WIRING init.go does (httpx.RailAmendmentsProvider =
# dp.RailAmendmentsSnapshot) names "RailAmendmentsProvider", not the field —
# the \b boundary must keep that from false-positiving, or the guard would
# reject the one line that makes the render path read the field locked.
plant "RailProviderWiring" 'package pages

func zzGuardTestWire(dp *common.Deps) {
	httpx.RailAmendmentsProvider = dp.RailAmendmentsSnapshot
}'
expect_pass "the httpx.RailAmendmentsProvider = dp.RailAmendmentsSnapshot wiring"
clear_fixture "RailProviderWiring"

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
	_ = d.MenuAmendmentsSnapshot()
	_ = d.ItemsAmendmentsSnapshot()
	_ = d.RailAmendmentsSnapshot()
	_ = d.LayoutAmendmentsSnapshot()
}'
expect_pass "the locked MenuSnapshot/InstalledPlugin/MenuPluginByKey/MenuAmendmentsSnapshot/ItemsAmendmentsSnapshot/RailAmendmentsSnapshot/LayoutAmendmentsSnapshot accessors"
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

# A NEW call site of BuildMenuAmendments (independent review of
# ut-docs#1904, F4). The field pattern cannot see this — the function reads
# pm.LayoutAmendments off a *plugins.Manager PARAMETER, not through a
# *common.Deps — so the call-site allowlist is what actually guards it. A
# fourth caller must fail until whoever adds it states which lock it holds.
plant "RogueBuildMenuAmendmentsCaller" 'package pages

func zzGuardTestHandler(pm *plugins.Manager) []uislot.Amendment {
	return common.BuildMenuAmendments(pm, nil)
}'
expect_fail "a new, non-allowlisted BuildMenuAmendments call site"
clear_fixture "RogueBuildMenuAmendmentsCaller"

# ut-docs#1911: BuildItemsAmendments reads the identical pm.LayoutAmendments
# field under the identical caller-holds-the-lock contract — a new,
# non-allowlisted caller of IT must fail this guard too, not just Menu's.
plant "RogueBuildItemsAmendmentsCaller" 'package pages

func zzGuardTestHandler(pm *plugins.Manager) []uislot.Amendment {
	return common.BuildItemsAmendments(pm)
}'
expect_fail "a new, non-allowlisted BuildItemsAmendments call site"
clear_fixture "RogueBuildItemsAmendmentsCaller"

# ut-docs#1912: and BuildRailAmendments, the third reader of that field.
plant "RogueBuildRailAmendmentsCaller" 'package pages

func zzGuardTestHandler(pm *plugins.Manager) []uislot.Amendment {
	return common.BuildRailAmendments(pm)
}'
expect_fail "a new, non-allowlisted BuildRailAmendments call site"
clear_fixture "RogueBuildRailAmendmentsCaller"

# ...while the allowlisted call sites (deps.go, state.go, init.go)
# keep the clean codebase passing — asserted by the baseline check below.

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
