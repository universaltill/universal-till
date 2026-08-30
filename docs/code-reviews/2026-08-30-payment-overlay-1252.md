# Sale screen: payment overlay instead of an always-visible tender panel (ut-docs#1252)

**Card:** ut-docs#1252 — product owner UX proposal, 2026-08-28. **Complexity:** medium.
**Dev:** Sonnet (inline). **Review:** Opus (worktree-isolated, one round + a scoped fix round).

## What shipped

The sale screen's `.tender` column used to always render scan-row +
held-sales + Pay/Split tabs + pay-grid (Cash/Card/…) + split-tender form +
Hold Sale/New Customer, all inline, all the time — "we shouldn't show all
payment things." Now:

- The default view is just scan-row, held-sales, a **Hold Sale** button and
  a new **Payment** button (`data-testid="payment-open"`).
- Tapping Payment opens a native `<dialog id="payment-overlay">`
  (`.showModal()`) containing the Pay/Split tabs, pay-grid, split-tender
  form and New Customer — a slide-in panel anchored to the inline-end edge
  (`inset-inline: auto 0`, so RTL correctly opens from the left), with a
  near-transparent `::backdrop` so the basket — a separate CSS grid area,
  untouched by this change — stays visible while paying, unlike this
  file's other dialogs (`#modifier-modal`/`#hold-modal`/`#pfand-modal`),
  which really do want to block the screen.
- Below `.pos-container`'s own 900px stacking breakpoint (where
  basket/tender/products already render one-above-the-other instead of
  side by side), the overlay goes full-screen instead — a side panel and a
  visible basket can't both fit at that tier regardless.
- Two new locale keys (en/ar/fa/tr): `tender.open_payment` ("Payment"),
  `common.close` (the overlay's ✕ button).
- 12 e2e spec files updated to open the overlay before interacting with
  what moved inside it; `web/help/*/sell.md` and `payments.md` (all four
  locales) updated to describe the new flow; screenshots regenerated
  (`make docs-shots`).

No `.go` files touched — this is a `web/ui` + `web/public` + locale + e2e
change. Money, data-access/repository pattern, and plugin signing are
untouched.

## Independent review (Opus, worktree-isolated)

Verdict: needs fixes — 1 blocking, 3 should-fix, 1 flagged for a product
call (resolved as an engineering fix, see below). All confirmed and fixed
in the same session; a second, scoped pass re-verified each fix live.

**Blocking (F1) — fixed.** The overlay's full-screen fallback was keyed to
`max-width: 480px`, but `.pos-container` itself stops being a side-by-side
grid at **900px**. Between 481–900px the layout was already stacked *and*
the overlay still rendered as a fixed-width side panel, landing on top of
the basket instead of beside it — measured live (real Chromium, real
server): 58% of the basket covered at 768px (iPad portrait), 100% at
900px down to 481px. **Fix:** the fallback breakpoint now matches
`.pos-container`'s own 900px stacking point. Re-measured after the fix:
0% overlap at 1024/1280px, 100% (full-screen, intended) at 900px and
below.

**Should-fix, addressed:**

- **F2** — `common.close` was added but never referenced; the overlay's ✕
  button used `aria-label="{{ T "notice.dismiss" }}"` ("Dismiss") instead,
  the wrong word for a panel-close control. Fixed: the button now uses
  `common.close`, and `guard-i18n.sh`'s resolved-key count moved from 1312
  to 1313, confirming the key is genuinely used now.
- **F3** — the manual's ar/fa/tr `sell.md`/`payments.md` prose still
  described the old "choose Pay" flow even though their screenshots were
  regenerated to show the new Payment button — this repo's last two
  `sell.md`-touching commits both updated all four locales in lockstep, so
  this wasn't a tolerated lag. Fixed: translated prose added to all three,
  written direction-neutral (no "on the left" — the layout mirrors under
  RTL, confirmed by the fa screenshot: basket renders on the *right* under
  `dir="rtl"`).
- **F4** — the overlay's `box-shadow: -8px 0 24px …` was the one physical-
  direction value in an otherwise all-logical-properties block; under RTL
  the panel anchors to the opposite edge, so the shadow fell off-canvas.
  Fixed: made the shadow symmetric (`0 0 24px`) instead of chasing it with
  a `[dir="rtl"]` override.

**Flagged for a product call, resolved as an engineering fix:** Hold Sale
had been moved into the overlay along with everything else it used to sit
next to, but the overlay is *modal* — that made "hold this basket for the
next customer" cost an unrelated trip through Payment, with no other entry
point. New Customer staying inside the overlay is fine (duplicated by the
header's own New Sale button already); Hold Sale wasn't. This didn't need
escalating to a human — it's a straightforward corollary of the card's own
requirement ("still show the items", i.e. don't make ordinary
basket-building actions depend on opening Payment) — so it's fixed
directly: Hold Sale now lives in the default view (a new
`.tender-default-footer`, alongside Payment), not inside the dialog.

## Verified beyond automated tests

Drove the real compiled binary against a fresh seeded SQLite DB (temp
`UT_DATA_DIR`) with real headless Chromium and screenshotted the open
overlay at:

- **1024×600** (kiosk floor) and **1280×800**, en — panel readable, basket
  fully visible alongside it, no clipping.
- **360×800**, en — overlay correctly goes full-screen (intended
  trade-off at this tier), no horizontal scroll, every control reachable.
- **1280×800**, fa (RTL) — panel opens from the left as intended, basket
  fully visible on the right, tabs/labels correctly mirrored, no
  overlap/cut-off.
- **1024×600**, tr — longest-locale check; "Yeni müşteri" and every button
  label fit on one line, no wrapping.

Also re-measured the F1 fix live at 480/481/768/900/901/1024/1280px (see
above) rather than trusting the CSS change alone.

**Not independently re-screenshotted:** the non-default color themes
(fresh/slate/amber). The new CSS uses only existing design tokens
(`var(--surface)`, `var(--border)`, `var(--radius-lg)`) throughout, the
same tokens every other dialog in this file already relies on for
theming, so this is a low-risk gap, not a silent one — noting it rather
than implying it was checked.

## Gate

`gofmt -l .` empty (no `.go` files in this diff); `go build ./...`,
`go vet ./...`, full `go test ./...` (42 packages, no failures);
`guard-i18n.sh`, `guard-help-topics.sh`, `guard-data-access.sh`,
`guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
`guard-compliance-claims.sh`, `guard-docs-shots.sh`,
`guard-webkit-version.sh`, `guard-kiosk-launch-flags.sh`,
`guard-android-status-address.sh`, `guard-android-i18n.sh`,
`guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
`guard-autofill-suppression.sh`, `guard-makefile-version.sh`,
`check-brand-assets.sh` — all pass, re-run clean after the fix round.

e2e: the 17 spec files touching the moved markup (`tender-panel-
reachable`, `sale`, `manual`, `rtl`, `hold-named-tab`, `index-keyboard-
1023`, `inventory-to-till`, `bugreport-panel`, `split-tender-i18n-925`,
`split-tender-underpayment-921`, `tab-bar-overflow-aria-424`, `settings-
osk`, `sale-screen-213`, `phone-width-layout-413`, `focus-border-contrast-
797`, `form-label-layout-300`, `form-input-contrast-305`) all pass. This
environment's pre-installed Chromium build doesn't match the pinned
`@playwright/test` version (a known, separately-tracked gap, ut-docs#1283)
— worked around locally with a temporary `launchOptions.executablePath`
edit to `e2e/playwright.config.ts`, reverted before every commit (never
part of this diff).

## Explicitly deferred / follow-up cards filed

- **ut-docs#1309** — `tender.open_payment`/`common.close` need adding to
  the external `ut-plugin-language-{de,es}` packs (this session's
  `add_repo` tool was unavailable, so those repos couldn't be attached to
  do it directly this cycle).
- **ut-docs#1310** — found while verifying this card, reproduced
  identically on `main` with zero changes from this branch:
  `settings-osk.spec.ts`'s cancelled hold-sale-dialog test leaves a stray
  basket item that leaks into `split-tender-i18n-925.spec.ts` when the two
  files run in that order (which Playwright's own file discovery order
  does, alphabetically). Pre-existing test-isolation gap, unrelated to
  this diff's markup changes — not fixed here to keep this diff scoped to
  the card.
- Non-default theme screenshots not independently re-taken (see
  "Verified beyond automated tests" above) — low risk given token reuse,
  noted rather than silently skipped.

## Safe-to-merge verdict

Yes. Independent review's one blocking + three should-fix findings are
all fixed and re-verified; the Hold Sale placement issue it flagged is
resolved directly rather than escalated, since it followed from the
card's own requirement rather than being a new business decision. Full
gate green, e2e green (17 touched spec files), two follow-up cards filed
for genuinely out-of-scope items.
