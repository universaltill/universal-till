# Code review — table-claim till visibility before force-release (ut-docs#1714)

**Date:** 2026-09-08
**Branch:** `fix/1714-table-claim-till-visibility`
**Reviewer:** independent review pass (Opus, different model from the Sonnet
build), worktree-isolated, did not write the implementation under review.
**Verdict:** **safe to merge** after 4 findings fixed below (all applied and
verified in this branch).

---

## What shipped

Follow-up from the ut-docs#1393 independent review
(`docs/code-reviews/2026-09-07-manual-free-table-1393.md`), which deferred
exactly this as a follow-up: *"The manager can't tell a stuck claim from a
live one — the button renders on every occupied table with no indication of
which till (if any) currently holds it."*

- **`internal/data/tables_repo.go`** — `ListTablesWithState` gains
  `HasLiveClaim`/`ClaimTillID`/`ClaimTillName`/`ClaimTillOnline`, via a
  second `LEFT JOIN` against `table_claims`/`tills` kept apart from the
  existing `held_sales`+`table_claims` `UNION` (which deliberately loses
  which source contributed — this needs the claim specifically). Takes a
  `recentCutoff time.Time` parameter, mirroring `ClaimTableForTill`/
  `ForceReleaseTableClaim`'s existing convention, rather than deriving a
  second copy of the 2-minute online bound inside `internal/data` (which
  can't import `internal/pages`' `tillClaimTTL` constant).
- **`web/ui/pages/tables.html`** — the occupied row's "Live status" cell now
  shows "This till" / "`<till name>` — Online" / "`<till name>` — Offline";
  the "Free table" confirm dialog widens (names the till) only when the
  claim belongs to a *different* till.
- **`web/locales/{en,fa,ar,tr}.json`** — 2 new `tables.*` keys.
- **`web/help/{en,fa,ar,tr}/tables.md`** — one new "Good to know" bullet
  each; `make docs-shots` re-run (surface `2a267f83db54…`).
- Deliberately does **not** extend `GET /api/sync/tables`'s wire format or
  the replica-side `tablesWithStateForDisplay` merge with the new fields —
  see the finding-by-finding notes below for why that holds up.

## Independent review

Full pass: `gofmt -l .`, `go build ./...`, `go vet ./...`, whole-repo
`go test ./...`, `golangci-lint run ./...`, `guard-data-access.sh`,
`guard-i18n.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh`,
`guard-compliance-claims.sh`, `guard-kiosk-engine.sh`,
`guard-page-http-error.sh` — all run for real, all green on the final
state.

### 1. BLOCKING (fixed) — the load-bearing SQL comment overclaimed a defect that isn't reproducible against this query's actual shape

The Dev pass's own comment on `ListTablesWithState` asserted that an earlier,
unwrapped-bare-column version of the query "actually cross-attributed one
table's claim onto a DIFFERENT table with no claim at all," observed live.
The independent review reverted the `MAX()` wrapping and re-ran the new
regression test, then built a deliberately hostile 6-table fixture (a claim
plus two held orders whose `created_at` straddles it, so the `MIN(o.since)`
row isn't the claim row; interleaved claimed/unclaimed/free tables) — the
bare-column version returned every value correctly in every case.

The reason: `table_claims.table_id` and `tills.id` are both `PRIMARY KEY`
(`001_init.sql:1112`, `001_init.sql:671`), and `tc` joins straight onto
`t.id`, so every row inside one `GROUP BY t.id` group carries *identical*
`tc`/`ti` values — there is no arbitrary row for SQLite to pick wrongly, so
whatever was actually observed live must have come from a differently-shaped
earlier draft (e.g. joining `tc` through the `UNION` subquery instead of
`t.id` directly), not the query shape that shipped.

**Fix:** rewrote the comment to state the real invariant (two primary keys)
and to record that `MAX()` is being kept as *correctness by construction* —
so the guarantee survives the day this join is ever widened (a claim
history table, a second claim per table) — not as a fix for a reproduced
defect. `MAX()` itself was correct to add regardless (it's the standard-SQL-
correct form either way) and was left in place.

### 2. BLOCKING (fixed) — a claim whose owning till was later revoked rendered broken prose

Migration `008_table_claims_till_id.sql` deliberately gives `table_claims`
**no foreign key** to `tills` ("a revoked till's claim must stay readable...
rather than block `DeleteTill`"). So a claim can legitimately carry a
`till_id` with no matching `tills` row, and `COALESCE(MAX(ti.name), '')`
correctly evaluates to `""` for it — which the row template then rendered as
a bare `" — Offline"`, and the confirm dialog as *"Its claim currently
belongs to&nbsp; — clearing it may desync..."* with the till name missing
entirely. This is exactly the case a manager is most likely to be
force-releasing (a genuinely gone till).

**Fix:** `or .ClaimTillName (T "tables.claim.unenrolled_till")` in both the
row and the confirm dialog, a new locale key in all four locales, a manual
clause in all four `tables.md`, and a new regression test
`TestTablesPage_RenderNamesClaimOfUnenrolledTill`.

### 3. NON-BLOCKING (fixed) — the 2-minute online bound was a fourth, hardcoded copy of a number owned elsewhere

The first version derived `now.Add(-2 * time.Minute)` inside
`internal/data`, while the two sibling methods in the *same file*
(`ClaimTableForTill`, `ForceReleaseTableClaim`) already take a
`recentCutoff`/`cutoff time.Time` parameter from `internal/pages`' single
`tillClaimTTL` constant — precisely because `internal/data` can't import it.
The new method's own doc comment said "don't invent a different bound here"
while doing exactly that.

**Fix:** `ListTablesWithState(ctx, recentCutoff time.Time)` /
`tablesWithStateForDisplay(..., recentCutoff time.Time)`; all four
production call sites (`tables_page.go`, `sync_tables.go`,
`table_picker_api.go`, `hold_api.go`) now pass
`time.Now().Add(-tillClaimTTL)`.

### 4. NON-BLOCKING (fixed) — the new page-level test could not actually detect the bug class it exists for

`TestTablesPage_RenderShowsClaimTillIdentity`'s original assertions were all
page-global `strings.Contains` calls — which cannot distinguish "T3's row
shows Terrace till" from "*some* row on the page shows Terrace till
somewhere." That is precisely the failure mode a claim-attribution bug
produces. One assertion (`!strings.Contains(body, "tables.confirm...") &&
!strings.Contains(body, "Its claim...")`) also only failed if *both*
disjuncts were absent, so it would have passed even if the locale key
failed to resolve and rendered raw.

**Fix:** rewrote the test to scope every assertion to each table's own
`<tr>`, added explicit negative assertions (T3's row must NOT contain
Kitchen/Offline, T4's must NOT contain Terrace), and added an
unresolved-locale-key check across the whole body.

### Answered directly: does the regression test actually catch the SQL bug?

**No — because there wasn't one to catch, given this query's actual shape**
(see finding 1). `MAX()` is correctness-by-construction insurance against a
future change to the join, not a regression fix. The per-row-scoped test
from finding 4 is real protection against claim cross-attribution generally
(it would fail if a future change actually introduced one), which is the
property that matters here regardless of which specific defect prompted it.

### Confirmed, not changed

- **Replica scope decision (`tables_sync_proxy.go`)** — traced
  `TableWithState` through every consumer: `syncTableRow` (the only JSON
  wire type) carries no claim fields, `registerSyncTables` populates it
  field-by-field, and `TableWithState` itself is never marshalled anywhere
  else. No foreign till identity reaches a replica over the wire. The
  reasoning (`tills.last_seen_at` is redacted in the admin-sync snapshot per
  `sync_admin_repo.go`'s `redactCols`, so a replica has nothing fresh to
  judge "online" from) holds. One pre-existing, informational-only gap
  noted but correctly left out of scope: a *freshly enrolled* replica's
  initial full-DB snapshot does carry the primary's foreign `table_claims`
  rows with a snapshot-baked `last_seen_at` until the next admin-sync
  redaction pass — not a confidentiality issue (`Free table` is
  `requirePrimary`-gated, and the till roster is synced anyway) and
  pre-existing (ut-docs#1392/#1704 territory), just now given a name/badge.
  Worth its own future card if it turns out to matter in practice, not a
  blocker here.
- **`TestClaimTableWriteThrough_LocalBranchStillClaimsWhenReconcileFails`
  rewrite** — confirmed sound, and slightly *stronger* than the original:
  the old `tableOccupied` helper derived occupancy from a UNION that could
  in principle be satisfied by a held row alone; the direct
  `SELECT COUNT(*) FROM table_claims WHERE table_id = ?` asserts exactly
  what the test's name and comment claim (the local-claim fallback actually
  wrote the row). `DROP TABLE tills` in that test genuinely does now break
  `ListTablesWithState` (it needs `tills` for the new join), so the rewrite
  was necessary, not just tidying.
- **JS-escaping of the interpolated till name** — verified empirically by
  rendering the real page with a till named `` O'Brien\'s "till" </script>
  <img src=x onerror=alert(1)> `` and confirming Go's `html/template`
  contextual autoescaper produces a syntactically-safe JS string literal
  (quotes, backslash, `<`/`/` all escaped) both before and after the
  `unenrolled_till` fallback was added — the escaper acts on the pipeline's
  final value, so the added `or` doesn't change this.
- **Help-doc translations** — the fa/ar/tr bullets are genuine translations
  of the English meaning (till identity, the 2-minute bound, the "offline is
  the stronger signal" judgement), not filler. One pre-existing terminology
  inconsistency noted in `ar/tables.md` (mixes `جهاز`/device with the UI's
  own `الصندوق`) — not introduced by this change, the manual is correctly
  quoting the shipped `ar.json` string.
- **`web/help/img/manifest.json` + regenerated screenshots** — the four
  `tables` topic hashes match `sha256sum` of the markdown files exactly;
  `guard-docs-shots.sh` passed on the real re-run's surface hash; `tables.png`
  itself is unchanged because the `make docs-shots` demo fixture has no
  occupied-with-claim table to show the new row text on — a real, accepted
  screenshot gap (the row text is proven by the Go rendering tests + a
  manual browser check during Tester, not by the shipped screenshot).

## Verified beyond automated tests (Tester phase, same session)

Live server + real browser (Playwright/Chromium) against all four row
scenarios (this-till, foreign-till-online, foreign-till-stale,
held-order-only-no-claim): row text and confirm-dialog text captured and
confirmed correct in English and in `fa` (RTL) — no overlap, clipping, or
`left`/`right`-hardcoded misalignment; existing `.muted`/`<br />` markup
reused, no new CSS. `TestTablesPage_RenderShowsClaimTillIdentity` was
mutation-tested (confirmed it fails if the row-rendering block is deleted).

## Deferred / accepted, not blocking

- The pre-existing freshly-enrolled-replica phantom-foreign-claim
  visibility gap noted above — informational only, not introduced here.
- `tables.claim.this_till` duplicates the existing `tills.this_till` key in
  fa/ar and differs only by capitalisation in en/tr — a shared key would
  render lowercase standalone in `tills.html`'s parenthetical use, so the
  split is defensible; not worth churning both call sites for this card.
