# Code review — Self-order kiosk: "pay at counter" mode (universaltill/ut-docs#582)

- **Branch:** `feat/582-kiosk-pay-at-counter`
- **Reviewed range:** `7074a56..HEAD` (merge-base with `origin/main`)
- **Reviewed by:** Reviewer role, independently, in an isolated worktree
- **Date:** 2026-09-11
- **Verdict:** **Safe to merge** after the two fixes recorded below.

---

## What shipped

A new per-till setting `kiosk.payment_mode` with two values — `kiosk`
(default, unchanged ADR-0020 card/contactless picker) and `counter`
("pay at counter").

In `counter` mode:

- `GET /api/self-order/checkout` renders a new confirm partial
  (`self_order_counter_confirm.html`) instead of loading payment methods at
  all — the branch is taken *before* `ListActiveNonCashPaymentMethods`, so a
  counter-mode kiosk never queries methods it will never show.
- `POST /api/self-order/checkout` takes a completely separate path
  (`completeCounterOrderCheckout`): **no `pos.SaleInput`, no
  `completeTender`, no `sales`/`payments`/`payment_methods` row at all**. It
  writes one `kiosk_counter_orders` row (migration
  `026_kiosk_counter_orders.sql`, repo
  `internal/data/kiosk_counter_orders_repo.go`), fires a best-effort async
  kitchen ticket, resets the kiosk basket, and renders the shared
  confirmation partial with counter-specific copy.
- New authenticated staff board `GET /kiosk-counter-orders` +
  `GET /ui/kiosk-counter-orders` (15s self-re-arming poll) +
  `POST /api/kiosk-counter-orders/{id}/collect` with the same htmx
  out-of-band row-removal shape `/orders` already uses.
- New `/menu` tile (`uislot.CoreMenu`, order 2050, no `VisibleIf` — any
  operator), 16 new locale keys across all four core locales, and manual
  topics in en/de/ar/fa/tr plus a regenerated screenshot set.

The card deliberately does **not** touch the Hold/park subsystem or the
existing completed-sale-based `/orders` board, to avoid colliding with the
in-flight Hold redesign (`universal-till#982` / ut-docs#1903). That was an
explicit Architect decision and is not re-litigated here.

---

## Gate re-run (by the reviewer, from scratch, not taken on report)

| Check | Result |
|---|---|
| `gofmt -l .` | clean (no output) |
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `go test ./...` | **all packages ok, exit 0** |
| `golangci-lint run ./...` | **0 issues** |
| All 26 CI-blocking guards in `ci.yml`'s `build` job | **all pass** |
| `shellcheck scripts/ci/*.sh` | **not runnable here** — no `shellcheck` binary in this environment (`guard-shellcheck-version.sh` fails for the same reason). This PR changes no shell script, so CI's own run is the authority. |

Guards individually confirmed green including the ones that matter most for
this surface: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
`guard-i18n.sh`, `guard-page-http-error.sh`,
`guard-migration-version-collision.sh`, `guard-help-topics.sh`,
`guard-help-drift.sh`, `guard-docs-shots.sh`, `guard-compliance-claims.sh`.

---

## Independent TDD re-verification (performed personally, not taken on faith)

Each of these was a real revert → run → observe → restore cycle in this
isolated worktree. The working tree was verified clean (`git status
--porcelain` empty) afterwards.

1. **The elapsed-age fix (`2e334ff`) is load-bearing.** Reverting
   `counterOrderRow.AgeMinutes` back to `CreatedAt` and the template back to
   `{{ datetime .CreatedAt }}` made
   `TestKioskCounterOrdersPage_ColumnShowsElapsedAgeNotAbsoluteTimestamp`
   fail, printing the actual regressed cell: `<td>11/09/2026 02:45</td>`.
   Restored → green.

2. **The counter-mode POST branch is load-bearing.** Removing the
   `KioskPaymentModeCounter` branch from `POST /api/self-order/checkout`
   made `TestSelfOrderShop_CounterMode_CheckoutCreatesCounterOrderNoSale`
   fail — the request fell through and rendered the payment picker.
   Restored → green.

3. **The zero-sales-row assertion is *itself* load-bearing, not just the
   markup assertions ahead of it.** This needed a second, more careful
   attempt and is worth recording. The markup checks (`"Order placed"`, no
   `payment-picker-method`) sit *before* the `COUNT(*)` checks and abort
   with `t.Fatalf`, so (2) alone does not prove the row counts are
   exercised. First sabotage attempt injected an `INSERT INTO sales (id,
   status, total, created_at)` — which **silently failed** the `NOT NULL`
   constraints on `receipt_no`/`subtotal` and was swallowed by `_, _ =`, so
   the test passed and proved nothing. Re-run with a valid insert
   (`id, receipt_no, subtotal, total`) and a panic on error, the test failed
   exactly as intended:
   `counter-mode checkout must create zero sales rows: before=0 after=1`.
   Restored → green. **The claim holds.**

4. **The manager-elevation gate on the new settings endpoint is
   load-bearing.** Removing the `checkOrElevate`/`needsElevation` block from
   `POST /api/settings/kiosk-payment-mode` made both
   `TestKioskPaymentModeEndpoint` and
   `TestSettingsEndpoints_RoleMatrix/kiosk-payment-mode/cashier_denied`
   fail (`cashier ... = 204, want 200 with the elevation prompt`).
   Restored → green.

5. **Both fixes added by this review were verified the same way** — see
   "Findings" below; each new regression test was confirmed failing with its
   fix reverted, and passing with it restored.

---

## Checks done by reading, beyond what any test asserts

- **Repository pattern.** All SQL in the diff lives in
  `internal/data/kiosk_counter_orders_repo.go` and
  `internal/db/migrations/026_kiosk_counter_orders.sql`. The handlers call
  repo methods only. Verified by reading the repo file in full, not just by
  trusting `guard-data-access.sh`. Raw SQL in `*_test.go` is test-support and
  is what the guard's own scope already allows.
- **Kiosk isolation (ADR-0020) — the single most safety-critical rule for
  this surface.** Grepped the *entire* Go diff, every changed file, for
  `Engine`: the only matches are `KioskEngine` (7 occurrences). The two
  pre-existing `d.Engine` references in `settings_page.go`/`init.go` are
  untouched by this diff and are not on a `/self-order` route file.
  `guard-kiosk-engine.sh` passes independently.
- **Money.** No price is persisted anywhere: the `kiosk_counter_orders`
  table has no money column, no FK to `sales`, and the kitchen ticket carries
  no price. The one place money *is* handled — the counter confirm screen's
  `{{ money .Total }}` — reads `d.KioskEngine.Basket().Total`, which is
  byte-for-byte the same expression `renderKioskPaymentPicker` already uses,
  so the customer is shown the same total the existing screen shows. Proper
  `money.Money` throughout; no raw `int64` arithmetic introduced. I
  initially suspected a divergence here (the kiosk POST path computes its
  total via `kioskSaleLinesAndTotal`, not `Basket().Total`) — checked, and
  the *displayed* total on both pickers comes from the same call, so there
  is no new discrepancy. **Dismissed after verification, not asserted away.**
- **Offline-first.** The counter checkout path is strictly *more*
  offline-safe than the kiosk path: local SQLite insert, engine reset, and an
  async goroutine for the ticket (`d.AsyncWork`-tracked, own timeout,
  best-effort). No fiscal signing, no plugin call, no network call is on the
  request path. No new modal blocker; the counter confirm partial carries its
  own `.selforder-exit` lock link, exactly as the sibling payment picker does
  (the ut-docs#208 finding), so exit stays reachable inside the `<dialog>`.
- **Double-tap on "Place order."** Checked: the first POST calls
  `d.KioskEngine.Reset()`, so a second POST hits the `len(lines) == 0` guard
  and returns 400 rather than creating a duplicate order/ticket. Same
  protection the kiosk path gets from `completeTender`'s own reset.
  **Dismissed.**
- **Auth.** `/kiosk-counter-orders` and `/api/kiosk-counter-orders/*` are
  *not* in `internal/auth/middleware.go`'s exempt set (which covers only
  `/self-order`, `/self-order/`, `/api/self-order/` and `/o/{token}`), so the
  staff board is behind the ordinary session gate. Confirmed by reading the
  middleware, not inferred.
- **i18n.** All 16 new keys are present in all four core locale files with
  real translations — verified by parsing every locale as JSON and diffing
  key sets against the merge-base: `en/ar/fa/tr` each went 2285 → 2301, zero
  keys removed, and all four key sets are identical. The large apparent
  locale diff is alphabetical re-sorting of pre-existing keys, not content
  change. No hardcoded prose in any new template or inline script; the one
  inline `onclick` copies the sibling picker's and contains no user-facing
  text. The `"title": "Pay at counter"` literal in the page handler follows
  the identical established convention in ~20 other page handlers and is not
  rendered as body copy.
- **RTL / design tokens / UX checklist.** No CSS was added at all — the new
  markup reuses `page-head`, `card`, `table`, `muted`, `btn`,
  `btn-actions`, `payment-picker*`, `selforder-exit`, all already defined in
  `app.css`. No literal `left`/`right` anywhere in the diff. Row-action
  button sizing matches `orders_list.html`/`tax_codes_table.html` exactly, so
  touch-target behaviour is the established one for a list row, not a new
  one. Real empty state (`kiosk_counter_orders.empty`) and a real error path
  (`common.LogAndLocalizedError` with `orders.err.server`). Longest-locale
  risk is low: the board is a plain `.table` with four short columns.
- **File-writes / path handling** — the two recurring bug classes this
  pipeline keeps finding. Neither applies: the diff adds **no** production
  file-write, so there is no missing `os.MkdirAll`, and no cwd-relative path
  where `paths.Data(...)` belongs. The only `os.WriteFile`/`filepath.Join`
  occurrences are in tests, all under `t.TempDir()`. The help images were
  produced by `make docs-shots`, i.e. tooling, not hand-written path code.
- **Manual prose actually true, not merely present.** Read the en and de
  topics end to end and checked each claim against the code: "sent straight
  to the kitchen without taking any payment" ✅, "oldest first" ✅
  (`ORDER BY created_at ASC`), "the row disappears immediately" ✅ (OOB
  delete), "never appears in reports, the journal or day-close totals" ✅
  (no sale row; `TestGenerateEOD_CounterOrderContributesNothing` pins it),
  "refreshes itself every few seconds" ✅ (15s), "works fully offline" ✅.
  The "☰ menu" wording matches the convention already used in
  `bug-reporting.md`/`order-status.md`. Every new page route is claimed by a
  topic's `routes:` and the page carries a `?` `helpLink`.
- **Migration hygiene.** `026` is genuinely the next free number on the
  freshest `origin/main` (highest there is `025`), append-only, `IF NOT
  EXISTS` throughout in line with 021/023/025.
  `guard-migration-version-collision.sh` passes.
- **No real client/shop name** used as demo/seed/test data — all fixtures are
  generic (`Flat White`, `Croissant`, `Tea`, `Oat milk`). No secret-shaped
  literal anywhere in the diff.

---

## Acceptance criteria on ut-docs#582, checked one by one

| AC | Status |
|---|---|
| Per-till setting; default stays today's behaviour | ✅ `kiosk.payment_mode`, `DefaultKioskPaymentMode = "kiosk"`, clamped on both load and save |
| Payment picker never reached; **no** `payment_methods` row, tender or authorization — "assert this, don't just hide the UI" | ✅ and the assertion is real: direct `COUNT(*)` on `sales`, `payments`, `payment_methods`, independently proven load-bearing (see TDD item 3) |
| Customer-visible order reference | ✅ own `C-`-prefixed sequence, deliberately never shared with the sale display-no sequence |
| Order ticket via the existing ESC/POS path | ⚠️ works for a shop using the legacy `printer.kitchen_addr`; **silently prints nothing for a stations-only shop** — see finding 3 |
| Appears on the live order view so staff see it if paper runs out | ✅ *by equivalent*, not literally: rather than the existing `/orders` board named in the card (#517), this ships a dedicated live board at `/kiosk-counter-orders` with the same 15s-poll + OOB-row-removal pattern. That is the Architect's explicit decision to stay clear of the in-flight Hold/`/orders` redesign (`universal-till#982`/ut-docs#1903), recorded here so the deviation from the card's literal wording is visible rather than silent. |
| Not a fiscal sale; absent from reports/X-Z totals/fiscal export | ✅ no sale row exists at all; `TestGenerateEOD_CounterOrderContributesNothing` pins it, and the table is excluded from sync-admin snapshots with a reasoned comment |
| Offline-first: kiosk still takes orders with no network | ✅ nothing on the request path touches the network |
| Strings via `{{ T }}` in every `web/locales/` file, **German included** | ⚠️ partially: all four *core* locale files (en/ar/fa/tr) have all 16 keys, and the German **manual** topic ships in this branch. But German *UI* strings live in the external `ut-plugin-language-de` pack, so they land only when that pack's follow-up PR merges. Since the card names a German café as the first user, that follow-up is on the critical path for this feature, not optional polish. |

## Findings

### 1. `MarkCollected` re-stamped `collected_at` on an already-collected order — **fixed**

**Severity: low-medium. Fixed in this review.**

`KioskCounterOrdersRepo.MarkCollected`'s own doc comment claimed a
missing/already-collected id was "a silent no-op (0 rows affected)". For a
missing id that was true; for an already-collected one it was not — the
`UPDATE ... WHERE id = ?` matched and overwrote `collected_at` with a new
timestamp.

This is reachable in normal operation, not just theoretically: the board
polls on a 15s cycle, so two tills can both be displaying the same still-open
row, and the second staff member's tap would silently rewrite when the
customer actually collected their order. `collected_at` is the only
timestamp this row carries beyond `created_at`.

Fixed by adding `AND status = ?` (open) to the update, which makes the
documented contract actually true. Regression test
`TestKioskCounterOrdersRepo_MarkCollectedTwiceKeepsFirstCollectedAt`
added and verified failing without the fix
(`rewrote collected_at: "2020-01-01T00:00:00Z" -> "2026-09-11T03:31:35Z"`).

### 2. The kiosk cart's own button promised "Pay" in counter mode on 3 of 4 locales — **fixed**

**Severity: low. Fixed in this review.**

The self-order cart's call-to-action renders `T "selforder.checkout"`. In
English that is the neutral "Checkout", which is why this is invisible to an
English-only read — but the shipped translations of that same key are
literally *Pay*:

| locale | `selforder.checkout` |
|---|---|
| en | `Checkout` |
| ar | `الدفع` ("the payment") |
| fa | `پرداخت` ("payment") |
| tr | `Ödeme` ("payment") |

Before this card that was accurate, because the button always did lead to
payment. Counter mode makes it false: the kiosk never charges anything, and
the very next screen says "There's nothing to pay here." A customer on an
Arabic, Persian or Turkish kiosk is told to pay and then told not to.

Fixed by rendering the **already-added** `selforder.counter.place_order`
("Place order") in counter mode only. Deliberately reuses an existing key —
this adds **no** new `en.json` key and therefore **no** extra language-pack
follow-up debt. Kiosk (default) mode renders byte-identically to before.
Regression test `TestSelfOrderShop_CartButtonLabelFollowsPaymentMode`
covers both modes and was verified failing without the fix.

### 3. A stations-only shop gets **no** kitchen ticket for a counter order — **accepted / needs a Backlog card**

**Severity: medium. Real, but genuinely outside this card's stated v1
boundary — not fixed here.**

`printCounterOrderTicketAsync` gates on `cfg.KitchenEnabled()` alone, i.e.
the legacy `printer.kitchen_addr` setting. The sale-based path does **not**
— `kitchenPrintingEnabledChecked` (`kitchen_print.go`) deliberately also
considers enabled kitchen stations, because "a shop that only configures
stations — and never fills the legacy setting — must still print
(ut-docs#516)".

So a shop that has migrated to `/kitchen-stations` and left
`printer.kitchen_addr` empty — a configuration this product explicitly
supports — will have every counter order silently print nowhere. The
customer is told "your order is on its way to the kitchen" and it is not.
The code's own comment frames this as a benign "nothing to send" no-op; for
that shop it is a dropped order.

Not fixed here because the card's explicit v1 non-goal is per-station
routing, and every reasonable fix (fan the single unrouted ticket out to
every printing station, or route properly) is a behaviour decision for the
Architect/UX, not a reviewer's unilateral change. **A Backlog card should be
filed:** *"Counter-order kitchen ticket never prints for a stations-only
shop (ut-docs#582 follow-up)"*.

### 4. Kiosk payment-mode settings card has no explanatory help line — **accepted, minor**

**Severity: cosmetic.**

The neighbouring kiosk-idle-reset card carries a
`<p class="muted">{{ T "settings.kiosk_idle_reset.help" }}</p>`; the new
payment-mode card has only a heading, a `<select>` and a `?` link. Flipping
this setting has a large consequence (the kiosk stops taking money
entirely), so an in-context sentence would be worth having. Not fixed:
the two option labels are self-describing ("Pay at counter" / "Card /
contactless (kiosk)"), the `?` links to a topic that explains it properly,
and adding a line means one more `en.json` key and one more language-pack
follow-up for marginal gain. Worth a Backlog card only if the product owner
wants it.

### 5. `settings.kiosk_idle_reset.apply` reused as the payment-mode submit label — **accepted, nit**

The new form's submit button borrows the idle-reset card's "Apply" key
rather than defining its own. Harmless (the string is generic and identical
in all four locales) and avoids yet another key; noted only so it is a
recorded decision rather than an accident.

### Reports from Dev/Tester checked and found correct

Every gate result Dev and Tester reported was re-run here and reproduced.
Nothing either of them claimed was found to be wrong. The one place their
report was *incomplete* rather than wrong is finding 3 above, which is a
scope boundary they were told to hold, not an error.

---

## Explicitly deferred

- **Finding 3** (stations-only kitchen printing) — Backlog card owed.
- **Cross-till sync of counter orders** — explicit v1 non-goal, correctly and
  deliberately recorded in `sync_admin_repo.go`'s `nonAdminTables` with a
  reasoned comment. Not a defect.
- **`lang-pack-drift` will go red on `main` after this merges.** This PR adds
  16 *brand-new* `en.json` keys. Per the reviewer skill's lang-pack rule,
  pack-first is impossible for new keys (the packs' own key-drift guard
  treats a translated key with no core counterpart as an orphan and fails),
  so merging core first and leaving `main` red until the
  `ut-plugin-language-{de,es}` follow-ups land is the **expected, bounded**
  state — not a reason to hold this merge. A pack follow-up is owed and needs
  its own card.

---

## Verdict

**Safe to merge.** The two fixes above are committed on this branch with
regression tests, the full gate was re-run green after them, the core safety
claim ("counter mode creates zero sales/payment rows") was independently
proven load-bearing at the DB-assertion level rather than only at the HTTP
level, and the kiosk-isolation rule is satisfied across every changed file.
