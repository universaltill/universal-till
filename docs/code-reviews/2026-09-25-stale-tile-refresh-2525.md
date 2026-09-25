# Review: stale sale-screen tile refreshes the grid (ut-docs#2525)

**Branch:** `fix/2525-stale-tile-refresh` · **Author lane:** lane:cloud-54 (Opus 5.5) · **Reviewer:** independent Fable subagent

## What shipped
A sale-screen tile is a snapshot of the catalog from when the grid last
rendered. The card's suggested fix doesn't work: emitting `buttons-changed`
from the catalog handlers can't reach an open sale screen, because
`HX-Trigger` only reaches the requesting document and catalog editing happens
on `/catalog`. So this change makes staleness heal itself on the first tap:
- Tiles (`product-tile`, `product-tile-result`) send `src=tile`.
- `POST /api/pos/scan`: when every resolution path misses and `src=tile` is
  set, the basket shows an info toast `pos.toast.tile_stale_refreshed` and
  sends `HX-Trigger: buttons-changed`, so the grid re-fetches. Manual entry,
  the suggestion strip and camera identify keep "Item not found".
- `GET /ui/pos/modifiers`: a stale tile gets 200 with
  `HX-Retarget: #basket`, `HX-Reswap: outerHTML`, the same toast and
  `buttons-changed`, instead of a plain-text 404.
- The picker opens only if the swap really landed in `#modifier-modal`.
  Before this, a stale modifier tile opened the picker still holding the
  previous item's options. `app.js`'s order-type-prompt path empties the
  closed modal before re-issuing the GET.
- New key in en/ar/fa/tr, plus `ut-plugin-language-{de,es}` PRs. Help prose
  added in `web/help/*/sell.md`, and docs-shots regenerated.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | The help sentence sits in the "every active item" paragraph in en, but in the press-and-hold paragraph in ar/de/fa/tr. Those translations never got the #2541 paragraph (pre-existing gap). | Accepted. Structure guard passes and readers are unaffected. |
| 2 | nit (pre-existing) | `pos_api.go` scan-cache fast path (`HasScanCache`/`HasLine`): if an item was already rung up in this sale and then deactivated, its tile still adds from the cache and never reaches the new branch. | Accepted, out of scope. The item was active when it was cached. |

The reviewer also checked and found safe:
- `src=tile` can't change what resolves or what it costs.
- The retarget is gated on the marker.
- No other caller depends on the old literals.
- In htmx 1.9.12, `event.detail.target` is the retargeted element during `afterRequest`.
- Jiggle mode and the Designer grid are unaffected.
- Kiosk never renders these templates.
- Translations use each locale's established terms.

## Verified
- TDD: both stale-tile Go tests fail with the Go handler fix reverted and pass once it is restored. I did this and so did the reviewer, independently.
- `buttons_variant_test.go`'s new `src=tile` assertion fails with the template marker removed.
- The e2e `e2e/tests/sell-stale-tile-2525.spec.ts` (2 tests) passes against a real till. I reverted only the tile's after-request guard and the modifier test failed (`#modifier-modal` visible), so the guard is proven.
- Full gate:
  - gofmt, build, vet, `golangci-lint` (0 issues) and full `go test ./...` all green.
  - Every `ci.yml` guard passes after `make docs-shots`, except `guard-shellcheck-version`: no shellcheck binary in this container, and no shell scripts changed.
- Visual check, looked at: sale screen with the toast at 1024×600 in en, fa (RTL) and tr, plus 360px en. The toast reads cleanly, RTL mirrors correctly, and the refreshed grid no longer shows the tile. The refreshed grid returns to the first category tab, which is existing behaviour for every `buttons-changed` refresh.
- Not checked: real touch hardware. Tap only, no gesture change. At 360px the basket toast sits above the scrolled viewport, the same as every existing basket toast there.

## Deferred
- ut-docs#2765: push catalog changes to an already-open sale screen across devices, tabs and sync.

## Verdict
Safe to merge.
