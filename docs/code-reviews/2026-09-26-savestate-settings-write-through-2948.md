# Review: Settings handlers that save through `SaveState` write through to the main till (ut-docs#2948, part 1)

- **Date:** 2026-09-26 · **Lane:** `lane:cloud-24` · **Complexity:** hard
- **Built by:** Opus 5.5 (inline) · **Reviewed by:** Fable (independent subagent, detached worktree)
- **Precedent:** ut-docs#2791 (`2026-09-26-shop-settings-write-through-2791.md`).
- **Split out of #2948:**
  - ut-docs#2982 (found in this review): Settings cards show the generic "Could not save" banner instead of the server's refusal reason.
  - ut-docs#2979: shop-wide writes outside these handlers, including the barcode types, plus a CI guard.
  - ut-docs#2980: `store.country` from an additional till.
  - ut-docs#2981: read-only rendering while the main till is unreachable.

## What shipped

1. **`common.StateKV`** is split out of `common.SaveState`. It returns the same rows with the same window-mode and launch-on-startup read-back. `SaveState` is now `SetMany(StateKV(...))`, so a main till's behaviour does not change.
2. **`saveStateThrough(ctx, d, elev, st, extra)`** (`internal/pages/settings_sync_proxy.go`):
   - **On a main till:** `SaveState`, then `extra` best-effort.
   - **On an additional till:** only the shop-wide rows that `st` changed against the live state travel, together with `extra`, in one `saveShopSettings` batch. Per-till rows are written locally once the main till has accepted.
   - A per-till change never calls the main till.
   - An additional till's stale in-memory copy of a shop-wide value is never pushed over the main till's value.
3. **Twelve `SaveState` call sites in `settings_page.go` rewired:**
   - shop-wide: idle-lock, kiosk-idle-reset, kiosk-payment-mode, allow-negative-inventory, browsing-mode, and the Store card;
   - per-till: window-mode, launch-on-startup, ui-scale, basket-panel-width, osk, theme.
   A failed write-through answers like the generic upsert: 403 forbidden, 409 refused, 502 unreachable. Nothing is written locally and no side effect runs.
4. **Store card (`POST /api/settings/save`) on an additional till:**
   - `store.currency_confirmed` and `store.locale_confirmed` travel in the same batch as the change.
   - An explicit language choice also carries the bumped `store.locale_generation`, published locally only after the main till accepts.
   - `store.country` is refused ("Change this setting on the main till.") before the local fiscal reset runs. Before this change it was saved locally, fiscal reset included, and then reverted by the next pull.
5. **Help:** step 13 of the `multitill` topic, in all five languages, now lists the Store card, lock timeout, kiosk options, selling without stock tracking and the browsing mode among the settings that go through the main till. Only the barcode types and the country remain main-till-only.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `saveStateThrough` diffed against a fresh `d.CurrentState()` instead of the snapshot the handler edited. An admin pull landing between the handler's read and its save made the pulled values look like changes, and the till's pre-pull copies were pushed back over the main till. | **Fixed:** the handlers pass their snapshot (`base`) and the diff runs against it. The new test `SaveStateDiffsAgainstHandlerSnapshot` fails without the fix (`main till store.currency = "GBP", want EUR`) and passes with it. |
| 2 | minor | The additional till's locale-generation read dropped its error. On a store error it sent `1`, which could move the shop's generation backwards. | **Fixed:** the error is logged and the bump is skipped. |
| 3 | minor (UX) | The rewired cards are `hx-swap="none"` forms. On any failure they show only the generic "Could not save — please try again." banner, not the localized reason the handler returns ("Can't reach the main till — …") that help step 13 promises. #2791's cards already behave this way, as does most of the Settings page. | **Filed ut-docs#2982:** one shared after-request helper across the page, which needs its own UX pass and screenshots. The refusal itself is correct: nothing is written and the operator is told the save failed. |
| 4 | nit | On an additional till `StateKV` runs twice, costing four store reads per save. | Accepted: an operator-paced admin screen. |
| 5 | nit | `/api/settings/save` reads `sync.primary_url` twice. | Accepted: consistent in practice. |

The reviewer checked each of the following and found no problem:
- behaviour on a main till, for all 12 handlers;
- that every key `StateKV` emits is classified;
- cases where a change could be missed: the window-mode and launch-on-startup read-back, UI scale, the basket-width reset, OSK;
- side effects on both tills;
- elevation (`elevationCheck{}` only on per-till-only handlers);
- the order of the country refusal;
- the help text in all five languages;
- the manifest hashes.

The reviewer re-verified TDD red in its own worktree: all five fix tests fail with the production files reverted and pass restored.

## Verification

- **TDD red** (before the fix):
  - `SaveStateFieldLandsOnMain`: `main till calls = 0, want 1`.
  - `SaveStateSendsOnlyChangedKeys`: the main till never got the value.
  - `SaveStateUnreachableRefuses`: `204` instead of `502`.
  - `StoreSaveImpliedKeysOneBatch`: `main till calls = 0`.
  - `StoreSaveCountryPointsAtMainTill`: `204`. The country was saved locally.
  - `SaveStatePerTillStaysLocal` passed before and after, as a guard against over-routing.
- **Gate:**
  - `gofmt -l .` empty, `go build ./...`, full `go test ./...` green, `golangci-lint run ./...` 0 issues.
  - Guards pass: data-access, i18n, help-topics, help-drift, docs-shots, kiosk-engine, core-neutral, compliance-claims, competitor-naming, page-http-error, demo-env, readme-local-links.
- **E2E (main-till path unchanged):** 18 of 18 pass across these specs:
  - `sell-screen-browsing-mode-2499`
  - `settings-fee-row-251`
  - `pos-divider-resize-2308`
  - `login`
  - `kiosk-counter-order-held-2703`
- **Screenshots:** no rendered pixel changes. The code is backend only, and the help change is prose. Only `manifest.json` was refreshed: `surface_sha256` via `update-docs-shots-surface-hash.sh`, plus the four `multitill` topic hashes. Docs-Shots-Unchanged: true.
- **Not done:** no two-till browser run. As in #2791, the flow is covered end to end by httptest against the real main-till handler, backed by its own migrated DB. Nothing visible changes on an additional till except the refusal messages that already exist.
- **Gate after the review fixes:** full `go test ./...` green, `golangci-lint` 0 issues, guards pass, `gofmt` clean.
- **Language packs:** no new locale keys, and `ut-plugin-language-es` carries no help topics, so nothing follows in the packs.

## Verdict

Safe to merge. Deferred: ut-docs#2979, #2980, #2981 and #2982.
