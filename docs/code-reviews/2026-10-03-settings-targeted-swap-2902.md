# Review: Settings saves swap their own region instead of reloading (ut-docs#2902)

- Date: 2026-10-03
- Branch: `feat/2902-settings-targeted-swap`
- Author: Opus 5.5 (lane:cloud-54). Reviewer: Fable (independent, different model).

## What shipped
- `web/ui/pages/settings.html`: eight forms only change their own card. On success they no longer
  run `reload:<reason>`. Instead they run the dispatcher's `refresh-region` step (`UT.refreshRegion`)
  behind the existing `not-html` guard, so an elevation prompt still never triggers it. The eight
  forms are basket-panel reset, report retention, printer, till name, till register, invoice,
  telemetry and store name.
- Each form gets its own region, `<div id="settings-…-region" data-ut-refresh=… data-ut-saved-msg=…>`.
  The region always sits **inside** its `.card`, never on it, because the section switcher holds
  references to the card elements.
- These forms keep the full reload, because they change the shell:
  - theme, ui-scale, effects-level and osk restyle it;
  - currency, language and staff-languages re-format or re-label it;
  - idle-lock is read once from `<body data-idle-lock>`;
  - auto-register: opting in registers the till now, which removes the status bar's "Register till" chip. This one came from the review.
- Printer LAN discovery is now a click listener delegated from `#settings-printer`, with the elements
  looked up per click. The button sits inside the swapped region, so a once-bound listener would be lost.
- The settings switcher listens for `ut:region-refreshed`. On that event it:
  - clears its search index, so the index never points at detached nodes;
  - shows the existing "Saved." notice (`settings.display.change_saved`) in the region's
    `data-ut-saved-msg` span. A swap re-renders identical content, so the notice is the acknowledgement.
- No new strings and no CSS changes. `make docs-shots` regenerated no PNG, so only the manifest's
  surface hash changed.

## Findings (Fable)
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | Auto-register opt-in registers now. The status bar's `.sb-enrol` chip is static, so a region swap left "Register till" showing. | **Fixed**: that form keeps `reload:settings-auto-register`. The Go guard pins it in the keep-reload table. |
| 2 | major (UX) | The plain 204 save path became silent, because the region re-renders identical content. | **Fixed**: a "Saved." notice in each region's message span, via `ut:region-refreshed`. It reuses the existing key. Basket reset got an empty `aria-live` span for it. e2e asserts the notice. |
| 3 | minor | The settings search index held detached elements after a swap, so a hit would not scroll or highlight. | **Fixed**: `index = null` on `ut:region-refreshed`. |
| 4 | nit | Some comments still described a reload. | Fixed. |
| 5 | nit | On the old template, the new spec fails by a 45s timeout rather than a fast assertion. | Accepted. It still fails as it should. |
| 6 | pre-existing | Report-retention's elevated success sends no `X-UT-Response: ok`, so `elevation-done` never refreshes the region. Same on `main`. | Out of scope. Filed as a Backlog card. |

## Verification
- TDD: `TestSettingsSwapRegionNotReload` and `e2e/tests/targeted-swap-settings-2902.spec.ts` both
  fail against `settings.html` reverted to `origin/main`, and pass on the branch. The author and the
  reviewer each checked this independently.
- The e2e spec runs a real till for each case, and checks three things: a `window` marker survives
  (no reload), a stale attribute on the old form is gone (the region really was swapped), and the
  new value and "Saved." notice show. The section switcher still shows the card afterwards. Printer
  discovery still works after a swap; the discovery endpoint is stubbed.
- Related specs re-run and passing: users/shifts and bluetooth swap specs, store-name-3115,
  printer-discovery-http-error-1556, settings-two-pane-1960, settings-categories-3090,
  tills-pairing-layout-1548, manual, htmx-admin-error-swap-916 and pos-divider-resize-2308. The
  pos-divider spec waited for the old page reload and was updated to wait for the in-place notice.
- `go build ./...`, `go vet`, full `go test ./...`, gofmt, and every guard in `ci.yml`'s build job
  pass. Three guards fail only because of this container, not the change: deadcode-baseline (no
  GTK headers, so `cmd/unitill-desktop` is skipped; the reported functions are in untouched
  `internal/logging`) and shellcheck-version (no binary).
- Visual: the invoice, printer, tills and registration cards were looked at after a save, at
  1024×600 and 360×800 (light theme, en). Nothing overlapped or clipped. docs-shots regenerated
  byte-identical PNGs for all 30 topics × 4 locales, so the layout is pixel-identical. Not checked
  on real touch hardware; the change involves no touch gestures.

## Verdict
Safe to merge.
