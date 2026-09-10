#!/usr/bin/env bash
#
# Regression test for guard-price-history-sync.sh (ut-docs#1671): proves
# the guard flags a real caller of AppendPriceHistoryItem/Variant planted
# outside internal/pos/pricing.go — including one planted in a DIFFERENT
# file inside internal/pos itself, proving the exclusion is scoped to
# pricing.go specifically, not the whole package (independent review
# finding) — proves it does NOT flag a call planted in a _test.go file,
# proves it fails closed (not open) when the classification file is
# missing (independent review finding), and proves it gets out of the way
# once price_history is no longer classified in nonAdminTables. Also
# proves the guard still passes on the real, unmodified codebase.
#
# Unlike an earlier version of this test, none of this mutates the real,
# tracked internal/data/sync_admin_repo.go — the reclassification and
# missing-file cases instead point the guard's SYNC_CLASSIFICATION_FILE
# override at scratch files, the same convention
# guard-migration-version-collision.sh's MIGRATIONS_DIR override uses, so
# a killed run can never leave a stray .bak or a clobbered file mode on
# real source.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-price-history-sync.sh"
FAIL_COUNT=0

fixtures=()
scratch_dir=""
# Invoked indirectly via `trap ... EXIT`, not a direct call -- shellcheck cannot see that (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() {
  local status=$?
  if [[ ${#fixtures[@]} -gt 0 ]]; then
    for f in "${fixtures[@]}"; do
      [[ -n "${f}" && -f "${f}" ]] && rm -f "${f}"
    done
  fi
  if [[ -n "${scratch_dir}" && -d "${scratch_dir}" ]]; then
    rm -rf "${scratch_dir}"
  fi
  exit "${status}"
}
trap cleanup EXIT

scratch_dir="$(mktemp -d)"

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
  # SYNC_CLASSIFICATION_FILE override) — passed through explicitly rather
  # than exported, so a case that doesn't need one can't leak into the next.
  env "$@" bash "${GUARD}"
}

expect_fail() {
  local label="$1"
  shift
  if run_guard "$@" >/tmp/guard_price_history_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_price_history_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_price_history_test_out.$$
}

expect_pass() {
  local label="$1"
  shift
  if run_guard "$@" >/tmp/guard_price_history_test_out.$$ 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat /tmp/guard_price_history_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_price_history_test_out.$$
}

# A real caller planted outside internal/pos entirely (an admin page, say)
# while price_history is still non-admin must be rejected.
plant "internal/pages" "pages" "RealCaller" 'import (
	"context"
	"time"

	"github.com/universaltill/universal-till/internal/pos"
)

func zzGuardTestRealCaller(ctx context.Context, repo pos.PricingRepo) {
	_ = pos.AppendPriceHistoryItem(ctx, repo, "itm1", 100, time.Now())
}'
expect_fail "a caller of AppendPriceHistoryItem outside internal/pos"
clear_fixtures

plant "internal/pages" "pages" "RealCallerVariant" 'import (
	"context"
	"time"

	"github.com/universaltill/universal-till/internal/pos"
)

func zzGuardTestRealCallerVariant(ctx context.Context, repo pos.PricingRepo) {
	_ = pos.AppendPriceHistoryVariant(ctx, repo, "var1", 100, time.Now())
}'
expect_fail "a caller of AppendPriceHistoryVariant outside internal/pos"
clear_fixtures

# A call planted inside internal/pos itself, but in a DIFFERENT file than
# pricing.go, must ALSO be rejected — the exclusion is scoped to
# pricing.go specifically (the file that legitimately wraps these), not
# the whole package, which is 40 files of real production code including
# exactly the kind of scheduled-price-change logic that would matter here.
plant "internal/pos" "pos" "OtherFileInPos" 'import (
	"context"
	"time"
)

func zzGuardTestOtherFileInPos(ctx context.Context, repo PricingRepo) {
	_ = repo.AppendPriceHistoryItem(ctx, "itm1", 100, time.Now())
}'
expect_fail "a caller inside internal/pos but outside pricing.go"
clear_fixtures

# A call planted in a _test.go file outside internal/pos must not trip the
# guard (mirrors the deadcode guard's own test-file exclusion).
path="internal/pages/zz_guard_test_TestFileCall_test.go"
fixtures+=("${path}")
printf 'package pages\n\nimport (\n\t"context"\n\t"testing"\n\t"time"\n\n\t"github.com/universaltill/universal-till/internal/pos"\n)\n\nfunc TestZzGuardTestFileCall(t *testing.T) {\n\t_ = pos.AppendPriceHistoryItem(context.Background(), nil, "itm1", 100, time.Now())\n}\n' >"${path}"
expect_pass "a call inside a _test.go file"
clear_fixtures

# Missing classification file must fail CLOSED (loud error), not silently
# pass as if price_history had been reclassified — a plain `grep -q`
# treats "file not found" and "pattern not found" identically, so this
# needs the guard's explicit existence check, not just the grep.
expect_fail "a missing SYNC_CLASSIFICATION_FILE" \
  "SYNC_CLASSIFICATION_FILE=${scratch_dir}/does-not-exist.go"

# Once price_history is no longer classified non-admin (a deliberate
# re-classification), the guard must get out of the way even with a real
# caller present — verified against a scratch classification file, never
# the real tracked one.
printf 'package data\n\nvar nonAdminTables = map[string]string{\n\t"other_table": "unrelated",\n}\n' >"${scratch_dir}/sync_admin_repo.go"
plant "internal/pages" "pages" "ReclassifiedCaller" 'import (
	"context"
	"time"

	"github.com/universaltill/universal-till/internal/pos"
)

func zzGuardTestReclassifiedCaller(ctx context.Context, repo pos.PricingRepo) {
	_ = pos.AppendPriceHistoryItem(ctx, repo, "itm1", 100, time.Now())
}'
expect_pass "a caller once price_history is no longer classified in nonAdminTables" \
  "SYNC_CLASSIFICATION_FILE=${scratch_dir}/sync_admin_repo.go"
clear_fixtures

# Baseline: the guard must still pass on the real, unmodified codebase.
if run_guard >/tmp/guard_price_history_test_out.$$ 2>&1; then
  echo "✓ guard still passes on the clean codebase"
else
  echo "❌ FAIL: guard rejects the clean codebase (false positive introduced)" >&2
  cat /tmp/guard_price_history_test_out.$$ >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi
rm -f /tmp/guard_price_history_test_out.$$

if [[ "${FAIL_COUNT}" -gt 0 ]]; then
  echo "❌ guard-price-history-sync_test.sh: ${FAIL_COUNT} case(s) failed" >&2
  exit 1
fi

echo "✓ guard-price-history-sync_test.sh: all cases passed"
exit 0
