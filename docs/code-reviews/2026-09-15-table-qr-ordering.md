# Code review — table-QR guest ordering (ut-docs#815)

- **Date:** 2026-09-15
- **Ticket:** ut-docs#815 (`complexity:medium`)
- **Branch:** `feat/815-table-qr-ordering`
- **Reviewer:** independent pass, different model (Opus) from the Sonnet
  implementation, per this card's `complexity:medium` routing.
- **Verdict: SAFE TO MERGE**, after the fix pass below. Two blocker-class
  findings (money/order-integrity class) from the review round were fixed
  and re-verified in this same round; a third was mitigated with a
  fail-safe rather than fully solved (see "Explicitly deferred").

## What shipped

Lets a guest scan a QR code printed for their table (generated from the
tables/floor-plan admin page, `GET /api/tables/{id}/qr`) and place an
order from their own phone via the existing `/self-order` guest flow,
bound to that table. Checkout for a table-bound session always produces a
`kiosk_counter_orders` row (staff collects payment) regardless of the
till's own `kiosk.payment_mode` setting — a guest's own phone has no card
terminal attached, so the till's card/contactless payment mode never
applies to a table session; this was the "needs a product decision" the
card was filed with, resolved by research (SumUp/Square/Lightspeed guest
ordering all route the same way — see the BA/Architect comment on the
issue) rather than escalated.

Reuses existing infrastructure end to end: the auth-exempt `/self-order`
flow (already LAN-reachable from any phone on the shop wifi), the
"pay at counter" `kiosk_counter_orders` path (ut-docs#582), and the table
entity + `pos.Service.SetTable`/`TableID`/`TableLabel` fields ADR-0054/
ut-docs#820 already added — no new mechanism for any of those three
pieces. QR generation follows the existing `order_tracking.go`/
`sync_api.go` pattern (`skip2/go-qrcode` + `advertisableHost`).

- Migration `internal/db/migrations/029_kiosk_counter_orders_table.sql`
  adds `kiosk_counter_orders.table_id` (nullable FK, append-only,
  idempotent `ADD COLUMN` per `execMigrationStatements`'s existing
  convention).
- `internal/pages/self_order_page.go`: `GET /self-order?table=<id>`
  validates (exists + enabled) and binds the table via the existing
  `SetTable`.
- `internal/pages/self_order_shop.go`: `selfOrderForcesCounterCheckout`
  gates both the GET/POST checkout paths — counter-order path whenever
  `kiosk.payment_mode == counter` OR a table is bound.
- `internal/pages/tables_page.go` / `web/ui/partials/table_qr.html`:
  manager-gated `GET /api/tables/{id}/qr`, one shared dialog on
  `tables.html`.
- `internal/pages/kiosk_counter_orders_page.go` / `..._list.html`: the
  staff board shows the table label on a table-bound entry.
- i18n: `tables.qr.*` (3 keys) added to all 4 core locales; help docs
  (`web/help/{en,ar,de,fa,tr}/tables.md`) each gained one matching bullet,
  screenshots regenerated (`make docs-shots`, see below).

## What the independent review found

**First-round verdict: NOT SAFE TO MERGE**, on two blockers plus a CI-
blocking gap; a third architectural concern flagged as needing a product
decision rather than a review fix.

### BLOCKER 1 — the cart's Takeaway toggle silently unbound the table and completed a real card sale

`POST /api/self-order/order-type` → `pos.Service.SetOrderType` →
`applyTablePolicyLocked` (ADR-0073 Decision 5, ut-docs#1355) clears
`tableID`/`tableLabel` the moment no dine-in line remains.
`selfOrderForcesCounterCheckout` reads exactly that `TableID()`, so one
tap on the Takeaway button — the most prominent control in the cart —
silently defeated the card's core acceptance criterion and let a
table-bound guest's checkout fall through to the card/contactless payment
picker. Reproduced for real: a completed `sales` row, from a session with
no card terminal.

**Fix:** a table-bound session is clamped to dine-in server-side (the
anonymous, auth-exempt surface can never rely on UI gating alone — same
pairing ut-docs#1355 already established for the cashier's own table
picker). The cart shows which table the order is going to instead of the
toggle when a table is bound (reuses the existing `basket.table.label`
key — no new i18n key, no language-pack follow-up for this one).
Deliberately not re-applying the table after a switch to takeaway: that
would leave an all-takeaway basket holding a table, exactly the state
ADR-0073 D5 forbids.

Regression tests: `TestSelfOrderShop_TableCheckout_TakeawayToggleCannotUnbindTable`,
`TestSelfOrderShop_NoTable_TakeawayToggleStillSwitches` (pins plain kiosk
sessions are unaffected).

### BLOCKER 2 — the 60-second idle reset threw the table binding away

`self_order_shop.html`'s idle timer navigated to a hardcoded
`/self-order`, which resets the basket and — with no `?table=` — leaves
the session unbound. Exactly right for the till's own kiosk terminal;
on a guest's own phone, putting it down for a minute to talk to the
table silently dropped them back onto the card path (blocker 1's harm
again, via the *default* idle path, no user action required).

**Fix:** `GET /self-order/shop` now renders an `idleResetURL` carrying
`?table=<id>` for a bound session, re-validated on the way back in
exactly as on first scan. Plain kiosk sessions keep today's bare
`/self-order`. Test: `TestSelfOrderShop_IdleResetKeepsTableBoundSession`.

### BLOCKER 3 — one process-global basket, N guest phones (mitigated, not solved)

`common.Deps.KioskEngine` is a single till-process-global basket
(ADR-0020, `scripts/ci/guard-kiosk-engine.sh`) — correct for one physical
kiosk, not for a QR printed on every table. Reproduced for real: guest B
loading `/self-order?table=T2` while guest A had an in-progress
`T1`-bound basket silently wiped A's basket and rebound the session to
T2 — A's order would be lost, and B's checkout would have landed on A's
table.

**This needs a session-scoped basket to fix properly — a real
architecture change, not a review fix** (no existing ADR documents
basket-concurrency as a deferred limitation, and it wasn't reasoned about
in the original BA/Architect pass). Filed separately: ut-docs#2261.

**Mitigated here, in this same round, rather than shipped un-mitigated:**
a narrow fail-safe guard in `registerSelfOrder` — a DIFFERENT, non-empty
`?table=` colliding with an active (non-empty) table-bound basket now
renders a "till busy" screen instead of silently taking over. Re-scanning
your OWN table (a deliberate restart, or the idle-reset bounce) always
proceeds normally; an empty table-bound basket (nothing to lose) never
blocks a different table from taking over. This does not make ordering
concurrent — it makes the existing single-basket limitation fail safe
(a clear message) instead of failing silently (a lost or misdelivered
order). New i18n keys `selforder.table_busy.*` (3, same self-translation
caveat as the QR keys). A bare kiosk hit (no `?table=` at all) is
deliberately left unchanged — the physical kiosk's own pre-existing
"always start fresh" behaviour, a different risk also left to #2261.

Tests: `TestSelfOrder_DifferentTableWithItems_ShowsBusyInsteadOfWiping`,
`TestSelfOrder_SameTableRescan_NeverBusy`,
`TestSelfOrder_DifferentTableNoItemsYet_NotBusy`.

### BLOCKER 4 — `guard-docs-shots.sh` failed (CI-blocking) — fixed by regenerating screenshots

The reviewer found this already failed on the pre-review diff (the app
surface genuinely changed — a new button/dialog on `/tables`, a changed
cart) and flagged it as unfixable in their own sandbox (no browser/running
app available there). **This session's sandbox does have a pre-installed
Chromium** (`/opt/pw-browsers`, exactly what
`e2e/scripts/docs-shots.sh`/`resolve-chromium.sh` were built for,
ut-docs#622) — ran the real `make docs-shots` (via `bash
e2e/scripts/docs-shots.sh`, `npm ci` + the pre-installed-Chromium
fallback path): 124/124 screenshots captured, manifest regenerated.
`guard-docs-shots.sh` now passes (`surface a7f8fd7cf79e…, fresh`), along
with its own test suite (`guard-docs-shots_test.sh`,
`guard-docs-shots-cross-check_test.sh`,
`update-docs-shots-surface-hash_test.sh`). Only `web/help/img/manifest.json`
changed — the four `tables.png` locale screenshots came back byte-identical
to before (the new QR action doesn't visibly change the captured
viewport/state for this topic's screenshot scenario), which the manifest's
recomputed topic hashes confirm rather than contradict.
`resolve-chromium.sh` printed a non-fatal version-drift warning (reused
Chromium 141.0.7390.37 vs. the `@playwright/test` pin's expected
149.0.7827.55) — by the script's own design this is informational, not a
failure; noting it here per that script's own "not silent drift" intent.

### Should-fix — fixed this round (from the reviewer's pass)

- Cart button label ignored table-binding (`CounterMode` read
  `KioskPaymentMode` alone) — in ar/fa/tr `selforder.checkout` literally
  means "Pay", so a table-bound guest's button still promised payment on
  the phone. Now uses `selfOrderForcesCounterCheckout(d)`.
- The tables-page QR dialog opened unconditionally on `hx-on::after-request`,
  so a failed request (403/500) would show the *previous* table's QR as if
  it were the current one. Guarded with `event.detail.successful`.
- The QR dialog's `✕` close button had no accessible name — added
  `aria-label="{{ T "common.close" }}"` (existing key).

### Should-fix — left as notes (out of scope for this diff)

- **Language-pack follow-up (lang-pack-drift):** 6 new core keys
  (`tables.qr.*` ×3, `selforder.table_busy.*` ×3) need `ut-plugin-language-
  {de,es}` follow-up PRs. Per `scrum-master/SKILL.md`'s lang-pack-drift
  rule, since these are brand-new keys (not an existing key a pack merely
  hasn't caught up to), core merges first and `main` goes red on
  `lang-pack-drift` until the pack PRs land — the merging lane (this one)
  owns landing them in the same cycle. Tracked in the close-out, not
  silently dropped.
- Pre-existing: `ar`'s "pay at counter" keys (from ut-docs#582) translate
  as "pay at the *table*" (`الدفع عند الطاولة`), which was merely odd
  before and now reads strangely alongside a genuinely table-bound order
  ("C-4 · الطاولة 5" — table mentioned twice). Out of scope to retranslate
  existing keys here; worth a follow-up alongside the native-speaker pass
  already flagged for the new ar/fa/tr strings.

### Nits — left as notes

- tr's `tables.qr.caption` inserts "masası" ("Masa 5 masasındaki"),
  slightly redundant; other locales don't add the extra word.
- `TestKioskCounterOrdersPage_NoTableOrderShowsNoTableSuffix` asserts the
  response body contains no `·` character — brittle against unrelated
  future use of that glyph.
- Migration 029 has no `ON DELETE` clause; tables are soft-disabled per
  ADR-0054 (never hard-deleted in practice), so this is a documented,
  accepted tail risk, not a gap.

## Looked at and found genuinely fine (reviewer's independent read)

- Migration correctness/idempotence (verified against `execMigrationStatements`'s
  real `ADD COLUMN` skip logic, not just asserted).
- `ListOpen`'s `LEFT JOIN tables` — correct join shape, correct `COALESCE`
  use, resolves the table label fresh at read time rather than freezing a
  stale one (matches `GetSaleDetail`'s own convention).
- No money-path regression: `kiosk_counter_orders` still carries no amount
  columns; nothing in the diff should be `money.Money` and isn't.
- The two recurring bug classes this pipeline watches for (a file-write
  handler missing `os.MkdirAll`; a cwd-relative path where `paths.Data(...)`
  belongs) — neither applies; the diff performs no file writes at all.
- Security: `/api/tables/{id}/qr` is genuinely manager-gated
  (`requireManager` → `canPerform(d, r, "settings")`, with a cashier-403
  test); the QR encodes only a URL with a table UUID, no secret/token;
  guessing a table id buys an attacker nothing beyond forcing their own
  session onto the counter path.
- i18n mechanics (every new string through `T`, including the three
  `ErrKey` values `guard-i18n.sh` can't statically see because `T` is
  called with a variable — hand-checked present in all 4 locales), RTL
  safety (no `left`/`right` in new markup), touch target sizing (QR button
  reuses the row's existing `.btn.secondary` sizing), and `alt=""` on the
  QR `<img>` (correct — caption + URL text are the real accessible
  content, matching the existing ut-docs#527 tracking-QR convention).
- Translation plausibility: ar/fa/tr strings (both the i18n additions and
  the help-doc bullets) are grammatical, register-consistent with
  neighbouring strings, and specifically checked against existing
  terminology in the same files (e.g. ar's "scan" verb matches
  `pos.toast.scan_prompt`) — still want the native-speaker pass both the
  Dev and this record flag.
- No real client/shop name and no secret-shaped literal anywhere in the
  diff.

## Independently re-verified (this session, beyond the reviewer's own pass)

- Reviewed the reviewer's actual diff line-by-line before accepting it.
- TDD-re-verified blocker 3's own fix personally: removed the busy-guard's
  table-collision check, confirmed
  `TestSelfOrder_DifferentTableWithItems_ShowsBusyInsteadOfWiping` fails
  with the real cross-contamination reproduced (table A's basket and
  binding overwritten), restored the fix, confirmed green.
- Full `go build ./...`, `go vet ./...`, `gofmt -l`, `golangci-lint run`
  (touched packages), and the complete `go test ./...` (repo-wide, not
  just touched packages) re-run clean after every fix round, including
  after the docs-shots regeneration.
- Every CI guard re-run and green: `guard-kiosk-engine.sh`,
  `guard-i18n.sh`, `guard-data-access.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh` (one pre-existing baseline entry updated for the
  `tr`/`tables` bullet-count shift, itself deliberate and explained
  in-place in `scripts/ci/i18n-baseline/help-drift-baseline.json`),
  `guard-compliance-claims.sh`, `guard-page-http-error.sh`,
  `guard-docs-shots.sh` (+ its own three regression-test scripts).

## Explicitly deferred (not this card)

- ut-docs#2261 — concurrent multi-table ordering (session-scoped basket);
  this card ships the fail-safe "busy" guard, not real concurrency.
- ut-docs#2262 — card/online payment captured from the guest's own phone
  (blocked on the billing/payment-gateway arc, ut-docs#725).
- ut-docs#2263 — a guest re-scanning their table's QR to resume an
  already-open order rather than always starting fresh.
- `ut-plugin-language-{de,es}` follow-ups for the 6 new core keys —
  owned by this same lane/cycle per `scrum-master/SKILL.md`'s
  lang-pack-drift rule; tracked in the cycle close-out.
- ar's pre-existing "pay at the table" mistranslation of the #582 counter-
  order keys — a genuinely separate follow-up, not this card's to fix.
