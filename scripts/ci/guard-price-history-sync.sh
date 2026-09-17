#!/usr/bin/env bash
#
# price_history is NOT an admin-synced table (internal/data/sync_admin_repo.go's
# nonAdminTables entry, ADR-0099): it is mutated (the append helpers UPDATE the
# previous open row's ends_at; item deletion DELETEs rows) and live-consulted
# at checkout (POSRepo.ResolveCurrentPrice reads an open price_history row
# BEFORE items' synced price, so it overrides it). Resolved by ADR-0099
# (ut-docs#2348, closing the question ut-docs#1671 deferred): the table stays
# out of adminTables — it is an unbounded ledger, not a current-state mirror —
# and instead ApplyAdmin's invalidateStalePriceHistoryOnSync closes every
# locally-open row for a synced item/variant on the next admin bundle that
# actually changes (ApplyAdmin only runs when the primary's admin
# fingerprint has moved — internal/pages/sync_admin.go's `!Unchanged` check
# — not literally every poll; a primary-side price edit always moves that
# fingerprint, since items/item_variants are themselves admin tables, so
# this still closes the gap ADR-0099 exists for), so a satellite can never
# keep charging a stale override once the primary has moved the underlying
# price.
#
# What this guard enforces (ADR-0099 Decision 3): no NEW, unreviewed write
# path to price_history ships unnoticed while the table is classified
# non-admin. A write from anywhere else silently diverges a satellite till's
# checkout price from the primary until the next admin poll — or forever, if
# that satellite never polls.
#
# Mechanism — the TABLE NAME in SQL write statements, not call-site names.
# The previous version of this guard grepped for calls to
# `.AppendPriceHistoryItem(`/`.AppendPriceHistoryVariant(` by name, and was
# proven blind the moment ut-docs#2314 added differently-named writers
# (internal/data/catalog_repo.go's appendPriceHistoryItemExec /
# appendPriceHistoryVariantExec — execer-based twins so the pre-write
# resolved-price read and the append share the caller's own BEGIN IMMEDIATE
# transaction). It would have been just as blind to ADR-0099's own
# invalidation step. Repointing an allowlist at call sites inherits the
# identical blind spot (a future caller can just as easily be a new,
# differently-named helper), so this greps for the literal SQL fragments
# `INSERT [OR IGNORE|REPLACE] INTO price_history` / `REPLACE INTO
# price_history` / `UPDATE price_history` / `DELETE FROM price_history`
# (case-insensitive, any run of whitespace between keywords) in every *.go
# file instead, attributes each hit to its enclosing top-level Go function,
# and fails unless that `file:function` pair is on the explicit
# ALLOWED_WRITERS list below.
#
# Adding a genuinely new, reviewed writer means adding ONE line to
# ALLOWED_WRITERS (`path/from/repo/root.go:FunctionName`, method receiver
# omitted) as part of that change's own review — never widening the regex.
# A hit with no enclosing `func` AT OR BEFORE ITS LINE (e.g. a package-level
# `var q = "UPDATE price_history ..."` string that appears before any
# top-level `func` in the file) can never be allowlisted and always fails.
# See "Known limitations" below for the one case this does NOT cover: a
# package-level declaration placed AFTER an allowlisted function is
# attributed to that function (this guard tracks the nearest PRECEDING
# `func` line, not brace scope) — deliberately documented rather than
# silently trusted, since an earlier draft of this comment overstated the
# guarantee.
#
# Fail-closed, same philosophy as before: a missing classification file is a
# loud error, never a silent pass (a plain `grep -q` treats "file not found"
# and "pattern not found" identically). If price_history is ever deliberately
# re-classified (removed from nonAdminTables), the guard gets out of the way.
#
# Known limitations (deliberately not papered over — read before trusting ✓):
#   * Line-based, literal (case-insensitive, whitespace-tolerant) match. A
#     statement split across lines between the keyword and the table name
#     (`"DELETE FROM\n" + "price_history"`), built from a table-name
#     variable, or schema-qualified/quoted (`main.price_history`,
#     `` `price_history` ``) is invisible. This is a trip-wire against an
#     honest new writer, not an adversarial one — same standing caveat
#     every grep guard under scripts/ci/ carries.
#   * scripts/** and e2e/** are excluded from the scan entirely — same
#     "test-support, not domain code" carve-out universal-till/CLAUDE.md's
#     data-access rule already makes for scripts/e2e_seed and friends
#     (guard-data-access.sh scopes its own scan to `internal` for the same
#     reason). scripts/e2e_seed/main.go and scripts/smoke_quickstart/main.go
#     both seed price_history directly (`INSERT OR IGNORE INTO
#     price_history`) — real, reviewed, but test/demo fixtures, not
#     production write paths a satellite till ever executes. If this guard
#     is ever widened to cover them, add them to ALLOWED_WRITERS rather
#     than silently relying on this exclusion.
#   * Attribution is "nearest preceding line starting with `func `", which
#     is what gofmt guarantees for a top-level declaration — it is NOT
#     brace-scope tracking, so a package-level declaration placed AFTER an
#     allowlisted function's closing brace is attributed to that function,
#     not flagged as unattributable. A closure inside a function is
#     attributed to that enclosing function, which is the intended unit of
#     review either way.
#   * ALLOWED_WRITERS has no liveness check against itself — see the
#     "stale allowlist entries" pass below, which does check this.
#   * Comments are skipped only when the whole line is a `//` comment; an
#     inline trailing comment containing one of the fragments is counted.
#   * THE CLOUD SetPrice DIRECTIVE PATH IS NOT PRIMARY-GATED TODAY. The
#     allowlisted catalog_repo.go twins are the reviewed WRITER, but one of
#     their callers — CatalogRepo.SetItemPrice, reached from
#     internal/pages/cloudsync_wire.go's directive application — runs on
#     every till regardless of role (internal/cloudsync/cloudsync.go's
#     directive loop has no requirePrimary; only the catalog SNAPSHOT push is
#     role-checked). The catalog item/variant edit form IS gated
#     (requirePrimary, internal/pages/catalog/handlers.go). So a satellite
#     can, today, receive a cloud-originated price edit and write its own
#     price_history row; ApplyAdmin's invalidation only cleans that up on the
#     next admin bundle that actually changes (an unbounded window on a
#     steady-state shop, not "the next poll"). That is an open dependency
#     on ut-docs#2353 (cloud directives
#     skipping requirePrimary — the whole directive family, not just
#     SetPrice), explicitly NOT a fact this guard verifies. A green run here
#     means "every SQL writer is a reviewed, allowlisted function", NOT
#     "every writer is primary-gated". ADR-0099 Decision 1 records the same
#     correction.
#
# Deliberately separate from guard-deadcode-baseline.sh rather than special-
# cased inside it: that guard's "burned down is not a failure" behaviour is
# a deliberate, independently-reviewed generic design (see its own header
# comment) — this is a narrower, price_history-specific assertion layered
# on top via plain grep, not a change to that shared mechanism.
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

# Every reviewed SQL writer to price_history, as `file:function` (method
# receiver omitted). Each entry names the review that approved it. Add a
# line here — with its own review — for a genuinely new writer; do not
# touch the grep.
ALLOWED_WRITERS=(
  # The original append pair (ut-docs#1671): close the prior open row, insert the new one.
  "internal/data/pos_repo.go:AppendPriceHistoryItem"
  "internal/data/pos_repo.go:AppendPriceHistoryVariant"
  # Item deletion removes its price rows (pre-#1671; named in the ADR's
  # Context as the "item deletion DELETEs rows" mutation).
  "internal/data/pos_repo.go:CleanupObsoleteItems"
  # Single-demo-item removal mirrors remove_demo.sql's cleanup (ADR-0090).
  "internal/data/demo_seed_repo.go:RemoveDemoItem"
  # ut-docs#2314: execer-based twins of the append pair, sharing the
  # catalog edit form's / SetItemPrice's own transaction. See the header:
  # the SetItemPrice CALLER is not primary-gated yet (ut-docs#2353).
  "internal/data/catalog_repo.go:appendPriceHistoryItemExec"
  "internal/data/catalog_repo.go:appendPriceHistoryVariantExec"
  # ADR-0099 Decision 2: satellite-side invalidation on every admin apply.
  "internal/data/sync_admin_repo.go:invalidateStalePriceHistoryOnSync"
)

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

# enclosing_func FILE LINE — prints the name of the nearest top-level
# `func` declared at or before LINE (gofmt puts every top-level declaration
# at column 0, so `^func ` is exact), or nothing if there is none.
enclosing_func() {
  awk -v L="$2" 'NR <= L && /^func / { fn = $0 } NR == L { print fn; exit }' "$1" \
    | sed -E 's/^func[[:space:]]+(\([^)]*\)[[:space:]]*)?([A-Za-z0-9_]+).*/\2/'
}

is_allowed() {
  local key="$1" allowed
  for allowed in "${ALLOWED_WRITERS[@]}"; do
    [[ "${allowed}" == "${key}" ]] && return 0
  done
  return 1
}

# ut-docs#2129: this scans the whole working tree ("." — a new writer can
# show up anywhere), so it also walks any agent worktree checked out under
# .claude/worktrees/ (gitignored, not repo content, but still real files on
# disk — see universal-till/CLAUDE.md's "Agent worktree hygiene"). Those are
# just copies of this same repo's tracked files at some other commit, so a
# worktree holding an unmerged branch that ALSO touches one of the
# allowlisted files trips this guard on its own already-tracked-elsewhere
# writer, with nothing new to review — a false failure, confirmed
# reproducible on the previous version of this guard. Excluded for the same
# reason a _test.go file is: it's not a NEW production writer.
#
# scripts/** and e2e/** are excluded too (see "Known limitations" above) —
# test-support tooling, not a production write path. Matched case-
# insensitively, with a run of whitespace tolerated between keywords, so
# `INSERT OR IGNORE INTO`/`INSERT OR REPLACE INTO`/`REPLACE INTO` and a
# tab/double-space before the table name don't slip past the way a plain
# `(INSERT INTO|UPDATE|DELETE FROM) price_history` literal did (independent
# review finding — scripts/e2e_seed and scripts/smoke_quickstart's own
# `INSERT OR IGNORE INTO price_history` were invisible to that literal, and
# would have been to a production writer using the same idiom).
hits="$(grep -rniE '(INSERT([[:space:]]+OR[[:space:]]+(IGNORE|REPLACE))?[[:space:]]+INTO|REPLACE[[:space:]]+INTO|UPDATE|DELETE[[:space:]]+FROM)[[:space:]]+price_history\b' --include='*.go' . 2>/dev/null \
  | grep -v '_test\.go:' \
  | grep -v '^\./\.claude/worktrees/' \
  | grep -v '^\./scripts/' \
  | grep -v '^\./e2e/' \
  | grep -vE '^[^:]+:[0-9]+:[[:space:]]*//' || true)"

violations=""
declare -A matched_writers
while IFS= read -r hit; do
  [[ -z "${hit}" ]] && continue
  file="${hit%%:*}"
  file="${file#./}"
  rest="${hit#*:}"
  line="${rest%%:*}"
  fn="$(enclosing_func "${file}" "${line}")"
  if [[ -z "${fn}" ]]; then
    violations+="  ${file}:${line}  (no enclosing func — a package-level SQL string can never be allowlisted)"$'\n'
  elif ! is_allowed "${file}:${fn}"; then
    violations+="  ${file}:${line}  in ${fn}  → not on ALLOWED_WRITERS as \"${file}:${fn}\""$'\n'
  else
    matched_writers["${file}:${fn}"]=1
  fi
done <<<"${hits}"

if [[ -n "${violations}" ]]; then
  echo "❌ price-history-sync guard: a SQL write to price_history outside the reviewed allowlist," >&2
  echo "   while price_history is still excluded from adminTables (internal/data/sync_admin_repo.go, ADR-0099)." >&2
  echo "   price_history is never synced primary→satellite; a new writer here can silently diverge a" >&2
  echo "   satellite till's checkout price from the primary until its next admin poll (or forever if it" >&2
  echo "   never polls). Before shipping this writer, either confirm it stays primary-gated (requirePrimary," >&2
  echo "   same pattern as the catalog edit form) or route it so ApplyAdmin's invalidateStalePriceHistoryOnSync" >&2
  echo "   covers it — then add its \"file:function\" to ALLOWED_WRITERS in scripts/ci/guard-price-history-sync.sh" >&2
  echo "   as part of the same review. Never widen the grep." >&2
  echo "" >&2
  printf '%s' "${violations}" >&2
  exit 1
fi

# Stale-allowlist check (independent review finding — the guard only ever
# asked "is this hit on the list", never "does every list entry still
# correspond to a real hit"): an ALLOWED_WRITERS entry with zero matching
# hits above means the function was renamed/deleted/rewritten since it was
# reviewed, and the stale line is now silently pre-approving whatever
# unrelated code next reuses that exact file:function pair — never itself
# reviewed for that write. Checked separately from (after) the violations
# above so the two failure reasons never get conflated in one message.
stale=""
for allowed in "${ALLOWED_WRITERS[@]}"; do
  [[ -n "${matched_writers[${allowed}]:-}" ]] || stale+="  ${allowed}"$'\n'
done
if [[ -n "${stale}" ]]; then
  echo "❌ price-history-sync guard: stale ALLOWED_WRITERS entry — no matching price_history write found." >&2
  echo "   The function was renamed, deleted, or rewritten since this line was reviewed; as written it" >&2
  echo "   pre-approves whatever unrelated code next reuses that exact file:function pair. Remove the" >&2
  echo "   line (if the writer is genuinely gone) or fix the path/name (if it just moved)." >&2
  echo "" >&2
  printf '%s' "${stale}" >&2
  exit 1
fi

echo "✓ price-history-sync guard: every SQL write to price_history sits in an allowlisted, reviewed function"
echo "  (this does NOT mean every writer is primary-gated — the cloud SetPrice directive path is not, ut-docs#2353; see this script's header)"
