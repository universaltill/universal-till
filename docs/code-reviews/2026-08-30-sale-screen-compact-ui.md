# Code review — sale screen compactness pass (2026-08-30)

**Branch:** `sale-screen-compact-ui` · **Files:** `web/public/app.css`,
`web/ui/pages/index.html`, `web/ui/partials/nav.html`,
`web/ui/partials/basket.html`, `e2e/tests/tender-panel-reachable.spec.ts`,
regenerated `web/help/img/**`.

## Origin

Product owner, live UX feedback comparing the sale screen against a
competitor POS: Hold Sale + Payment should sit side by side and be
smaller (not the previous 4.2rem/1.15rem CTA styling); the header row
(New Sale / Inventory / Deposit refund + a large "Universal Till" `<h1>`)
was costing a whole row of height that could move into the shared nav
bar instead; the Dine-in/Takeaway toggle didn't need full-size buttons;
the basket's Subtotal/Tax/Total block should be 2 columns instead of 1
to roughly halve its height. A left icon nav rail (also praised in the
same feedback) was explicitly scoped OUT — deferred as its own piece of
work, confirmed with the product owner before starting, since it would
replace the top nav across the whole app, not just this screen.

## What shipped

- `nav.html`: `.logo` now wraps the image plus a new `<span
  class="app-name">` (previously only in the sale screen's own `<h1>`,
  now shared across every page). Image `alt` emptied since the adjacent
  text now carries that info.
- `index.html`: `.kiosk-header`'s `<h1>` removed; New Sale/Inventory/
  Deposit refund get `.compact` + an emoji prefix (🛒/📦/♻️).
- `app.css`: new `.btn.compact` (2.5rem/40px, secondary actions only —
  never money/tax path); `.tender-default-footer` row instead of column,
  `.payment-trigger` dropped to the base `.btn` size; `.basket .totals`
  2-column grid, `.total` spans full width; `.kiosk-header`/`.kiosk-
  actions` spacing trimmed now there's no heading above them; the
  `@media (max-height: 700px)` row-split override added earlier today
  (before this pass) removed entirely — no longer needed once the footer
  itself got this much shorter (see "What I measured").

## Independent review (Opus) — findings and how each was handled

The review was thorough and found the change safe to merge overall, with
real, well-evidenced findings applied here rather than deferred:

- **`.order-type-toggle` reverted to the base `.btn` size (48px), NOT
  `.compact` (40px).** The review caught something a compactness pass
  alone wouldn't: this toggle re-derives VAT (`internal/pos/service.go`,
  `tax_codes.takeaway_rate_basis_points` — the German §12 UStG dine-in/
  takeaway split, live pilot work). A mis-tap puts the wrong tax on a
  fiscal receipt — not a "binary toggle" in the low-stakes sense the
  first draft assumed. The freed height from removing the old `<h1>` row
  and the tender footer's old oversized styling affords this with room
  to spare; there was no need to spend the budget here.
- **`.order-type-toggle` and `.btn.compact` were duplicating the same
  values in two rules** (with a silent, almost-certainly-unintentional
  drift — `.9rem` vs `.92rem` font-size). Resolved by the above: the
  toggle no longer has a compact override at all, so the duplication is
  gone rather than consolidated.
- **The sale screen had no `<h1>` at all** after removing the old
  heading — the most-used page in the product had its outline jump
  straight to `<h2>` ("BASKET"/"PRODUCTS"). Added a visually-hidden
  `<h1>{{ T "app.name" }}</h1>` (the `.visually-hidden` class already
  existed, used elsewhere for `basket.html`'s item-count label).
- **`.app-name { display: none }` below 480px removed the brand from
  the accessibility tree entirely**, not just visually — `.nav-toggle`'s
  own labels say "Till"/"Menu", not the app name, so nothing announced
  it on a phone. Changed to the same visually-hidden pattern instead of
  `display:none`.
- **Dead CSS removed**: `body.kiosk .kiosk-header h1` no longer matches
  anything now `.kiosk-header` has no `<h1>` child.
- **Garbled comment fixed** in `.tender-default-footer`'s own rationale.
- **Long customer names (CRM plugin) wrapping in the 2-column totals**:
  a real name paired against a money figure in a half-width cell can
  wrap to 2 lines and drag the row taller. Gave `.totals-customer`
  (new class, `basket.html`) the same full-width treatment `.total`
  already had — the one field here with no predictable length the way
  every money amount has.
- **Added a regression test** for ut-docs#1327 (900px-width stacked
  tablet tier), confirmed below to be fixed as a side effect of this
  change — see "ut-docs#1327" section.

Two findings from the review were read and deliberately NOT acted on,
with reasoning:
- Two pre-existing issues the review confirmed are unrelated to and not
  worsened by this diff (a 490-610px nav horizontal-scroll bug, and
  clipping at 800×480, "below the documented 1024×600 floor") — left
  as-is per the review's own assessment that they're pre-existing and
  out of scope.

## What I measured (re-verifying the review's own headline claim)

The review's most consequential finding was that removing the `@media
(max-height: 700px)` override (added earlier today for the *previous*
iteration's oversized footer) is not just safe but an improvement — its
own viewport-height sweep at 1024px wide found the override's first
clip point was 570px (30px of margin under the 600px target), while the
plain base 4:3 ratio without it doesn't clip until 470px (130px of
margin) once the footer no longer needs the height the override was
compensating for. Re-ran the exact regression suite after applying every
fix above (not just before):

| check | result |
|---|---|
| `tender-panel-reachable.spec.ts`, all 4 cases (1024x600 base, 1920x800 @2x scale, 1024x600 held-sales matrix, 850x700 the new #1327 case) | pass |
| Full `--project=default` suite | 233/233 passed |
| `gofmt -l .` | clean |
| `go build ./...` | clean |
| `go test ./...` | one pre-existing, unrelated sandbox failure (`TestListenWithFallback_WildcardHostFallsBackToLoopback`) |
| all 18 CI guards | pass, including `guard-docs-shots.sh` (re-run after every fix) and `guard-emoji-font.sh` (covers the new 🛒📦♻️ icons — glyph-agnostic by design, asserts the CSS fallback stack + `fonts-noto-color-emoji`, not per-glyph enumeration) |

## Follow-up round, same session, same PR

After the review above landed, the product owner kept comparing live
against the SumUp reference and asked for four more things in the same
pass rather than as a separate change (all bounded compactness, no new
information architecture — that class of idea, see "Deferred to
backlog" below):

- **Removed `.status-pill` entirely** (`index.html`, `app.css`). It was
  a static "Online" label in the top header with no JS wiring at all —
  `base.html`'s footer already has the real, live status indicator
  (`#sb-conn`), so the top one was a dead duplicate, not a second real
  signal. The product owner spotted this by eye ("why we have it at the
  top as well"); grepping confirmed no script ever touched
  `.status-pill`. Removed the markup, the CSS rule, and a dangling
  `body.kiosk .status-pill` reference in the touch-target user-select
  rule; updated two comments elsewhere that cited it as precedent.
- **Shrunk `.scan-row` inputs** (barcode + qty fields) — `padding: .4rem
  .55rem`, qty width `3.8rem` → `3.4rem` — "the barcode text box is big
  as well."
- **Shrunk Hold Sale + Payment to the same `.compact` sizing as the
  other secondary actions** (`.tender-default-footer > .btn`, 2.5rem
  min-height / .9rem font, was 4.2rem/1.15rem) — "make the barcode and
  payment, hold sail smaller and their panel smaller, give more space to
  the products."
- **Denser product tiles**: `.grid` column floor `8.8rem` → `7rem`,
  `.btn-tile` min-height `8.5rem` → `6.5rem`, `.thumb` height `4.25rem`
  → `3.25rem`. Unlike an earlier abandoned attempt at this same density
  today, `.tile-name` keeps its 2-line clamp this time (verified against
  realistic German names — see below) instead of silently losing it.

### The row-ratio retune, and why it stopped at 5fr:2fr

Shrinking the tender footer freed real height, so `.pos-container`'s
`grid-template-rows` was revisited from the review's already-tested
`minmax(8rem, 5fr) minmax(0, 2fr)`:

| tried | products gain | result |
|---|---|---|
| 5fr:2fr (baseline, already tested above) | — | safe |
| 3fr:1fr | 293px → 314px (+21px) | `tender-panel-reachable.spec.ts` still passes, but 21px is marginal — confirms the 1024×600 shortfall (products wants ~476px, the right column only has ~427px total there) is a genuine floor-height constraint, not something ratio-tuning alone fixes |
| 4fr:1fr | more, but... | **fails** the held-sales case in `tender-panel-reachable.spec.ts` — Payment clipped |

Reverted to and finalized at **5fr:2fr** — the only one of the three
that's actually e2e-verified safe. Noted here so the next person who
looks at this ratio (this is the fourth documented tuning pass on it —
ut-docs#213, #161, #1231, this one) doesn't re-try 3:1 or 4:1 expecting
a different result without also re-running this exact test.

### "We only have place for 4" — investigated, not a layout bug

The product owner compared product-grid density directly against SumUp
and said we only fit 4 tiles where SumUp fits many more. Measured
directly (`productsScrollHeight`/`productsClientHeight` — no phantom
scroll cutoff) and cross-checked against the demo seed data
(`grep -c cat_dairy demo_catalogue.sql`): the "Dairy" category the demo
was showing genuinely only has 4 seeded items. This is a demo-data
artifact, not a grid/CSS ceiling — the density changes above are working
(more tiles fit per row and per screen than before), there was just
nothing more in that one category to show. No code change from this —
flagging here so it isn't rediscovered as a false lead later.

### Verified again after this round

| check | result |
|---|---|
| `tender-panel-reachable.spec.ts`, all 4 cases | pass |
| Full `--project=default` suite | 233/233 passed |
| `gofmt -l .` / `go build ./...` | clean |
| `go test ./...` | one pre-existing, unrelated sandbox failure (`TestListenWithFallback_WildcardHostFallsBackToLoopback`) |
| `make docs-shots` | 92/92, regenerated screenshots included in this diff |
| all 18 CI guards | pass |

### Deferred to backlog, not folded into this PR

The product owner's live feedback kept going past bounded compactness
into real information-architecture calls — where a feature *lives*, not
how big its button is. Captured as separate cards rather than bolted
into an already-large compactness diff:

- Left/right icon nav rail (SumUp-style) — already scoped out of this
  PR up front, re-raised again mid-flight; still deferred.
- "New Sale" moving to sit next to Payment.
- "Deposit refund" moving from the header row to the Menu page.
- Inventory + "Add new item" becoming small icons (SumUp-style),
  instead of full buttons in the header row.
- Reconsidering whether more than 2 payment methods should be directly
  visible next to Payment again (needs reconciling against ut-docs#1252,
  which deliberately moved payment methods behind the overlay).

## ut-docs#1327 — confirmed fixed, closing this session

The review measured this directly on two live builds (before/after, same
seed, same machine): pre-change, the Payment button was clipped at
850×700/900×700/900×600/850×600 regardless of held-sale count (margin
−39.7px to −74.3px); post-change, unclipped at every one of those with
10.7-12.9px of real margin, and a height sweep found the bug was
width-driven (broken at every height ≤800px pre-change) with ~140px of
new headroom post-change at 900×700. Screenshots confirmed visually — the
baseline shows the Payment button sliced in half by the panel edge; the
new build shows Hold Sale + Payment fully rendered side by side. A
regression test for exactly this case now ships in this same diff.

## Operational note, unrelated to the diff itself

While this review was running, this repo's shared local checkout was
concurrently switched to a different branch (`fix/1148-osk-inverted-
punctuation`, with its own uncommitted work) by something outside this
session's control — almost certainly the autonomous pipeline operating
on the same local clone. My own uncommitted diff was auto-stashed in the
process (`git reflog` shows `git stash` → `checkout main` → `pull` in the
same few seconds). Nothing was lost — the stash was intact and this
branch was rebuilt from it in an isolated worktree rather than the
shared checkout, which is where the rest of this session's work
happened from this point on. Worth a standing note for future sessions:
this local checkout is not exclusively this session's to hold dirty for
long — either commit promptly or work in an isolated worktree when a
change will span multiple turns.
