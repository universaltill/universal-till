# Code review — manual "Free table / Guests left" action (ut-docs#1393)

**Date:** 2026-09-07
**Branch:** `feat/1393-free-table-action`
**Reviewer:** independent review pass (Opus, different model from the Sonnet
build), worktree-isolated, did not write the implementation under review.
**Verdict:** **safe to merge** after the one blocking fix (and three smaller
ones) below were applied in this branch and pinned by tests.

---

## What shipped

Split from ut-docs#1390 (live table occupancy claims). The automatic
claim/release lifecycle only ever clears a table's live `table_claims` row
when the OWNING till's basket acts again (clear/hold/tender/reset), or —
for `ClearLocalTableClaims`/`ClaimTableForTill`'s TTL reconciliation — when
that same till acts again later. A till that crashes between claiming a
table and completing/clearing the sale, or a replica claim nobody ever
revisits, left a table stuck reading "occupied" forever with **no
in-product recovery** (`tables_repo.go`'s own doc comment on
`ClearLocalTableClaims` said so explicitly, and both the ut-docs#1390 and
ut-docs#1703 review records name this card as the planned fix for that
residual gap).

- **`internal/data/tables_repo.go`** — new `ForceReleaseTableClaim`: an
  unconditional `DELETE FROM table_claims WHERE table_id = ?` inside a
  transaction with a `held_sales` existence check, reporting `released`
  (was there a claim to drop) and `stillHeld` (does a genuine held order
  still occupy the table afterwards). `held_sales` is **never** written or
  deleted here — the safety property the whole design turns on.
- **`internal/pages/tables_page.go`** — new `POST /api/tables/{id}/release`,
  manager- and primary-gated exactly like the existing `/active` handler,
  audited (`table_release`, payload records what actually happened).
- **`web/ui/pages/tables.html`** — a "Free table" button per occupied row,
  confirm-gated.
- **`web/locales/{en,fa,ar,tr}.json`** — 5 new `tables.*` keys. The external
  `ut-plugin-language-{de,es}` packs got all 5 first, in two rounds mirroring
  the two rounds of key additions below (de#177/es#176, then de#178/es#177 —
  all four merged), per the standing pack-lands-before-core rule.
- **`web/help/{en,fa,ar,tr}/tables.md`** — new "Good to know" bullet;
  `make docs-shots` re-run (surface `14f8732d491b…`).

## Independent review

Full pass: build/vet/lint/tests/all relevant CI guards run for real, not
taken on faith — see the Opus reviewer's own summary table (gofmt, `go
build`, `go vet`, `go test ./internal/data/... ./internal/pages/...` and
whole-repo `go test ./...`, `golangci-lint`, `guard-data-access.sh`,
`guard-i18n.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh`,
`guard-compliance-claims.sh`, `guard-page-http-error.sh`,
`guard-kiosk-engine.sh` — all green). JS-escaping of a translated string
inside the button's `onsubmit="confirm('...')"` was checked empirically
(Go's `html/template` contextual escaper turns a literal `'` into
`'`), not assumed safe.

### 1. BLOCKING (fixed) — force-release reopens the cross-till double-claim
`ut-docs#1703` just closed

`ForceReleaseTableClaim` deletes a claim regardless of ownership, on
purpose — that is the whole point of a manual override. But the existing
`ReleaseTableClaim` (used by every till's own basket-lifecycle release,
via `releaseTableClaimWriteThrough`) deleted **by `table_id` only**, with
no `till_id` scoping. Before this feature that was safe *by construction*:
`table_claims.table_id` is a PRIMARY KEY, so at most one row exists per
table, and whoever was releasing could only ever be deleting their own
row. `ForceReleaseTableClaim` breaks that invariant on purpose, which
makes the following real, reachable sequence possible:

1. Primary's cashier picks table T5 → local claim (`till_id=''`),
   `Engine.TableID() == "T5"`.
2. A manager taps **Free table** on T5 (the claim looked stuck) → the row
   is deleted. The primary's own live basket is never told — it still
   thinks it holds T5 for the rest of the sale (the `/api/pos/table`
   handler's "already my own table" short-circuit means it's never
   re-claimed).
3. A different till legitimately claims T5 (now genuinely free) — its row.
4. The primary's cashier eventually tenders/resets → the ordinary release
   path fires → `ReleaseTableClaim(T5)`, unscoped → **deletes the other
   till's live claim**, not the primary's own (already-gone) row.
5. The primary now reads T5 free while the other till has an open order on
   it — the exact cross-till double-booking ut-docs#1703 exists to
   prevent, reopened by this feature.

**Fix:** scope `ReleaseTableClaim`'s `DELETE` to `till_id = ''` — its one
caller always means "release MY OWN local claim," and `till_id=''` is
that local row's own convention. Verified there is exactly one call site
(`tables_claim_proxy.go:164`) and it is that exact case.
`TestReleaseTableClaim_NeverTouchesAnotherTillsClaim` (new) seeds a
foreign-till claim, confirmed it fails against the pre-fix unscoped
`DELETE` (a different till's claim was silently deleted), then confirmed
it passes after scoping the statement.

### 2. NON-BLOCKING (fixed) — misleading "claim cleared" message

`tables.error.held_order_attached` said "Claim cleared, but…" even when
`released=false` — a table occupied *only* by a genuine held order (no
stuck claim at all, arguably the more common press of this button) had
nothing to clear. Split into two keys: `tables.error.held_order_attached`
(a claim WAS cleared, a held order remains) and the new
`tables.error.held_order_only` (nothing was cleared, a held order is the
whole reason it's occupied). New test case in `TestTablesPage_Release`
covers the no-claim/held-order-only path.

### 3. NON-BLOCKING (fixed) — a mid-check DB failure could report "failed"
for a change that actually committed

`ForceReleaseTableClaim` originally ran the `DELETE` and the `held_sales`
count as two independent statements; a failure on the second would report
`tables.error.release` ("could not free the table") for a claim that HAD
already been dropped, and the handler's `audit(...)` call (gated on
`err == nil`) would never record that real state change. Wrapped both in
one transaction: either both succeed and the reported state is accurate,
or nothing commits and the reported error is accurate.

### 4. NON-BLOCKING (fixed) — a DB error on the existence check read as
"table not found"

The handler's `GetTable` check conflated a genuine DB error with a
missing row (`err != nil || !found`), unlike the sibling `/active` and
`/api/tables/{id}` handlers. Split into two branches, distinct error keys.

### 5. NON-BLOCKING (fixed) — help doc omitted the primary-till restriction

The new "Good to know" bullet didn't say the action is main-till-only,
unlike the bullet immediately above it for every other table mutation on
the page (same `requirePrimary` gate). One clause added in all four
locales.

### Deferred as follow-ups (not blocking this merge)

- **The manager can't tell a stuck claim from a live one** — the button
  renders on every occupied table with no indication of which till (if
  any) currently holds it, which is what makes finding 1's sequence
  realistically reachable rather than theoretical. Worth surfacing
  `table_claims.till_id` on the row, or at least widening the confirm
  text. Filed as a new Backlog card.
- **The row-level table list isn't live** — only the SVG floor-plan
  partial polls; a table's row (and hence the button's presence) only
  updates on full page reload. Harmless (the server side is idempotent),
  but noted.
- **Pre-existing test gap, not introduced here**: `tables_page_test.go`'s
  OTHER audit-adjacent tests (`table_create`/`update`/`move`/
  `activate`/`deactivate`) use `auth.User{ID: "m1"}` with no matching
  `users` row, so `InsertAudit`'s `actor_id` FK silently fails via the
  `audit` closure's swallowed error — those tests have never actually
  verified an audit row lands. This PR's own new tests are the first in
  the file to use a real seeded user (`data.NewAuthRepo(...).CreateUser`).
  Filed as a new Backlog card to give the other five call sites the same
  fixture.

## Verified beyond automated tests

Real driven run against the actual compiled binary (`go run .`,
`UT_AUTH=off`), not just `httptest`: seeded a table with a foreign-till
orphaned claim directly in SQLite, confirmed the button renders (English
+ Persian/RTL, screenshotted and read — button sits correctly after
Save/Deactivate in both LTR and RTL, no overlap/clipping), drove a real
headless-Chromium click through the confirm dialog (Playwright), and
confirmed after the click: `table_claims` empty, `audit_log` carries the
`table_release` row with the correct payload, and the floor plan/list
render the table free. Server killed after.

## Safe-to-merge

Yes. Core safety property (never destroy a real held order) holds and is
tested; the one real data-integrity risk found (finding 1) is fixed and
pinned by a regression test that fails against the pre-fix code.
