#!/usr/bin/env bash
#
# ut-docs#1671: price_history is mutated (AppendPriceHistoryItem/Variant
# UPDATE the previous open row's ends_at, and item deletion DELETEs rows)
# and is live-consulted at checkout (POSRepo.ResolveCurrentPrice reads an
# open price_history row BEFORE items' synced price, so it overrides it) —
# see internal/data/sync_admin_repo.go's nonAdminTables entry for the full
# reasoning. It stays out of adminTables today because nothing in
# production writes it: AppendPriceHistoryItem/Variant have no caller
# outside internal/pos and its own tests (confirmed via
# scripts/ci/deadcode-baseline.txt — the whole internal/pos/pricing.go file
# is currently whole-program-unreachable).
#
# That's a decision, not a fact that holds forever: the moment a real
# caller appears (a scheduled-price-change admin page, say), a satellite
# till could start diverging from the primary on checkout price with
# nothing to catch it. This guard is that catch — it fails the build if a
# real caller of AppendPriceHistoryItem/Variant shows up anywhere outside
# internal/pos while price_history is still classified non-admin, forcing
# the sync classification to be revisited (join adminTables with the
# FK-ordering + mutated/deleted-row handling other admin tables already
# got, or confirm the new caller stays primary-gated so a satellite never
# writes it at all) instead of shipping silently.
#
# Deliberately separate from guard-deadcode-baseline.sh rather than special-
# cased inside it: that guard's "burned down is not a failure" behaviour is
# a deliberate, independently-reviewed generic design (see its own header
# comment) — this is a narrower, price_history-specific assertion layered
# on top via plain grep, not a change to that shared mechanism.
#
# Known limitation (independent review): this is a direct-caller check, not
# a call-graph analysis. A one-hop wrapper added INSIDE internal/pos (e.g.
# a new internal/pos/scheduled_price.go function that calls
# AppendPriceHistoryItem itself, then gets called from internal/pages) is
# invisible to this guard, and to guard-deadcode-baseline.sh's whole-program
# analysis too (a burned-down baseline entry is explicitly not a failure
# there). If price_history ever gets a real feature built around it, don't
# over-trust this guard's green check alone — grep for the actual call
# sites by hand as part of that change's own review.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

# Overridable so the regression test can point this at a scratch copy
# instead of mutating the real, tracked classification file (independent
# review: an earlier version of this guard's test edited
# internal/data/sync_admin_repo.go in place via a backup-file dance, which
# risked a stray .bak / clobbered file mode on an interrupted run — same
# class of risk guard-migration-version-collision.sh's MIGRATIONS_DIR
# override exists to avoid).
SYNC_CLASSIFICATION_FILE="${SYNC_CLASSIFICATION_FILE:-internal/data/sync_admin_repo.go}"

if [[ ! -f "${SYNC_CLASSIFICATION_FILE}" ]]; then
  echo "❌ price-history-sync guard: ${SYNC_CLASSIFICATION_FILE} does not exist (renamed or missing?)" >&2
  echo "   This guard can't verify price_history's sync classification without it — fix the path" >&2
  echo "   (via SYNC_CLASSIFICATION_FILE) rather than let this check silently no-op." >&2
  exit 1
fi

if ! grep -q '"price_history":' "${SYNC_CLASSIFICATION_FILE}"; then
  echo "✓ price-history-sync guard: price_history no longer classified in nonAdminTables, nothing to enforce"
  exit 0
fi

callers="$(grep -rnE '\.(AppendPriceHistoryItem|AppendPriceHistoryVariant)\(' --include='*.go' . 2>/dev/null \
  | grep -v '^\./internal/pos/pricing\.go:' \
  | grep -v '_test\.go:' || true)"

if [[ -n "${callers}" ]]; then
  echo "❌ price-history-sync guard: AppendPriceHistoryItem/Variant now has a real caller outside internal/pos," >&2
  echo "   but price_history is still excluded from adminTables (internal/data/sync_admin_repo.go, ut-docs#1671)." >&2
  echo "   A production writer here can silently diverge a satellite till's checkout price from the primary." >&2
  echo "   Revisit the sync classification before shipping this caller: either join price_history to" >&2
  echo "   adminTables with the FK-ordering + mutated/deleted-row (ends_at UPDATE, hard DELETE) handling" >&2
  echo "   other admin tables already got, or confirm price changes stay primary-gated (requirePrimary," >&2
  echo "   same pattern as registers_page.go/locations_page.go) so a satellite never writes it at all." >&2
  echo "" >&2
  echo "${callers}" >&2
  exit 1
fi

echo "✓ price-history-sync guard: no production caller of AppendPriceHistoryItem/Variant outside internal/pos"
