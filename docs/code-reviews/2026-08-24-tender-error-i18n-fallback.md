# 2026-08-24 — cashier tender handler: localize the last raw-English error fallbacks (ut-docs#921)

## What shipped

`ut-docs#921` ("Engine-originated error strings... render verbatim English
instead of going through i18n") named `completeTender`'s insufficient-stock
rejection as its example. Investigation found that specific case was
**already fixed** by a prior change (`httpx.T(locale,
"pos.toast.insufficient_stock")`, already in place). The self-order kiosk's
own tender handler (`self_order_shop.go`) was also already fully localized,
defaulting every unmatched error to a generic message.

The real remaining gap, in the **cashier** tender handler
(`internal/pages/pos_api.go`, `/api/pos/tender`):

- A fallback `http.Error(w, err.Error(), http.StatusBadRequest)` leaked raw
  Go error text for any `completeTender`/`CompleteSale` failure not already
  specifically handled — most notably underpayment
  (`"payments (%d) do not cover total (%d)"`), a common, real cashier
  scenario.
- The declined-payment branch had the identical leak:
  `http.Error(w, err.Error(), http.StatusPaymentRequired)` rendered
  `"payment declined: <method>"` verbatim.

### Fix

- New `classifyTenderError(err) string` maps a `completeTender` failure to
  its locale key (`insufficient stock` → existing
  `pos.toast.insufficient_stock`; `do not cover total` → new
  `pos.toast.payment_insufficient`; anything else → new generic
  `pos.toast.tender_failed`, mirroring the kiosk handler's own default).
  Replaces the old insufficient-stock special case **and** the raw fallback
  with one unified block, same `ToastMessage`/`ToastLevel` +
  `ui.NewBasketView` render pattern already used by the neighboring
  fiscal-gate branch (HTTP 200 — an htmx-partial swap, not a JSON error).
- The declined-payment branch now renders `httpx.T(locale,
  "pos.toast.payment_declined")` instead of `err.Error()`. Its 402 status is
  unchanged (deliberate — lets a caller distinguish a decline from a generic
  400).
- Three new locale keys (`pos.toast.payment_insufficient`,
  `pos.toast.tender_failed`, `pos.toast.payment_declined`) added to all four
  locale files (`en`/`ar`/`fa`/`tr`). The NAS Ollama translation endpoint
  (192.168.1.231) was unreachable from this cloud pipeline session
  (confirmed via a timed-out `curl`) — translated directly instead, matching
  the tone/register of neighboring `pos.toast.*` keys. Same accepted
  fallback `docs/code-reviews/2026-08-23-sale-search-stranded-headers-422.md`
  used, for the same reason.
- **External language packs** (`ut-plugin-language-de`/`-es`) synced in the
  same session so `lang-pack-drift` doesn't go red on `main` after merge:
  `ut-plugin-language-de#77` adds real German translations (this pack was
  already at 100% key parity); `ut-plugin-language-es#76` adds the 3 keys to
  the pack's own accepted-untranslated baseline (this pack's `pos.toast.*`
  namespace — including the pre-existing `insufficient_stock` — is already
  largely untranslated debt; ADR-0010's `T()` en fallback means this is a
  no-op for the shipped UI, not a regression). Both PRs currently show a
  `key-drift` check failure because their CI validates against core's
  *currently-merged* `main`, which doesn't have these 3 keys yet — expected,
  resolves once this PR merges; merged as a follow-up in the same cycle.

## Independent review

Two rounds, both via a fresh-context **Opus** subagent (this card is
`complexity:medium`, built at Sonnet — routing per the scrum-master skill).

**Round 1** (full diff): found **F1, blocking** — the split-tender panel's
client JS (`web/public/app.js`, `submitPayments()`) branched success/failure
on `response.ok` alone. Since the server's rejection path now answers 200
(an htmx-partial-swap contract, not a JSON error), `response.ok` was always
true, so a rejected tender wiped the operator's pending split payments and
declared **"Sale completed."** on a sale that didn't happen — exactly the
split-tender panel's own characteristic failure mode (it exists to
accumulate partial payments; submitting before covering the total is the
natural mistake). Also found **F2, non-blocking**: the declined-payment
branch had the identical raw-English leak, only partially closing #921's own
acceptance criteria.

**Fixes applied**: `submitPayments()` now re-queries the just-swapped DOM
for `#toast-message.error` before declaring success — if present, shows the
error and preserves pending payments instead. New e2e regression spec
(`e2e/tests/split-tender-underpayment-921.spec.ts`) pins both the rejection
case (never shows "Sale completed.", payment pill survives) and a companion
success case (a payment that actually covers the total still shows "Sale
completed."). F2 fixed as described above, with its own Go test.

**Round 2** (scoped to the F1/F2 fixes only, not a re-review of the whole
diff — earned because F1 was a money-handling blocker): **PASS, safe to
merge**. Independently re-verified both TDD claims by reverting each fix in
isolation and confirming the associated test fails with the real original
symptom, then restoring and confirming green. Confirmed no false-negative
risk in the `#toast-message.error` re-query (the element only ever renders
with class `error` on the server's designated error branch; a successful
tender's response contains no such element at all). Found three additional
LOW-severity, non-blocking notes (an unreachable duplicate English literal
in `app.js`, a test asserting only the negative case, and a pre-existing
unrelated raw-English 500 path in `EnsurePaymentMethod`) — the first two
fixed in this same pass (trivial), the third left as pre-existing/out of
scope.

## Verified beyond automated tests

- Full `go build`/`go vet`/`go test ./...` clean; `guard-i18n.sh`,
  `guard-data-access.sh`, `guard-kiosk-engine.sh` all green (the
  `guard-i18n.sh` output explicitly confirms "no hardcoded ToastMessage
  literals found").
- Real browser check (Playwright against the live `run-till.sh` server, not
  just Go httptest): drove a genuine underpayment through the real
  split-tender UI in **en** and **fa** (RTL), screenshotted both — correct
  layout, no overflow/wrapping, no console errors, RTL mirrors correctly,
  no raw `"do not cover total"` text anywhere in either page.
- e2e regression suite: `split-tender-underpayment-921.spec.ts` (both
  cases), plus the neighboring `sale-screen-213.spec.ts` and
  `tender-panel-reachable.spec.ts` re-run for regressions — all green.
- Both TDD claims (F1 fix, F2 fix, and the original underpayment/declined-
  payment fixes) independently reverted-and-restored by the round-2
  reviewer, confirming each test fails with the real original symptom
  before the fix and passes after.

## Non-goals / explicitly deferred

- `EnsurePaymentMethod`'s pre-existing raw-English `http.StatusInternalServerError`
  path (`pos_api.go:~697`) — a genuine remaining gap, but unrelated to this
  card's scope (a 500-class internal failure, not a tender-rejection path) —
  filed as a new Backlog card.
- No sweep of the ~29-file-wide `http.Error(w, err.Error(), ...)` pattern
  elsewhere in the codebase — filed as a new Backlog card.
- Settings.html/self-order's own hardcoded English JS strings noticed while
  visually verifying the toast ("No pending payments yet.", "Sale
  completed.") are pre-existing, unrelated to this card (a known,
  documented `guard-i18n.sh` gap: `web/public/**` isn't scanned) — filed as
  a new Backlog card.
- No `self_order_shop.go` changes — verified already fully correct.

## Safe-to-merge verdict

**Yes.** Both review rounds passed; full gate green; real browser
verification done in two locales including RTL; TDD claims independently
re-verified twice over (once per round). Merging with `merge_method:
"merge"` (never squash/rebase — ut-docs#250) once CI is confirmed green on
the PR's head.
