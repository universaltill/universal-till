# Review: self-order user-manual topic (ut-docs#338)

**Scope:** branch `docs/338-self-order-manual`, uncommitted diff:
`web/help/en/self-order.md` (rewritten topic: which shops it suits, set-up,
what the customer sees step by step, pay at the counter, getting out of kiosk
mode, what can go wrong; now claims `routes: [/self-order, /self-order/shop,
/self-order/language]`), `e2e/tests-docs/docs-shots.spec.ts` (optional
`interact` hook on extra shots + four self-order recipes with setup/teardown),
20 new PNGs `web/help/img/{en,fa,ar,tr}/self-order{,-shop,-options,-checkout,-counter}.png`,
`web/help/img/manifest.json`.

**Author:** Dev agent (Sonnet). **Reviewer:** Opus 5.5 (independent).

## What shipped

- A shop-owner manual for self-order kiosk mode, replacing the "full setup
  guide will be added soon" stub. Every quoted UI string and error message
  matches `web/locales/en.json`.
- docs-shots: `ExtraShot.interact` runs after the htmx settle and before the
  screenshot (forces reduced motion when present). Recipes stage a two-line
  kiosk basket through `/api/self-order/scan`, set the kiosk payment mode
  through `/api/settings/kiosk-payment-mode`, open the options/payment
  dialog via `openKioskModal`, and restore (walk-up `GET /self-order` resets
  the `KioskEngine` + kiosk language cookie; mode back to `kiosk`).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low | Set-up step 1 said "anyone else is asked for a manager's PIN"; a non-manager cannot open Settings at all (Tester), so the parenthesis promises a prompt nobody sees. | **Fixed:** "Only a manager can change it, and choosing it signs that screen out." |
| 2 | Low | "What can go wrong" said the no-methods message means "No card-type payment method is active", contradicting step 6 (every active non-cash method is offered; `ListActiveNonCashPaymentMethods`). | **Fixed:** "No payment method other than cash is active." |
| 3 | Info | Spec: `interact` is optional and only passed by extras; `capture()` for topic and dialog shots is unchanged (reduced motion is applied only when `interact` is set). Setup runs inside the `try`, so `restoreKiosk` always runs in `finally` even when setup fails half-way (e.g. counter mode set, then a scan fails). Fresh page per test; `workers: 1`; extras run after every topic shot. No leak found: basket emptied (also unblocks Settings → Data cleanup), mode written back to the default `kiosk`, kiosk language cookie cleared. | No change |
| 4 | Info | `openKioskModal` swallows an htmx-settle timeout (`.catch`), same as `capture()`; `ready` then asserts the dialog content, so a wrong frame still fails and its PNG is removed. | Accepted |
| 5 | Info | Rewrite dropped two minor sentences from the stub: browser-print-dialog receipts also use the customer's language; a changed item photo appears in an open cart on its next update. Neither is set-up critical. | Accepted |
| 6 | Nit | The shop screen has a **Back** button (visible in `self-order-shop.png`) that the Browse step doesn't name. | Accepted (self-explanatory) |
| 7 | Nit (pre-existing) | Counter dialog: **Cancel** wraps under **Place order** at 1024 px (visible in `self-order-counter.png`). | Not this card |

No compliance or competitor wording; offline framing is right ("No network:
the sale still completes. **Pay at counter** needs no network at all; card
payment at the kiosk depends on your payment terminal"). No dev jargon in the
prose.

## Verification

- **Tester's driven run** (scratchpad `338/tester.md`): throwaway till with
  auth on, demo catalogue; drove profile switch, 🔒 exit with cashier and
  kiosk PINs, restart persistence, pay-at-kiosk checkout + confirmation
  (plain order numbers 1, 2; no tracking QR), counter flow (`C-1` in Open
  orders), idle reset (~29 s, basket cleared, language back), tr/ar
  rendering. Satellite/additional-till paths verified from source and the
  passing Go tests. Ten corrections were applied by Dev afterwards; I
  re-read the final topic against them.
- docs-shots: Tester re-ran `-g self-order` (20 passed, PNGs + manifest
  byte-identical to Dev's); `sell` and `sell-browse-tiles` (existing extra
  shot through the changed `capture()`) passed.
- Reviewer, after the two fixes: `node tests-docs/write-manifest.js`
  (34 topics, 7 extra shots); `run-docs-shots-guard.sh`,
  `guard-help-topics.sh`, `guard-help-terms.sh`, `guard-help-breadcrumbs.sh`,
  `guard-competitor-naming.sh`, `guard-compliance-claims.sh`,
  `guard-i18n.sh`, `guard-fixed-dates.sh` all rc=0;
  `go test ./internal/manual/...` ok. `playwright test -c
  playwright.docs.config.ts --list` loads the spec (164 tests, 20
  self-order); no `e2e/tsconfig.json`, and an ad-hoc `tsc --noEmit` on the
  spec reports only missing `@types/node`, nothing in the new code. No
  captures re-run (prose-only fixes; PNGs untouched).
- Looked at `en/self-order-shop.png`, `en/self-order-options.png`,
  `en/self-order-counter.png`, `ar/self-order-checkout.png` (RTL mirrored,
  methods/Cancel/🔒 inside the dialog, no clipping, no focus ring or hover).

## Verdict

**PASS** with the two wording fixes applied.

## Deferred

- `customer-order-tracking.md`: its front-matter `summary` ("Customers scan
  a QR code, on the self-order kiosk's confirmation…") and steps read as if
  the QR exists today; only the body banner says "not available yet".
  Hedge the summary — follow-up card.
- No screenshot of the kiosk confirmation ("Order placed!") — needs a
  checkout POST against a staged payment; follow-up.
- No screenshots for the PIN exit (`/login?next=kiosk`) or the Settings
  cards (Device profile, Kiosk payment mode, Kiosk idle reset); follow-up.
- Harness freshness gap: the surface hash only counts a Go handler file if
  one of its routes is a topic's primary (first) route
  (`e2e/tests-docs/lib.js` `screenshotted`). Pages shot only as extras
  (`/self-order/shop`) are covered through `web/ui/**` templates but not
  their handler file, so a handler-only change does not stale the manifest.
  Follow-up card for the harness.

Pre-flight: help topic `updated` (self-order.md + docs-shots PNGs/manifest);
`reference/` page `n/a` (no behaviour change, docs-only); `README.md` `n/a`
(no stale text); `en.json`/`en.meta.json` `n/a` (no new keys); ut-cloud
locales `n/a` (till only); ADR index `n/a`; plugin taxonomy/mirrors `n/a`;
review record `updated` (this file); follow-ups listed above as cards to file.
