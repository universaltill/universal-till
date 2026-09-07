# Code review — LAN-sync journal replay: generic FK-violation quarantine (ut-docs#1686)

**Date:** 2026-09-07
**Author (Dev):** Sonnet, inline (complexity:medium)
**Reviewer:** Opus, fresh-context subagent, isolated worktree
**Verdict:** PASS WITH NOTES — no blockers. One should-fix found and fixed
before merge (i18n gap); another should-fix was already independently
addressed in a companion `ut-docs` PR. Three nits, two fixed, one accepted.

## What changed

`applyJournal` (`internal/pages/sync_sales.go`), the LAN-sync journal-replay
path a primary uses to apply a replica's offline sales, could hit a raw,
uncaught SQLite FOREIGN KEY violation on two more insert stages beyond the
one ut-docs#1681 already fixed (`payments.method_id`):

| Gap | FK | Observed error |
|---|---|---|
| Replica-local cashier | `sales.cashier_id → users(id)` | `insert sale: ... FOREIGN KEY constraint failed (787)` |
| Replica-local item | `sale_lines.item_id → items(id)` | `insert sale lines batch: ... FOREIGN KEY constraint failed (787)` |

Either wedges a replica's entire subsequent replication forever (ADR-0065's
whole reason for having a poison-entry quarantine mechanism at all), because
neither was on `permanentJournalFailureReason`'s allowlist.

Fix: `permanentJournalFailureReason` gains a **generic** branch —
`isForeignKeyViolation(err)` (string-match on SQLite's stable "FOREIGN KEY
constraint failed" text, same approach as `data`'s own identically-named
helper in `reset_archive_repo.go`) — rather than N more special cases. The
reason string is distinguished only by which insert failed
(`foreignKeyViolationReason`, keyed off `data.POSRepo`'s own wrapped-error
prefix: `"insert sale:"` vs `"insert sale lines batch:"`), not by which
column, per the #1681 review's own recommendation: SQLite's error carries no
column detail, and unlike `payments.method_id` there is no safe placeholder
to substitute for a missing user or item — quarantining, not guessing or
substituting a fallback actor (e.g. "system" as cashier), is the considered
design choice, preserving audit-trail correctness.

Two new regression tests in `internal/pages/sync_sales_fk_quarantine_test.go`
(real migrated DB via `internal/db.Open`, reusing the existing
`newSyncSalesRealMigrationDeps` helper from #1681's own test file — same
reasoning: `internal/pages`'s old hand-rolled fixture predates the real FK
and can't reproduce this class of bug at all, per ut-docs#1657/#1676).

## TDD verification (re-verified independently, not by reading)

Reviewer reverted only the production fix (`internal/pages/sync_sales.go`
back to `main`), kept the new tests, ran them, confirmed both failed with
the exact reported raw errors:

```
sync_sales_fk_quarantine_test.go:45: applyJournal must not raw-error on an unknown cashier id (FK violation): insert sale: constraint failed: FOREIGN KEY constraint failed (787)
sync_sales_fk_quarantine_test.go:120: applyJournal must not raw-error on an unknown item id (FK violation): insert sale lines batch: constraint failed: FOREIGN KEY constraint failed (787)
```

Restored the fix, confirmed both pass again, each emitting its own distinct
quarantine Warn line. **Mutation-tested the tests themselves**, twice: swapping
the two reason strings made both tests fail (they genuinely discriminate
which insert failed, not just "quarantined or not"); stubbing out the
durable-record insert (`InsertJournalQuarantine`) made the cashier test fail
but the item test pass (see N1 below — fixed).

## Findings

### Should-fix (fixed before merge)

**F1 — the quarantine reason IS operator-facing, and three new strings had
no locale key.** `internal/pages/sync_quarantine_page.go`'s
`quarantineReasonKeys` map translates `/sync-quarantine`'s Reason column;
its own doc comment predicts exactly this gap ("a reason not in this map …
falls back to the raw stored string"). `guard-i18n.sh` cannot catch this —
the untranslated text is a DB value at render time, not a template literal.
Fixed: three new map entries plus `sync.quarantine_reason.{unknown_sale_reference,
unknown_line_reference,unresolved_reference}` keys added to all four locales
(en/fa/tr/ar — `guard-i18n.sh` parity confirmed). **fa/tr/ar translations
are machine-assisted** (this session's self-hosted Ollama endpoint,
`reference/translation.md`, is on the homelab LAN and unreachable from this
cloud session — same documented fallback ut-docs#1543/#1688 already use):
plain, direct translations of short technical phrases, not run through
native review. Flagging here per `reference/translation.md`'s "Label
machine translations" rule; a native speaker should spot-check before the
next Germany-pilot-adjacent locale audit.

Also added `TestPermanentJournalFailureReason_AllReasonsHaveALocaleKey`
(enumerates every error shape the classifier recognises and fails if any
non-empty result has no `quarantineReasonKeys` entry) — this is a
mechanical guard against a fourth recurrence of the exact gap this map's
own doc comment already warned about twice.

### Should-fix (already independently addressed)

**F2 — journal contract doc.** The reviewer (running in an isolated
worktree with no visibility outside `universal-till`) correctly flagged
that this changes documented replay semantics and should update
`ut-docs/reference/contracts/pos-lan-sync-journal.md`, same as #1681's own
S2 finding. This was already done in parallel, in
`universaltill/ut-docs#1693` (changelog row 1.7.0, updated Guarantees
bullet, and a stale header-version fix left over from #1681's own PR).

### Nits

**N1 (fixed)** — the item-id test was missing the `ListJournalQuarantine`
durable-record assertion its cashier sibling has (proven load-bearing by
the mutation test above: it's what would have caught a durable-record
regression on that path). Added.

**N2 (fixed)** — the new `isForeignKeyViolation`'s comment cited
`data.isUniqueViolation` as precedent, but the actual byte-identical twin
is `data.isForeignKeyViolation` (`internal/data/reset_archive_repo.go`,
unexported). Comment corrected to name it and explain why it's duplicated
rather than exported (a pages-layer classification of an already-unwrapped
error, not worth a new cross-package dependency for one predicate).

**N3 (fixed)** — the original comment named all four `sales` FK columns
(`customer_id`/`register_id`/`cashier_id`/`table_id`) as if all were
reachable from `applyJournal` today; only `cashier_id` actually is
(`SaleInput.CustomerID`/`RegisterID`/`TableID` are never set on this path).
Comment tightened to say the reason string deliberately names the wider
column set the insert *could* hit (so a future field addition doesn't need
this classifier revisited), not that all four are reachable now.

**N4 (accepted, not fixed)** — a `payments.method_id` FK violation outside
`EnsurePaymentMethod`'s guarded window (e.g. a method row deleted between
the ensure and the insert, same transaction) would now fall to this
generic branch and be quarantined rather than batch-rejected-and-retried,
slightly widening what #1681's review deliberately scoped `EnsurePaymentMethod`
failures out of (environmental/transient, not content-of-entry). Vanishingly
unlikely under SQLite's single-writer model within one transaction, and the
full payload is preserved for manual replay either way — not acted on.

### Verified clean (no action needed)

- **Broad-match over-swallow risk**: every FK in the insert path
  (`internal/db/migrations/001_init.sql`: `sales`→customers/registers/
  users/tables, `sale_lines`→items/item_variants, `payments`→sales/
  payment_methods, `sale_discounts`→sales/sale_lines,
  `sale_line_modifiers`→sale_lines) was enumerated. The externally-driven
  ones are all correctly quarantinable; the structural ones (`sale_id`,
  `sale_line_id`, `line_id`, referencing rows the same transaction just
  inserted) would now quarantine instead of 422 on what would actually be
  an internal `CompleteSale` bug — accepted as unreachable absent a bug
  that fails loudly in tests first, and the quarantine record + Warn-level
  Problem preserve the diagnostic signal either way.
- **False-positive classification risk**: `permanentJournalFailureReason`
  has exactly one call site, gated inside `pos.CompleteSale`'s own error
  branch — `EnsureStockLocation`'s and `ReturnedQuantities`' errors can
  never reach it.
- **Branch-ordering hazard**: `"insert sale:"` cannot match
  `"insert sale lines batch:"` (colon boundary), so the two `foreignKeyViolationReason`
  cases are disjoint regardless of order.
- Two recurring pipeline bug classes (missing `os.MkdirAll`, cwd-relative
  path instead of `paths.Data`): not applicable — no file writes, no path
  construction anywhere in this diff.
- No real client/shop name in test data (`replica-only-user-42`,
  `replica-only-item-99`, synthetic sale/receipt/till ids throughout).
- Repository pattern: diff adds zero SQL — pure error classification in
  the pages layer, confirmed by `guard-data-access.sh`.

## Verification (local gate, after all fixes above)

- `gofmt -l .` — clean
- `go build ./...` — clean
- `go vet ./...` — clean
- `go test ./internal/pages/...` (targeted, `-v`) — all new/affected tests
  pass, including `TestPermanentJournalFailureReason_AllReasonsHaveALocaleKey`
- `go test ./...` (full suite) — all green
- `golangci-lint run ./internal/pages/...` — 0 issues
- `bash scripts/ci/guard-i18n.sh` — ✓ (all locales match en.json, no
  missing Go-side i18n key literals)
- `bash scripts/ci/guard-data-access.sh` — ✓
- Every other CI-blocking guard in `ci.yml`'s `build` job run locally —
  green (unaffected surfaces: no new routes, no self-order/kiosk touch, no
  Android/webkit/emoji-font/htmx/autofill/makefile-version regression)
- `go test ./internal/pages/... ./internal/pos/... ./internal/data/... -race`
  was attempted and is **not part of this repo's actual CI** (`ci.yml`
  runs plain `go test`, no `-race`, confirmed by reading the workflow) —
  it hit an unrelated pre-existing timeout in `internal/data`
  (`TestLoadButtons_CarriesItemCategoryID`'s migration path, nothing to do
  with this diff) under the race detector's per-test slowdown against this
  sandbox's default 600s per-package timeout. Not investigated further
  since it isn't the repo's actual gate; the plain (non-race) full suite,
  which is CI's real invocation, is green.

## Deferred / not built here

- Native (non-machine-assisted) review of the fa/tr/ar strings added by
  this PR (see F1) — flagged, not blocking.
- N4 above (payments.method_id race reclassification) — noted, not acted
  on per its own reasoning.

## Merge

`merge_method: "merge"` (never squash/rebase — preserves real commit
attribution, ut-docs#250).
