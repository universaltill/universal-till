# Review — Bluetooth devices + barcode backfill swap a region instead of reloading (ut-docs#2762, slice 2)

- Date: 2026-09-26
- Branch: `feat/2762-bluetooth-backfill-swap`
- Author: Opus 5.5 (lane:cloud-54). Reviewer: Fable (independent, different model).

## What shipped
- `/bluetooth-devices`: Pair and Forget used to call `window.location.reload()`.
  They now re-render only the paired-devices card (`#bt-paired`,
  `data-ut-refresh`) through `UT.refreshRegion` (from slice 1). The Forget
  listener is delegated from `#bt-layout`, so buttons that arrive with a
  refreshed card work. A paired device is removed from the scan results.
  Forget's confirmation survives the swap (`#bt-paired-msg`, aria-live).
- Catalog barcode backfill: the result dialog's Close used to reload the
  page. It now refreshes only `#catalog-table`, via `data-ut-refresh` on
  `#barcode-backfill-modal`.
- `UT.refreshRegion` (app.js) dispatches a bubbling `ut:region-refreshed` on
  the swapped-in element, so catalog.html re-applies its search filter to
  the fresh grid (`htmx:afterSwap` never fires for a non-htmx swap).
- The help prose in `web/help/{en,de,ar,fa,tr}/bluetooth-devices.md` no
  longer says "the page refreshes". The docs-shots manifest has its surface
  hash and those topic hashes refreshed (see Verification).

## Findings (Fable)
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | low | `list_error` (`errKey`) was rendered outside `#bt-paired`, so a failed list read during a refresh showed "No devices paired" with no error. | **Fixed**: the error `<p>` moved inside the card. It is the only `errKey` value and it concerns the list. |
| 2 | low | catalog.html's body listeners stack up on each `/items` rail revisit, because the IIFE re-runs. | Accepted. The result is still correct, and the `htmx:afterSwap`/`oobAfterSwap` listeners on the same page already have this pre-existing pattern. |
| 3 | low | Adapter/permission notices and the scan button's disabled state sit outside the region, so they are not re-evaluated after Pair/Forget. A reload used to refresh them. | Accepted. Pair/Forget do not change adapter state, and the next visit re-renders them. |
| 4 | low | A second Forget in flight while the first refresh swaps the card acts on a detached button. | Accepted. The state converges. Same as slice-1 finding 4. |
| 5 | nit | A comment line overran the wrap. | Fixed. |

## Verification
- TDD: `TestBluetoothBackfillSwapRegionNotReload` (Go) and the e2e specs
  `targeted-swap-bluetooth-2762.spec.ts` and `catalog-barcode-backfill-1356.spec.ts`
  (now asserts no reload) were run against the templates reverted to
  `origin/main`. All of them FAIL there, and they pass on the branch. Both
  the author and the reviewer checked this independently.
- The e2e runner has no bluetoothd, so the Bluetooth spec stubs the scan,
  pair and forget JSON endpoints with `page.route`. The refresh itself is a
  real page GET, and the Forget button is planted after load, which tests
  the delegation. Real pairing still needs hardware (the existing
  ut-docs#76 gap).
- Docs-shots: `make docs-shots` ran in this container on `origin/main` and
  on the branch, and every PNG was byte-identical. This container's
  renderer differs from the one that took the committed PNGs, so only the
  manifest hashes were refreshed (`Docs-Shots-Unchanged: true`). The
  review-fix move of the error `<p>` only renders when the list read fails,
  and the committed screenshot shows no such error.
- `/items`: the shell embeds the catalog panel, and the rail pushes
  `/catalog`, so a plain GET at either URL contains `#catalog-table`. No
  reload fallback in practice.
- Gate: `gofmt -l .` clean, `go build ./...`, `go test ./...`,
  `golangci-lint run ./...` (0 issues), and every `build`-job guard ran
  (shellcheck is not installed locally, and no `.sh` changed).

## Verdict
Safe to merge.

## Deferred (still on ut-docs#2762)
The plugin screens, `settings.html`, the server `HX-Refresh` responses
(`pairing_api.go`, `backup_api.go`, `sync_api.go`) and the item editor's
Details and Variants saves.
