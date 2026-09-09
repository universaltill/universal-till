#!/usr/bin/env bash
#
# Regression test for guard-plugin-settings-bump.sh (ut-docs#1357): proves
# the guard flags a production .go file that calls a plugin-settings writer
# (UpsertPluginSetting/UpsertPluginSettingScoped/MergeAdditiveJSONMapSetting)
# without referencing BumpGeneration() anywhere in that same file, proves it
# does NOT flag a call planted in a _test.go file, proves the writers'
# own definition file (internal/data/plugin_repo.go) is exempted so the
# guard isn't tripped by its own internal UpsertPluginSetting ->
# UpsertPluginSettingScoped delegation, proves the inline
# `plugin-settings-bump:allow` escape hatch silences a real finding ONLY on
# the line that carries it (a second, unmarked writer-call line in the same
# file must still be caught — independent review, ut-docs#1357, found the
# first draft's file-scoped escape hatch silenced the whole file instead),
# proves the guard fails closed rather than silently no-ops when its own
# writer pattern matches nothing, proves the guard still passes on the
# real, unmodified codebase (every known production call site was already
# fixed by ut-docs#222/#1351), and (ut-docs#1942) proves the scan also
# catches a violation planted under cmd/, scripts/, and e2e/ — not just
# internal/.
#
# Same fixture-planting convention as guard-price-history-sync_test.sh —
# scratch files under internal/, cleaned up on exit via a trap, never a
# mutation of real tracked source.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-plugin-settings-bump.sh"
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
  local dir="$1" pkg="$2" name="$3" content="$4"
  local path="${dir}/zz_guard_test_${name}.go"
  fixtures+=("${path}")
  printf 'package %s\n\n%s\n' "${pkg}" "${content}" >"${path}"
}

clear_fixtures() {
  if [[ ${#fixtures[@]} -gt 0 ]]; then
    for f in "${fixtures[@]}"; do
      rm -f "${f}"
    done
  fi
  fixtures=()
}

run_guard() {
  # Any extra args are env assignments the guard should see (e.g. a
  # WRITER_RE override) — passed through explicitly rather than exported,
  # same convention as guard-price-history-sync_test.sh's run_guard.
  env "$@" bash "${GUARD}"
}

expect_fail() {
  # $1 label, $2 optional substring the guard's rejection output MUST
  # contain (e.g. the offending filename) — without this, a case that
  # "fails" because the guard script itself crashed/is missing would be
  # miscounted as a correct, reasoned rejection (independent review, N1).
  # Any further args are env assignments passed through to run_guard.
  local label="$1" must_contain="$2"
  shift 2
  if run_guard "$@" >/tmp/guard_plugin_settings_bump_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_plugin_settings_bump_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  elif [[ -n "${must_contain}" ]] && ! grep -qF "${must_contain}" /tmp/guard_plugin_settings_bump_test_out.$$; then
    echo "❌ FAIL: guard rejected ${label}, but its output didn't name '${must_contain}' — did it reject for the right reason?" >&2
    cat /tmp/guard_plugin_settings_bump_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_plugin_settings_bump_test_out.$$
}

expect_pass() {
  local label="$1"
  if run_guard >/tmp/guard_plugin_settings_bump_test_out.$$ 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat /tmp/guard_plugin_settings_bump_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_plugin_settings_bump_test_out.$$
}

# A real production caller that writes a plugin setting but never
# references BumpGeneration anywhere in the same file must be rejected.
plant "internal/pages" "pages" "MissingBump" 'import (
	"context"

	"github.com/universaltill/universal-till/internal/data"
)

func zzGuardTestMissingBump(ctx context.Context, db data.DBTX) {
	_ = data.NewPluginRepo(db).UpsertPluginSetting(ctx, "com.example.tax", "rate", `+"`"+`"700"`+"`"+`)
}'
expect_fail "a plugin-settings writer call with no BumpGeneration reference in the same file" \
  "zz_guard_test_MissingBump.go"
clear_fixtures

# The same call, but the file also references BumpGeneration somewhere in
# it (the actual convention this guard enforces) must pass.
plant "internal/pages" "pages" "HasBump" 'import (
	"context"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins"
)

func zzGuardTestHasBump(ctx context.Context, db data.DBTX) {
	_ = data.NewPluginRepo(db).UpsertPluginSetting(ctx, "com.example.tax", "rate", `+"`"+`"700"`+"`"+`)
	plugins.SharedBus(db).BumpGeneration()
}'
expect_pass "a plugin-settings writer call that also references BumpGeneration in the same file"
clear_fixtures

# A call planted in a _test.go file must not trip the guard — tests write
# settings directly all the time with no asker cache to invalidate.
path="internal/pages/zz_guard_test_TestFileCall_test.go"
fixtures+=("${path}")
printf 'package pages\n\nimport (\n\t"context"\n\t"testing"\n\n\t"github.com/universaltill/universal-till/internal/data"\n)\n\nfunc TestZzGuardTestFileCall(t *testing.T) {\n\t_ = data.NewPluginRepo(nil).UpsertPluginSetting(context.Background(), "com.example.tax", "rate", "700")\n}\n' >"${path}"
expect_pass "a call inside a _test.go file"
clear_fixtures

# An inline plugin-settings-bump:allow escape hatch on the SAME LINE as
# the writer call must silence an otherwise-real finding, same same-line
# convention as guard-kiosk-engine.sh's kiosk-engine-guard:allow.
plant "internal/pages" "pages" "AllowedException" 'import (
	"context"

	"github.com/universaltill/universal-till/internal/data"
)

func zzGuardTestAllowedException(ctx context.Context, db data.DBTX) {
	_ = data.NewPluginRepo(db).UpsertPluginSetting(ctx, "com.example.tax", "rate", `+"`"+`"700"`+"`"+`) // plugin-settings-bump:allow this setting has no .ask-hook asker to invalidate
}'
expect_pass "a plugin-settings writer call with a same-line plugin-settings-bump:allow comment"
clear_fixtures

# Independent-review finding (ut-docs#1357, B1): the allow comment must be
# scoped to its OWN line, not the whole file — a second, unmarked
# writer-call line in the same file (with no BumpGeneration() reference
# either) must still be rejected, even though the file also contains an
# allowed line. A file-scoped escape hatch would silently disarm this.
plant "internal/pages" "pages" "PartialAllowInSameFile" 'import (
	"context"

	"github.com/universaltill/universal-till/internal/data"
)

func zzGuardTestPartialAllowAllowed(ctx context.Context, db data.DBTX) {
	_ = data.NewPluginRepo(db).UpsertPluginSetting(ctx, "com.example.tax", "a", `+"`"+`"1"`+"`"+`) // plugin-settings-bump:allow reviewed, no asker reads this one
}

func zzGuardTestPartialAllowUnmarked(ctx context.Context, db data.DBTX) {
	_ = data.NewPluginRepo(db).UpsertPluginSettingScoped(ctx, "com.example.tax", "b", `+"`"+`"2"`+"`"+`, "global", false)
}'
expect_fail "a second, unmarked writer-call line in a file that also has an allowed line" \
  "zz_guard_test_PartialAllowInSameFile.go"
clear_fixtures

# ut-docs#1942: the scan must also cover cmd/, scripts/, and e2e/ — not just
# internal/ — since nothing stops a future seed/smoke tool under one of
# those trees from writing a plugin setting an .ask-hook asker reads,
# unguarded. One planted violation per newly-covered directory, each in a
# real existing package so `go vet`/gofmt-adjacent tooling would still see
# valid Go if it ever ran over these fixtures.
plant "cmd/unitill-uninstall" "main" "CmdMissingBump" 'import (
	"context"

	"github.com/universaltill/universal-till/internal/data"
)

func zzGuardTestCmdMissingBump(ctx context.Context, db data.DBTX) {
	_ = data.NewPluginRepo(db).UpsertPluginSetting(ctx, "com.example.tax", "rate", `+"`"+`"700"`+"`"+`)
}'
expect_fail "a plugin-settings writer call under cmd/ with no BumpGeneration reference" \
  "zz_guard_test_CmdMissingBump.go"
clear_fixtures

plant "scripts/e2e_seed" "main" "ScriptsMissingBump" 'import (
	"context"

	"github.com/universaltill/universal-till/internal/data"
)

func zzGuardTestScriptsMissingBump(ctx context.Context, db data.DBTX) {
	_ = data.NewPluginRepo(db).UpsertPluginSettingScoped(ctx, "com.example.tax", "rate", `+"`"+`"700"`+"`"+`, "global", false)
}'
expect_fail "a plugin-settings writer call under scripts/ with no BumpGeneration reference" \
  "zz_guard_test_ScriptsMissingBump.go"
clear_fixtures

plant "e2e/seed_demo" "main" "E2eMissingBump" 'import (
	"context"

	"github.com/universaltill/universal-till/internal/data"
)

func zzGuardTestE2eMissingBump(ctx context.Context, db data.DBTX) {
	_ = data.NewPluginRepo(db).MergeAdditiveJSONMapSetting(ctx, "com.example.tax", "rate", `+"`"+`"700"`+"`"+`)
}'
expect_fail "a plugin-settings writer call under e2e/ with no BumpGeneration reference" \
  "zz_guard_test_E2eMissingBump.go"
clear_fixtures

# The writers' own definition file (internal/data/plugin_repo.go) must stay
# exempt: UpsertPluginSetting delegates to UpsertPluginSettingScoped in that
# same file, which would otherwise always trip this guard on itself.
if ! grep -q '\.UpsertPluginSettingScoped(' internal/data/plugin_repo.go; then
  echo "❌ FAIL: expected internal/data/plugin_repo.go to contain a UpsertPluginSettingScoped( call (test assumption stale)" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

# Fail CLOSED, not silently pass, if WRITER_RE ever matches zero production
# files (a future rename of these methods) — mirrors
# guard-price-history-sync_test.sh's missing-classification-file case.
# Overriding the pattern to something that matches nothing in this repo
# exercises that path without needing a real rename.
expect_fail "a WRITER_RE that matches no production file (simulated rename)" \
  "found no production caller" \
  'WRITER_RE=\.NoSuchWriterMethodAnywhere\('

# Baseline: the guard must still pass on the real, unmodified codebase —
# every known production call site (import_page.go, plugin_settings_page.go)
# was already fixed by ut-docs#222/#1351.
if run_guard >/tmp/guard_plugin_settings_bump_test_out.$$ 2>&1; then
  echo "✓ guard still passes on the clean codebase"
else
  echo "❌ FAIL: guard rejects the clean codebase (false positive introduced, or a real gap)" >&2
  cat /tmp/guard_plugin_settings_bump_test_out.$$ >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi
rm -f /tmp/guard_plugin_settings_bump_test_out.$$

if [[ "${FAIL_COUNT}" -gt 0 ]]; then
  echo "❌ guard-plugin-settings-bump_test.sh: ${FAIL_COUNT} case(s) failed" >&2
  exit 1
fi

echo "✓ guard-plugin-settings-bump_test.sh: all cases passed"
exit 0
