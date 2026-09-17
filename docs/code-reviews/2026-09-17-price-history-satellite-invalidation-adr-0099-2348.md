# Code review: ADR-0099 Decisions 2-4 — price_history satellite invalidation (ut-docs#2348)

- **Date**: 2026-09-17
- **Card**: ut-docs#2348 (implements ADR-0099, resolving ut-docs#1671)
- **Complexity**: hard
- **Author (Dev)**: Fable subagent, in the shared checkout
- **Reviewer**: Opus, two rounds, both isolated worktrees

## What shipped

ADR-0099 (`ut-docs/adr/0099-price-history-stays-non-admin-satellite-invalidates-on-sync.md`)
decided `price_history` stays out of `adminTables` (unbounded ledger, not
a current-state mirror) and instead `ApplyAdmin` gets a new invalidation
step. This card implements Decisions 2-4:

- `internal/data/sync_admin_repo.go`: new `invalidateStalePriceHistoryOnSync`
  — two set-based `UPDATE price_history SET ends_at = CURRENT_TIMESTAMP`
  statements (unconditional, not scoped to `starts_at <= now`, per the
  ADR's own review correction — a future-dated stale row must also be
  closed), called inside `ApplyAdmin`'s existing transaction alongside
  `backfillCodelessSyncedVariants`. `nonAdminTables["price_history"]`'s
  entry rewritten to record the resolution (Decision 4).
- `scripts/ci/guard-price-history-sync.sh` + its `_test.sh`: repointed
  from call-site-name grepping (blind to ut-docs#2314's differently-named
  writers) to SQL table-write grepping, attributed to enclosing function,
  checked against an explicit `ALLOWED_WRITERS` list (Decision 3).
- `internal/data/sync_admin_repo_test.go`: new regression test covering
  active/future-dated stale rows, for both items and variants, plus an
  already-closed row left untouched.

## Independent review — round 1 (Opus, isolated worktree)

Found the Go change (Decision 2) correct and its TDD claim genuine
(independently reverted/restored, confirmed red→green). Found real
problems in the guard rewrite:

1. **[BLOCKER, fixed] `shellcheck` failure.** 9× SC2016 on
   `guard-price-history-sync_test.sh`'s backtick-in-single-quote
   fixtures — would have turned CI red. Fixed with targeted
   `# shellcheck disable=SC2016` directives, reason on the preceding
   comment line, per `CLAUDE.md`'s convention.
2. **[HIGH, fixed] Regex blind spot.** The literal
   `(INSERT INTO|UPDATE|DELETE FROM) price_history` pattern was blind to
   this codebase's own dominant `INSERT OR IGNORE/REPLACE INTO` idiom and
   to extra whitespace — two real in-tree writers
   (`scripts/e2e_seed/main.go`, `scripts/smoke_quickstart/main.go`)
   already used exactly this idiom and were invisible to the guard.
   Widened to case-insensitive, whitespace-tolerant, covering
   `INSERT [OR IGNORE|REPLACE] INTO` / `REPLACE INTO` / `UPDATE` /
   `DELETE FROM`; `scripts/**` and `e2e/**` excluded from the scan
   (test-support tooling, mirroring `guard-data-access.sh`'s own scope
   and `CLAUDE.md`'s existing carve-out for `scripts/e2e_seed`).
3. **[MEDIUM, fixed] False guarantee.** The header claimed a
   package-level SQL string "can never be allowlisted" — actually true
   only when it precedes every `func`; one placed AFTER an allowlisted
   function's closing brace inherits its allowlisting (nearest-preceding-
   func attribution, not brace-scope tracking). Corrected to state this
   honestly as a documented limitation.
4. **[MEDIUM, fixed] No allowlist liveness check.** A stale
   `ALLOWED_WRITERS` entry (function renamed/deleted since review) stayed
   silently valid forever, pre-approving whatever unrelated code next
   reused that exact `file:function` pair. Added a check: any entry with
   zero matching hits fails loudly, naming it.
5. **[MEDIUM/LOW, fixed] Overstated "on every poll"/"next poll" wording**
   in three places — `ApplyAdmin` only runs when the primary's admin
   fingerprint actually changes (`sync_admin.go`'s `!Unchanged` gate), not
   literally every poll. The real window on the still-open cloud-directive
   gap (ut-docs#2353) is unbounded on a steady-state shop, not "one poll".
   Corrected; a related overstated bundle-completeness justification
   softened.

## Independent review — round 2 (Opus, isolated worktree, scoped to the fixes)

Earned per the standing rule (round 1 found a CI-blocker) — scoped to
verifying the five fixes above, not a full re-review of the unchanged Go
code. Verdict: **safe to merge.**

- **F1**: re-ran real `shellcheck 0.9.0` (CI's pinned baseline) —
  0 issues across all of `scripts/ci/*.sh`. Confirmed 9/9 SC2016 gone.
- **F2**: independently planted 12 evasion variants (including a real tab
  and mixed case the committed test doesn't itself cover) — all correctly
  rejected; no false positives on `price_history_archive` or a read-only
  `SELECT`. Confirmed the two real writers this was about are the only
  ones under `scripts/`, and confirmed the exclusion is correctly
  path-anchored (`internal/nested_scripts/` remains in scope). Flagged one
  narrow, accepted residual gap: a future writer added to
  `scripts/promote-super-admin` or `scripts/run_migrations` (today: none)
  would now be missed — same trade-off `guard-data-access.sh` already
  makes by scoping to `internal` only.
- **F3**: confirmed the new test case's attribution target
  (`invalidateStalePriceHistoryOnSync`'s closing brace) is located
  dynamically, not hardcoded — 9 other functions follow it in the file, so
  a naive append would have proven a different fact. Confirmed the real,
  tracked file's md5 is identical before and after the full suite runs
  (no leftover mutation).
- **F4**: not content with the committed test's own bogus-entry injection,
  independently made a REAL writer (`RemoveDemoItem`) disappear by
  rewriting its SQL to a different table, and confirmed the guard caught
  the resulting orphaned allowlist entry by name, with no violations
  conflated into the same message. File restored, md5 identical after.
- **F5/F6**: cross-checked the corrected wording against
  `internal/pages/sync_admin.go:423`'s actual `!Unchanged` gate and
  `adminTables`'s membership of `items`/`item_variants` directly, not
  taken on the comment's word.
- Full regression suite re-run clean: `gofmt`, `go build`, `go test
  ./internal/data/... ./internal/pos/...`, `golangci-lint` (0 issues),
  `guard-price-history-sync.sh`, its own `_test.sh` (all cases),
  `guard-data-access.sh`.
- Two cosmetic, non-blocking nits noted (a stale "on every poll" phrase in
  an unrelated, unchanged part of the same doc comment; a commit-message
  case-count off-by-one) — neither required a code change.

## Verified beyond automated tests

- TDD claim for the Go fix independently re-verified twice (round 1's
  Opus pass, plus this session's own revert/restore before round 1
  started).
- The guard's own regression suite independently re-verified via
  adversarial planting beyond what the committed test cases exercise
  (round 2: real tab whitespace, mixed case, a genuinely-orphaned real
  allowlist entry — not just the test script's own scripted cases).
- Confirmed via `git log`/inspection that the two "pre-existing writer"
  allowlist entries (`CleanupObsoleteItems`, `RemoveDemoItem`) are real,
  long-standing production code, not invented or mis-attributed.
- Confirmed ADR-0099's stated non-goals respected: no schema migration, no
  `adminTables`/FK-ordering change, no touch to the cloud `SetPrice`
  directive path (ut-docs#2353, explicitly out of scope).

## Full gate

`gofmt -l .` empty · `go build ./...` clean · `go test
./internal/data/... -run TestAdmin -v` all green (incl. the new
`TestAdminApply_InvalidatesStaleOpenPriceHistory`) · `go test
./internal/data/... ./internal/pos/...` green · `golangci-lint run
./internal/data/... ./scripts/...` 0 issues · `shellcheck scripts/ci/*.sh`
0 issues (verified with the CI-pinned v0.9.0) · `guard-price-history-sync.sh`
✓ on the real tree · its own `_test.sh` — 20 cases (was 11; 9 new, covering
every round-1 finding) all pass · `guard-data-access.sh` ✓.

## Verdict

**Safe to merge.** Both review rounds' findings addressed; the second,
scoped round found nothing new blocking.

---
_Generated by [Claude Code](https://claude.ai/code)_
