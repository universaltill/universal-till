# Code review: `reset-transactions` archives instead of deletes (ut-docs#187, ADR-0042)

## What shipped

`POSRepo.ResetTransactionHistory` used to permanently `DELETE` every
sale/payment/invoice/shift/stock-movement — a latent GoBD compliance gap
(ut-docs#40/#187). Per ADR-0042 (`ut-docs/adr/0042-...md`, product owner's
2026-08-13 decision), it now **archives instead of deletes**:

- `internal/db/migrations/040_reset_archive.sql`: `reset_batches(id,
  created_at, actor_id, sales_count)` + ten `*_archive` tables (nine from
  the ADR's own list, plus `sale_line_modifiers_archive` — added because
  that table cascade-deletes from `sale_lines` and would otherwise still
  be silently destroyed, contradicting "nothing is destroyed"). Archive
  tables are column-identical to their live twins plus `reset_batch_id`,
  deliberately drop FKs-to-live-tables and cross-batch PK/UNIQUE
  constraints (a shop can reset, trade, and reset again — the same
  `receipt_no` can legitimately appear in two different archived batches).
- `internal/data/reset_archive_repo.go` (new): `ResetTransactionHistory`
  (rewritten to archive+clear in one transaction, batch-tagged),
  `ListResetBatches`, `RestoreResetBatch` (whole-batch only, refuses with
  `ErrShopHasTradedSinceReset` unless `sales`/`held_sales`/`shifts`/
  `stock_movements` are all empty — `NextReceiptNo` derives the next
  receipt number as `MAX(receipt_no)` over live `sales`, so restoring on
  top of post-reset trading would collide).
- `internal/pages/data_api.go`: reset handler updated; new `GET
  /api/data/reset-archives` (list) and `POST
  /api/data/reset-archives/{id}/restore` (manager-gated, typed `RESTORE`
  confirmation, mirroring the existing `RESET` pattern).
- `web/ui/pages/settings.html`: new "Reset archives" subsection in the
  existing Data card — a table of batches with a per-row Restore button;
  `settings.data.warn`/`reset_confirm_dialog` rewritten (no longer claim
  irreversibility).
- `web/locales/{en,ar,fa,tr}.json`, `web/help/{en,ar,fa,tr}/reports.md`:
  new keys / updated prose in all four locales.
- No delete-archive/purge endpoint — deliberately out of scope pending a
  retention-window decision (ut-docs#635, opened during BA scoping).

Built by a Fable subagent (complexity:hard), TDD-first.

## Independent review (Opus, isolated worktree)

Verdict up front: **one real blocker, fixed same-session; safe to merge
after the fix.** Full findings below; see the ADR's own "Addendum"
section for the narrative.

### Blocker — found and fixed

**Restore could be permanently bricked by the button right below it on
the same Settings card.** `RestoreResetBatch`'s precondition only checked
that the *transaction* tables were empty — it never checked that the
*catalog/customer rows the archive points at* still existed. Reset empties
`sale_lines`/`stock_movements`/`sales`, which are exactly the tables
`CleanupObsoleteItems`, "Remove sample data," and `EraseCustomer` read to
decide something is safe to remove/anonymise. Reviewer proved it
end-to-end: reset → "Remove sample data" (the very next action the card's
own UI suggests) → Restore returns a raw HTTP 500 carrying
`restore: sale_lines: constraint failed: FOREIGN KEY constraint failed
(787)`, permanently (no delete-archive action exists to clear the dead
batch).

Rollback itself was already correct (nothing partially applied, archive
intact) — this was a broken "recoverable" promise plus an ugly error, not
data loss.

**Fix**: `RestoreResetBatch` now catches this specific class of failure
(`isForeignKeyViolation`, string-matched on SQLite's stable "FOREIGN KEY
constraint failed" text — `modernc.org/sqlite` exposes no typed error, and
no other code in this repo yet parses driver errors) and returns a new
named `ErrArchiveReferencesRemoved` → HTTP 422, with a distinct localized
client message (all 4 locales), instead of the raw SQL string. Same
refuse-cleanly-and-touch-nothing shape as `ErrShopHasTradedSinceReset`.

Regression tests added, each **independently revert-verified**: reverted
the specific fix, confirmed the test fails with the exact pre-fix error,
restored the fix, confirmed it passes again.
- `internal/data/reset_test.go`:
  `TestRestoreRefusesWhenArchiveReferencesRemovedItem` — repo layer, real
  migrated DB.
- `internal/pages/data_api_test.go`:
  `TestResetArchivesRestore_ReferencesRemovedItem` — HTTP layer. **Had to
  add a `newRealDBDataAPIDeps` helper** (`internal/db.Open`, mirroring the
  existing `demo_seed_opt_in_test.go` precedent) rather than use the
  file's existing `seedForPages` fixture: that fixture's hand-rolled
  `sale_lines` table (`ui_smoke_test.go`) declares only the `sale_id` FK,
  not `item_id → items` — so the test silently couldn't reproduce the bug
  at all against it (confirmed: it passed against the *unfixed* code
  first, a false-pass caught before it shipped). Exactly the fixture-drift
  trap the tester skill warns about.

**Deliberately not fixed here** (too broad for this card, tracked as
ut-docs#640): teaching the three predicates themselves to consult the
archive tables, so an item/customer an archive still needs is never
removable in the first place. This is root-cause; today's fix is
defense-in-depth and stays regardless.

### Real-but-non-blocking — fixed same-session

- **Restore's audit entry had no actor** (`InsertAudit(ctx, tx, "", …)`,
  hardcoded empty string — `RestoreResetBatch` didn't even take an
  `actorID` param, while the handler already had `auth.UserID(r)` and
  passed it to reset). On a card whose whole purpose is GoBD
  record-keeping, "who restored the archive: unknown" was the wrong
  record. Fixed: threaded `actorID` through, audited properly.
- **Credit-note archive/restore path was untested.** The two-phase
  invoices self-FK handling (`original_invoice_id`) works — reviewer
  proved it manually — but `seedFullSale`'s one invoice has no credit
  note, so neither existing test exercised it. Added
  `TestResetThenRestoreRoundTrip_CreditNote`, revert-verified (swapping
  the two insert phases reproduces the exact FK failure the ordering
  guards against).
- Nitpick, fixed: `ListResetBatches` had no `LIMIT` (added 200 — a reset
  is rare, pre-launch tooling, not a routine action). Nitpick, fixed:
  reset's audit payload key renamed `sales_deleted` → `sales_archived`
  (nothing reads it; it was just inaccurate).

### Nitpicks — accepted, deferred, not fixed

- `settings_page.go` formats the archive list's `CreatedAt` from a UTC
  string with no timezone conversion, while the backups table it mirrors
  formats from a local `time.Time` — two tables on one page, two time
  bases. Cosmetic; not fixed to keep this diff scoped.
- Locale key naming drifts slightly (`archive_date_col`/`archive_sales_col`
  vs. the `archives_*` prefix used everywhere else in this feature).
  Cosmetic, no functional effect.
- `resetArchiveTables`' column lists are hand-maintained, not generated —
  a future `ALTER TABLE sales ADD COLUMN` would silently stop archiving
  that column with no compile error or test failure. This diff itself
  fixed a live instance of the same drift class in `ui_smoke_test.go`'s
  fixture (was missing `order_status`/`order_status_updated_at`/print-
  failure columns already present in production). No generator exists in
  this codebase to reach for; flagging for awareness, not a blocker.

### Checked and confirmed fine (independent review, stated plainly)

- FK archive/restore ordering verified against every relevant migration
  (`001_init.sql`, `002_held_sales.sql`, `016_invoices.sql`, every later
  `ALTER`) in both directions — correct.
- No invoice-numbering collision gap: `invoices.sale_id` is `NOT NULL`
  with an FK to `sales`, so invoices can't be non-empty while `sales` is
  empty — the existing empty-check transitively covers it.
- Transaction safety: one `BeginTx`/`defer Rollback()`/`Commit`-last in
  both methods; empirically clean on a mid-restore FK failure.
- Manager gate present on both new endpoints, covered by the existing
  `TestDataAPI_AllEndpointsRequireManager` matrix.
- `report_archive` (ADR-0040) untouched anywhere in the diff — grepped,
  not just trusted from comments.
- No delete-archive/purge endpoint accidentally added.
- Money: integer minor units throughout; test assertions check real
  values (`total=100`, `opening_cash=5000`, …), not just row counts.
- The two recurring bug classes this pipeline watches for
  (missing `os.MkdirAll`, cwd-relative path instead of `paths.Data`) are
  N/A — this diff is DB-only, no file writes.
- i18n: all new keys present with real translations in all 4 locales;
  `RESET`/`RESTORE` correctly left untranslated inside localized text; no
  literal `left`/`right` in the new markup (RTL-safe).
- The 10th archive table (`sale_line_modifiers_archive`) is the right
  call, not scope creep — confirmed the cascade-delete via
  `017_item_modifiers.sql`.

## Verified beyond automated tests

- **Driven in a real running instance** (built binary, real migrated DB,
  headless Chromium at `/opt/pw-browsers`): reset via the API, screenshot
  of Settings → Data's new "Reset archives" table; wrong-confirm and
  correct-confirm restore flows clicked through for real (not just
  asserted in a template string) — caught and fixed a **second**, purely
  client-side bug this way: the restore-status span (`#archives-msg`)
  lives outside the `#reset-archives` div, so on the *last* batch
  restoring, the table was left showing dangling `ARCHIVED`/`SALES`
  headers over nothing instead of the "No archived resets yet" empty
  state. Fixed: swap in the empty-state text client-side when the last
  row is removed. Verified before/after with real clicks, not just code
  reading.
- Checked the archive list's date display against the existing backups
  table's convention (`"2006-01-02 15:04"`, not raw RFC3339) — the first
  draft rendered a raw ISO timestamp; reformatted server-side for
  consistency with the established pattern on the same page.
- RTL/fa driven in the same real browser: table columns and button/input
  order flip correctly, no overlap, the `RESTORE`/`RESET` confirm words
  correctly stay untranslated inside the Persian sentence.
- Full gate run twice — once by Dev, once after the review-fix commit:
  `go build ./...`, `go vet ./...`, `go test ./... -race` (36 packages,
  zero `FAIL`), `guard-data-access.sh`, `guard-i18n.sh`,
  `guard-kiosk-engine.sh`, `guard-help-topics.sh`,
  `guard-plugin-menu-read.sh` — all green both times.
- Migration number 040 reconfirmed free against `origin/main` immediately
  before this review record was written (no collision with any other
  in-flight lane).

## Explicitly deferred (tracked, not silently dropped)

- ut-docs#635 — retention-window decision (product owner) gating a future
  delete-archive/purge action. No such action ships in this card.
- ut-docs#636 — reset-archive batches' inclusion in backup/cloud-
  enrolment snapshots (a separate subsystem's join-path concern).
- ut-docs#640 — teach `CleanupObsoleteItems`/"Remove sample data"/
  `EraseCustomer` to consult the archive tables directly (root-cause fix;
  today's `ErrArchiveReferencesRemoved` refusal is the interim safety net
  and stays regardless of whether #640 ships).
- Cosmetic nitpicks noted above (UTC-vs-local date basis, locale key
  naming drift, hand-maintained column lists) — no functional risk,
  left as-is to keep this diff scoped to the compliance fix.

## Safe-to-merge verdict

**Yes**, after the review-round fixes above. Full gate green, independent
review's one blocker resolved and revert-verified, all real-but-non-
blocking findings fixed in the same pass.
