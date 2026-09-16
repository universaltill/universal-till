# Code review — catalog price edit never reached `price_history`, so the till kept charging the old price (ut-docs#2314)

- **Date:** 2026-09-16
- **Ticket:** ut-docs#2314 (`complexity:medium`)
- **Branch:** `fix/2314-catalog-price-history-edit`
- **Reviewer:** independent pass, different model from the implementation
  pass, per this card's `complexity:medium` routing. Reviewer had not seen
  the Dev pass's reasoning.
- **Verdict: SAFE TO MERGE**, after two reviewer fixes below (one missed
  CI-blocking gate, one now-false CI guard premise). No blocker-class
  finding — no money, data-loss or security defect survived verification.

## The bug

`POST /api/catalog/item/update`, `POST /api/catalog/variant` and the cloud's
`SetItemPrice` remote-price directive (ADR-0018) wrote `items.base_price` /
`item_variants.price` and nothing else. But the price the till actually
charges is resolved through `price_history` — `POSRepo.ResolveCurrentPrice`
/ `lookupPriceHistory` and `CatalogRepo.ItemCurrentPrices` all prefer an
active (started, not ended) `price_history` row over the configured price.

So on any item that has ever carried an active `price_history` row, editing
the price in the catalog form said "Saved" and changed nothing at checkout,
silently and permanently. That is not an exotic state: **all 50 items in the
shipped demo catalogue** (`internal/data/seeddata/demo_catalogue.sql`,
`ph001`–`ph050`, all open-ended) have one, as does anything seeded by
`scripts/e2e_seed` or `scripts/smoke_quickstart`, and so would any shop that
had ever been given a scheduled/promotional price.

## What shipped (the diff under review)

**Write path — `internal/data/catalog_repo.go`**

- `resolveCurrentPriceExec` — an `execer`-based twin of
  `POSRepo.ResolveCurrentPrice`, so the **pre-write** resolved price can be
  read inside the caller's own transaction rather than in a second,
  separately-timed one. Deliberately drops `ResolveCurrentPrice`'s
  `is_active` filter on the configured-price fallback (this is "what was the
  price before this edit", not a sellability check, and the edit may itself
  be the reactivation).
- `appendPriceHistoryItemExec` / `appendPriceHistoryVariantExec` —
  `execer`-based twins of `POSRepo.AppendPriceHistoryItem/Variant`
  (close the open row, insert a new one starting now), without opening their
  own transaction.
- `recordPriceChangeExec` — the shared "only touch `price_history` when the
  price actually changed" gate.
- `UpdateItemReturningWasActive`, `UpdateVariant` and `SetItemPrice` now each
  read the pre-write resolved price and append a `price_history` row when it
  differs, all inside one transaction with the write itself. `UpdateVariant`
  and `SetItemPrice` gained a transaction they did not previously have.

**Scheduled-row protection — `internal/data/pos_repo.go`**

- `AppendPriceHistoryItem/Variant`'s closing `UPDATE` gained
  `AND datetime(starts_at) <= datetime(?)`, so a still-open **future-dated**
  row (a deliberately scheduled price change) is no longer closed by an edit
  of today's price. Same clause in the new exec twins.

**Display — `internal/pages/catalog/{handlers.go,row_oob.go}`**

- Both `catalogRowVM` construction sites (first paint via `buildCatalogRows`,
  and the post-save OOB row swap via `writeCatalogRowOOB`) now override
  `Item.BasePrice` with `ItemCurrentPrices`' resolved value. That feeds both
  the visible tile price and `catalog_row.html`'s `data-price`, which
  `catalog.html`'s `openCatalogRow` reads into `#item-price` — so the edit
  form's Price field shows the resolved price.
- `CatalogRepo.VariantsForItem` resolves `PriceMinor` the same way, which is
  what `catalog_variants.html`'s per-variant price input renders.

**Docs / classification**

- `internal/data/sync_admin_repo.go`'s `nonAdminTables` entry for
  `price_history` updated from "currently latent (nothing in production
  writes this table yet)" to state that it now has real production writers.

**Tests:** `internal/data/catalog_repo_price_history_edit_2314_test.go` (5
cases), `internal/data/pos_repo_price_history_future_2314_test.go` (2),
`TestSetItemPrice_UpdatesPriceHistory` in `catalog_repo_crud_test.go`, an
`internal/pos/pricing_history_test.go` fixture-format fix, and
`e2e/tests/catalog-price-history-edit-2314.spec.ts`.

## Reviewer's independent TDD re-verification

Done for real — targeted source edits plus `go test`, not by reading the
code. Three separate reverts, five distinct tests confirmed to catch them:

1. **Removed the `recordPriceChangeExec` call from
   `UpdateItemReturningWasActive`** (the card's core fix).
   `TestUpdateItemReturningWasActive_UpdatesPriceHistory` failed with
   `expected the edited price 400 to be what the till charges now, got 250`
   — i.e. it reproduced the reported bug exactly (the stale
   `price_history` price still winning after a "successful" save), not some
   adjacent assertion. Restored, green.
2. **Removed `AND datetime(starts_at) <= datetime(?)`** from the closing
   `UPDATE` in *both* `catalog_repo.go`'s exec twin and `pos_repo.go`'s
   `AppendPriceHistoryItem`. Both
   `TestUpdateItemReturningWasActive_LeavesFutureScheduledRowUntouched` and
   `TestPOSRepo_AppendPriceHistoryItem_LeavesFutureScheduledRowUntouched`
   failed with the future-dated row's `ends_at` set to the edit timestamp.
   Restored, green.
3. **Removed the `oldPrice == newPrice` early return** from
   `recordPriceChangeExec`. All three "no spurious row" tests failed
   (`before=1 after=2` for item and variant,
   `before=4 after=5` for the cloud directive). Restored, green.

After each restore the files were byte-compared back to the pre-experiment
state (`git diff --stat` line counts identical) before continuing.

## What the independent review found

### Reviewer fix 1 — `guard-docs-shots.sh` was failing (CI-blocking, missed)

`guard-docs-shots.sh` is a **blocking** step in `ci.yml`'s `build` job and
it failed on the diff as handed over: the diff touches
`internal/pages/**.go`, which is part of the hashed surface. The Dev pass did
not run it.

Before taking the escape hatch, pixel-neutrality was **proven**, not
assumed:

- `docs-shots` runs against the demo catalogue
  (`e2e/playwright.docs.config.ts` → `run-till.sh`; the spec's own header
  says "demo catalog seeded deterministically"), and it does screenshot
  catalog surfaces.
- Parsed `demo_catalogue.sql` and compared every `price_history` row against
  its item's `base_price`: **50 items, 50 rows, zero mismatches**. There are
  no variant-level `price_history` rows in the seed at all.
- Therefore the resolved price the new code renders is byte-identical to the
  raw `base_price` the old code rendered, on every screenshotted page. Same
  holds for `scripts/e2e_seed` (item `base_price` 1000, `price_history` 1000).
- The rest of the diff to those two files is comments and an id slice; no
  markup or layout changed.

Ran `scripts/ci/update-docs-shots-surface-hash.sh` (the guard's own
documented remedy for a manually-confirmed no-pixel change — preferred over
a full `make docs-shots`, which rewrites all 104 PNGs and conflicts with
every other open PR by construction). Confirmed the diff is exactly one
field, `surface_sha256`. The commit carries the required
`Docs-Shots-Unchanged: true` trailer.

### Reviewer fix 2 — `guard-price-history-sync.sh`'s own premise is now false

`scripts/ci/guard-price-history-sync.sh` exists (ut-docs#1671) precisely for
this situation: fail the build if a real production writer of
`price_history` appears while the table is still classified non-admin. Its
header asserts, as its stated reason for `price_history` staying out of
`adminTables`, that "nothing in production writes it".

That is now false — but the guard still passes, because it greps only for
`.AppendPriceHistoryItem(` / `.AppendPriceHistoryVariant(` and the new
writers are the `internal/data` exec twins. This is the same direct-caller
blind spot the guard's own "Known limitation" note already describes,
reached via a sibling implementation rather than a one-hop wrapper.

Leaving the grep alone is the right call — widening it would just red the
build until ut-docs#2348 lands, and the guard's *own second acceptable
answer* ("confirm the new caller stays primary-gated so a satellite never
writes it at all") genuinely holds for the catalog form: both
`/api/catalog/item/update` and `/api/catalog/variant` are behind
`requirePrimary` (verified in `internal/pages/catalog/handlers.go`). But a
CI guard whose header documents a premise that no longer holds is exactly
the drift this repo does not tolerate, so the reviewer updated the header
and the success message to say plainly what the ✓ now does and does not
mean, naming ut-docs#2314 and ut-docs#2348. `guard-price-history-sync_test.sh`
re-run: all 7 cases pass.

### Race safety — checked, not taken on the comment's word

The diff's comments claim `BEGIN IMMEDIATE` race safety "for free" via the
DSN. Verified independently: `internal/db/db.go:97` really does build the
DSN with `&_txlock=immediate`, so every `BeginTx` takes the write lock at
`BEGIN`, and the read-then-compare-then-write sequence cannot interleave
with a concurrent price edit. The reasoning is sound, not just asserted.

Also checked, since two of these methods gained a transaction they did not
have before: no caller of `UpdateItemReturningWasActive`, `UpdateVariant` or
`SetItemPrice` already holds an open transaction on the same `*sql.DB`
(all three are called from handler level, via `internal/pos/catalog_ops.go`
or `cloudsync_wire.go`), so the new `BEGIN IMMEDIATE` cannot self-deadlock
against an outer transaction on another connection.

One genuine improvement worth recording: on the error path,
`SetItemPrice`/`UpdateItemReturningWasActive` now roll the whole thing back,
so a failed `price_history` append can no longer leave a committed
`base_price` write behind. Pre-change, that half-state *was* the bug.

### Timestamp-format correctness — the one subtle trap, and it is handled

Adding `datetime(starts_at)` to the closing `UPDATE`'s `WHERE` makes that
clause format-sensitive in a way the old `ends_at IS NULL` clause was not: a
value `datetime()` cannot parse reads as `NULL`, the row silently fails to
match, and it is never closed — leaving two open rows. This is what the
`internal/pos/pricing_history_test.go` fixture change in the diff is about
(the old fixture bound a raw `time.Time`, which `modernc.org/sqlite`
serializes as `2026-01-02 15:04:05.999999999 +0000 UTC` — unparseable).

Reviewer enumerated **every** writer of `price_history.starts_at` in the
repo and confirmed all produce a `datetime()`-parseable value:

| writer | format | parses |
|---|---|---|
| `001_init.sql` column default | `datetime('now')` → `YYYY-MM-DD HH:MM:SS` | yes |
| `demo_catalogue.sql` | `'2025-01-01'` (date only) | yes |
| `scripts/e2e_seed`, `scripts/smoke_quickstart` | `2006-01-02 15:04:05` | yes |
| `AppendPriceHistory*` and the new exec twins | `time.RFC3339` | yes |

Also checked the read/write clock pairing: writes use Go's `time.Now()`
formatted as RFC3339 (which *truncates* sub-seconds, and carries the local
offset), reads compare against SQLite's UTC `CURRENT_TIMESTAMP`. `datetime()`
normalizes the offset, and truncation only ever moves `starts_at` earlier, so
a just-inserted row can never be invisible to the very next read. The close
writes `ends_at = ts` and the insert writes `starts_at = ts` with the *same*
`ts`, and the read predicate is `ends_at > now` (strict) / `starts_at <= now`
(inclusive) — so there is no one-second window where neither row resolves,
and no window where both do. Confirmed by the tests, which assert the new
price resolves in the same second as the write.

Double-save within one second was traced by hand: the second save either
no-ops (same price) or closes the zero-length intermediate row and opens a
third — no overlapping open rows either way.

### "Only when the price changed" — is the comparison ever wrong?

This was the specific correctness question the card flagged, and the answer
is no, for a structural reason: `resolveCurrentPriceExec` is a
statement-for-statement twin of `lookupPriceHistory` + the configured-price
fallback (same `datetime()` window, same `ORDER BY datetime(starts_at) DESC
LIMIT 1`), read **inside the same transaction** as the write. So the value
compared is exactly the value the read path would have returned at that
instant, not a differently-computed approximation. The tests assert the
round trip through `ResolveCurrentPrice` *and* `ItemCurrentPrices`
independently.

The `ok=false` (id not found) branch was checked for a silent-skip hazard:
for items, the fallback is `SELECT base_price FROM items WHERE id = ?` with
no `is_active` filter, so `ok` is false only when the row genuinely does not
exist — in which case the write itself matches zero rows too. Same for
variants. There is no reachable state where a real row's price change is
silently skipped.

### Display — can any surface now show a wrong or misleading price?

Enumerated both `catalogRowVM` construction sites (`row_oob.go:89` and
`:188`) and confirmed both are covered; there is no third. `data-price` is
written only by `catalog_row.html` and read only by `openCatalogRow`
(`row.dataset.price`), so the form field and the tile can no longer disagree
with each other or with the basket. `ItemVariantsFor` (the row's variant
*list*) was correctly left alone — `catalog_row.html` renders only variant
names and barcodes from it, never a price.

Note for the record: ut-docs#2228's review record states `ItemVariantsFor`
was deliberately left on the configured price because "the catalog admin
grid needs the configured price to edit, never a transient promotion." This
card deliberately reverses that judgment for the *displayed* price, on the
card author's explicit instruction. That is a product decision superseding
an earlier one, not an oversight — recorded here so the trail is intact.

## Non-blockers — noted, not fixed here

- **N1 — an unrelated edit rewrites `base_price` to the resolved price.**
  Because the form now round-trips the resolved price, renaming an item that
  has an active `price_history` row also writes that row's price into
  `items.base_price`. Considered fixing it (restore the configured price
  when `oldPrice == newPrice`) and **deliberately did not**: since
  `price_history` is *not* synced to satellites but `items` *is*, letting
  `base_price` track the resolved price actually keeps a fleet
  **consistent**, whereas preserving the configured value would make the
  primary and its satellites charge different prices. The only way this
  could bite is a bounded promo (`ends_at` in the future) expiring back onto
  an overwritten `base_price` — and **nothing in the product can create
  such a row**: the only writer of `ends_at` is the close-out, which always
  sets it to *now*, and all 50 demo rows plus both seed scripts are
  open-ended. `web/help/en/promotions.md` is about promo *codes* (the
  `promotions` table), not scheduled item prices. Genuinely latent; the
  right place to settle it is the scheduled-price feature that first
  creates a bounded row.
- **N2 — `ResolveCurrentPrice`'s `is_active` filter is now bypassable for
  slightly more items.** `lookupPriceHistory` has no `is_active` filter, so
  an inactive item *with* a `price_history` row resolves a price instead of
  erroring "item not found or inactive". This diff creates rows for items
  that previously had none, marginally widening that. Pre-existing (every
  demo item already had a row), and not load-bearing: the only callers are
  `resolvePrice` (has a fallback, and its own query already filters
  `is_active = 1` upstream) and `ai_api.go` (not a sellability gate).
  Informational.
- **N3 — per-write `price_history` lookups have no usable index.**
  `VariantsForItem`'s new correlated subquery and `resolveCurrentPriceExec`
  both do a `variant_id` lookup that `idx_price_history_item (item_id,
  variant_id)` cannot serve as a leading column. Already filed as
  **ut-docs#2259** (N1/N3 of the ut-docs#2228 review); this diff multiplies
  the existing cost rather than introducing a new class of it, and these are
  admin-screen and write paths, not the checkout hot path.
- **N4 — `POSRepo.AppendPriceHistoryItem/Variant` still have no production
  caller.** They remain reachable only from `internal/pos/pricing.go` (whole-
  program-unreachable per `scripts/ci/deadcode-baseline.txt`) and tests, so
  `pos_repo_price_history_future_2314_test.go` is, strictly, a regression
  test over currently-dead code. Worth keeping: it is the direct unit proof
  of the scheduled-row rule the live exec twins implement, and those two
  methods are the obvious foundation for the eventual scheduled-price
  feature.
- **N5 — the e2e spec was not executed in this review.** No
  `node_modules`/Playwright install available in this sandbox. Reviewed
  structurally instead: every selector it uses was verified to exist
  (`.catalog-row[data-name]`/`data-id`/`data-price` in `catalog_row.html`,
  `#item-id`/`#item-price`/`#item-form-submit`/`#item-form-msg` in
  `catalog.html`, `POST /api/pos/reset` in `pos_api.go`), and both imported
  helpers (`watchConsole`, `closeItemForm`) are real exports of
  `e2e/tests/helpers.ts`. `guard-e2e-fixtures-import.sh` passes.

## Cross-till sync — confirmed accurate, and it belongs in the PR body

Checked `git diff origin/main -- internal/data/sync_admin_repo.go`
specifically. The one-line doc-comment change is **accurate on every claim**:

- `items` and `item_variants` *are* in `adminTables`
  (`sync_admin_repo.go:157`, `:166`) — they sync primary → satellite.
- `price_history` is *not*, and this diff does not change that.
- It really was latent before and is not now: this diff is the first
  production writer of the table.

The concrete fleet consequence — the thing a demo or test environment cannot
show you — is worth stating plainly, because the divergence is now
**asymmetric** where before it was symmetric. Before this card, a stale
`price_history` row made the primary *and* any satellite holding the same
row charge the same wrong price. After it, the primary is corrected and a
satellite holding its own `price_history` row (e.g. a till provisioned with
the demo catalogue and never `remove_demo`'d, then joined to a fleet) is
not — two tills in one shop quoting different prices for the same barcode,
with no error anywhere. The catalog form itself is `requirePrimary`-gated so
a satellite never *writes* the table; the cloud `SetItemPrice` directive
(`cloudsync_wire.go`) is **not** primary-gated, which is the sharper edge of
the same question.

Solving that is explicitly **not** this card — it is ut-docs#2348, filed
before this review. But it is a real behavioural consequence of merging
this, so **the PR description must state it**, not just the code comment: a
reviewer or operator reading the PR should not have to open
`sync_admin_repo.go` to learn that this change makes a previously
theoretical fleet divergence real. Reviewer confirms this belongs in the PR
body and has written it there.

## Verified beyond automated tests

- `gofmt -l .` silent; `go build ./...`, `go vet ./...` clean.
- Full suite `go test ./...` green (also re-run `-count=1` on
  `internal/data`, `internal/pos`, `internal/pages/...`, `internal/ui` to
  defeat the test cache).
- `golangci-lint run ./...` — **0 issues**.
- Every CI-blocking guard in `ci.yml`'s `build` job run locally and green:
  `guard-data-access`, `guard-price-history-sync` (+ its own self-test),
  `guard-migration-version-collision`, `guard-kiosk-engine`,
  `guard-plugin-menu-read`, `guard-page-http-error`,
  `guard-plugin-settings-bump`, `guard-i18n`, `guard-compliance-claims`,
  `guard-docs-shots` (after reviewer fix 1), `guard-help-topics`,
  `guard-help-drift`, `guard-emoji-font`, `guard-htmx-loaded`,
  `guard-autofill-suppression`, `guard-osk-loaded`,
  `guard-e2e-fixtures-import`, `check-brand-assets`,
  `guard-makefile-version`.
  `shellcheck` is not installed in this sandbox, so the shellcheck step and
  `guard-shellcheck-version.sh` could not be run; the one edited shell file
  was checked with `bash -n` and its edit is comment-and-echo only.
- Repository pattern: all new SQL lives in `internal/data`;
  `guard-data-access.sh` green. No money type regression — `price_history`
  and `base_price` are raw `int64` at the DB boundary throughout, which is
  what `internal/money`'s own convention prescribes; no `money.Money` value
  is constructed or compared in this diff.
- No i18n impact: no new user-facing string, no `web/locales/` change, so no
  `ut-plugin-language-{de,es}` follow-up is owed.
- No user-manual change owed: this restores the behaviour `web/help/en/
  catalog.md` already describes ("names, prices … edit an existing item")
  rather than adding, removing or altering anything an operator does. No new
  route, so `guard-help-topics.sh`'s coverage check is unaffected.

## Explicitly deferred (not this card)

- `price_history`'s cross-till sync classification, and whether the cloud
  `SetItemPrice` directive should be primary-gated — **ut-docs#2348**.
- The `price_history(variant_id, starts_at)` index and the same-`starts_at`
  tie-break — **ut-docs#2259**.
- Restoring the configured `base_price` on a price-unchanged edit (N1 above)
  — belongs with the scheduled-price feature that first creates a bounded
  `ends_at` row.
