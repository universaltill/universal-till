# Code review — Payment-trigger clipping after ut-docs#1252 (2026-08-30)

**Branch:** `product-tile-density` (name is stale — see "Naming note" at the
bottom) · **Files:** `web/public/app.css`, `web/ui/pages/index.html`,
`e2e/tests/tender-panel-reachable.spec.ts`, regenerated `web/help/img/**`.

## Origin

ut-docs#1252 landed on `main` while a separate density/clipping fix for the
*pre*-#1252 always-visible tender panel was in flight on this branch — a
genuine parallel-work collision, not a merge conflict caused by either side.
#1252 replaced that always-visible panel with a product-owner-proposed
pattern: default view is scan-row + Hold Sale + a single "Payment" button;
Cash/Card/Split now live behind a `<dialog>` opened by that button. This is
structurally the same "one primary action, everything else on demand"
pattern this session had independently found while researching a
competitor POS (SumUp) — ut-docs#1311 was closed as superseded by #1252
rather than duplicated.

Live-testing #1252 at the 1024x600 kiosk floor found the **Payment button
itself** clipped by the footer, with no scroll — the exact clipping-symptom
class #1252 was meant to retire, now on its own trigger button instead of
the old pay-grid. This doc covers fixing that.

## Three iterations, two of them independently reviewed and rejected

This is not a clean single-pass fix — worth recording honestly, since the
history is what justifies the final shape.

**Attempt 1 — flip `.pos-container`'s row split.** Same lever the
pre-#1252 fix on this branch had already used. An independent review
(Opus, isolated worktree) rejected it: not wrong, but redundant with a
`1fr:1fr` alternative that measured better at every viewport, and it
reproduced ut-docs#1231's own reported symptom (thumbnail-only product
tiles) at 1280x800 — the review's full findings are superseded by this
doc, not repeated here.

**Attempt 2 — `position: sticky` on `.tender-default-footer`.** A second
independent review (Opus, in-place on the working tree) measured this
properly and found it real but insufficient: **B1** — the fix left ~0px
to +2.6px of clearance depending on footer content (a production
"Update now" pill made it worse), not the claimed clear margin. **B2** —
the serious one: held sales are persistent DB state, not a transient, and
a growing held-sales strip pushed the sticky footer **worse than the
original bug** (fully off-screen at 3 held sales, vs the pre-fix ~34px
clip). **B3** — the `max-height: 640px` cutoff created a hard cliff at
641px (the layout flips from working to broken over a single pixel of
viewport height) instead of the measured ~686px break-even. **B4** — no
regression test existed, and the existing spec structurally cannot see
this class of bug (it opens the payment overlay before asserting, so it
only ever tests elements *inside* the modal). Full findings in that
review's own output; not duplicated in the repo since the fix it reviewed
was superseded before commit.

Independently, while implementing the review's own suggested direction, a
**third bug** was found (not by review — by direct measurement): `position:
sticky` combined with this flex column's `margin-top: auto` rendered the
footer's box **overlapping the scan-row itself**, at one point making the
scan-row's own "Add" button genuinely unclickable
(`elementFromPoint` resolved to `.tender-default-footer`, not the button
inside it) — confirmed live via Playwright actionability failures, not
theoretical.

**Attempt 3 (shipped) — the `.basket-scroll`/`.totals` pattern, applied to
tender.** This file already has a proven answer to "keep a footer always
visible below a region that can grow unboundedly": don't sticky/pin the
footer, make the *growable part* scroll in its own wrapper instead, and
leave the footer as a plain, un-scrolled sibling after it. Applied here:

- `web/ui/pages/index.html`: scan-row + `#held-sales` wrapped in a new
  `.tender-scroll` div.
- `app.css`: `.tender-scroll { flex: 1; min-height: 3rem; overflow-y: auto;
  ... }` (the growable, scrollable region); `.tender-default-footer`
  reverted to a plain flex item (`flex-shrink: 0`, no `margin-top: auto`,
  no `position`/`background`) — it's simply what comes after
  `.tender-scroll` now, so it's never competing with held sales for the
  same box.
- Measured: held-sales matrix (0/1/2/3 held sales, real UI interaction —
  scan, Hold Sale, confirm) at 1024x600 — `tenderScrollHeight`/
  `tenderClientHeight` on the OUTER `.tender` panel are **identical across
  every held-sale count** (213/201 throughout), because growth is now
  fully absorbed inside `.tender-scroll`'s own internal scroll, invisible
  to the outer layout. Payment button hit-tests true at every count.

**A residual ~30px base-case gap remained** even with the scroll split —
not internal waste (the `min-height: 3rem` floor was already trimmed down
to match `.tender-scroll`'s real content need, not borrowed oversized from
`.tab-panel`'s unrelated 6rem floor), but a genuine budget shortfall:
`.tender-default-footer`'s two buttons at their real `min-height: 4.2rem`
need ~131px, and `.tender`'s `3fr` share of a 1024x600 viewport is only
~174px total. Closed with a height-scoped media query (kept, unlike
attempt 1, because the review's own objection to a ratio change was that
it was the *wrong* lever for the held-sales problem specifically — the
held-sales problem is now fixed structurally, and this residual gap
*is* a real space-budget shortfall a ratio change is the correct lever
for):

```css
@media (max-height: 700px) {
  .pos-container { grid-template-rows: minmax(8rem, 5fr) minmax(0, 6fr); }
}
```

Not a bare `1fr:1fr` — that measured only ~0.3px of clearance in one dev
build and **+1.5px clipped** in the actual e2e harness's own built-binary
server (same viewport, same demo seed; the gap is footer-content/font-
metric noise between environments, not a real difference) — nowhere near
enough margin to survive normal environment noise. `5fr:6fr` (tender
~54.5%) measured ~13-20px of real headroom in both environments while
still leaving products the larger share, not a coin-flip split. Threshold
matches the previous review's own measured break-even (~686px), 700
rounds up for a small margin rather than cutting it exactly.

## What I measured (this iteration)

All via real Chromium (Playwright), both against an ad-hoc `go run .` dev
server AND the e2e suite's own `run-till.sh` (built binary, FAQ plugin
installed, matches exactly what CI drives) — the cross-environment check
is what caught the `1fr:1fr` margin being too thin.

| case | clip past `.tender`'s own edge | hit-test |
|---|---|---|
| 1024x600, 0 held sales | −12.9px (clear) | pass |
| 1024x600, 1 held sale | −0.3px (clear) | pass |
| 1024x600, 2 held sales | −0.3px (clear) | pass |
| 1024x600, 3 held sales | −0.3px (clear) | pass |
| 1280x800 | clear, held chips visible, unaffected by the media query (800 > 700) | pass |

`.payment-overlay` (the `<dialog>`) is sized independently of
`.pos-container`'s rows, per #1252's own design — unaffected by any of
this, re-confirmed visually.

## Gates

| gate | result |
|---|---|
| `gofmt -l .` | clean |
| `go build ./...` | clean |
| `go test ./...` | one pre-existing, unrelated sandbox failure (`TestListenWithFallback_WildcardHostFallsBackToLoopback`, confirmed identical on `main` via `git stash` earlier in this session) |
| all 18 `ci.yml` build-job guards | pass, including `guard-docs-shots.sh` (`make docs-shots` re-run after every layout-affecting change) |
| Playwright `--project=default`, full suite | 232/232 passed |
| New regression test | `tender-panel-reachable.spec.ts`: "the default-view Payment button is never clipped at 1024x600, with or without held sales" — holds a real hold-sale-through-the-UI matrix (0→3), directly answering B4 from the second review (no test existed for this class of bug; the existing specs open the overlay first and structurally can't see it) |

## Explicitly deferred, not attempted here

- **900px-width stacked tablet tier** (e.g. 850x700): the Payment button
  clips there too — confirmed **pre-existing** (the second review measured
  the same class of failure at 800x600 independently and found it
  unchanged by that iteration's diff; this tier's `auto`-sized rows have
  no flexible track to redistribute space the way the >900px and <480px
  tiers' own fixes already do, same root cause class as the historical
  ut-docs#413 phone-tier fix). Filed as its own card
  (universaltill/ut-docs#1327) rather than folded in here.
- **Held-strip visibility trade-off**: with several held sales, the strip
  itself can scroll mostly out of the small `.tender-scroll` region at
  1024x600 (the Payment button stays fully visible — that's what this fix
  guarantees — but an operator may need to scroll *within* that small
  region to see every held chip at once). Acceptable under this file's
  own "never invisible, always reachable via scroll" standing pattern; not
  a regression from before #1252 shipped. Not filed as its own card —
  cosmetic, not a reachability problem.
- Everything already tracked from the earlier density-fix work
  (universaltill/ut-docs#1312, #1313, #1314) stands unchanged; #1312
  specifically (held sales starving the tender height budget) is now
  **resolved** by this fix's `.tender-scroll` split and should be closed
  as part of this change landing.

## Naming note

The branch is named `product-tile-density` from an earlier, abandoned
direction on this same card (product-tile density turned out not to be the
actual fix once ut-docs#1252's architecture change was discovered — see
git history on this branch for that dead end, left as-is rather than
rewritten). The diff that actually ships is the Payment-trigger clipping
fix described above; no product-tile CSS changes are included.
