# Review — sell-screen plugin overlay + role-aware ui.* asks (ut-docs#3699)

Date: 2026-10-11 · Card: universaltill/ut-docs#3699 (ADR-0149 §6; ADR-0121
2026-10-11 amendment) · Author: Opus 5.5 (lane:sdk) · Reviewer: Fable,
fresh context, read-only on the working trees.

## What shipped

- **`opens_view` on `button` entries.** `ManifestEntry.OpensView`;
  `validateButtonOpensView` (ParseManifest/verifier, PersistManifest,
  Rollback): button-only, a valid view name, and the view of one of the
  same manifest's `page` entries with a `/plugin/` route outside every
  reserved prefix and no `slot`. Persisted in `config_json` under
  `opens_view`; a `config.opens_view` key is dropped, so only the typed,
  validated field counts. Read back as `data.ButtonEntryRow.OpensView`.
  ut-cloud's `signing.CanonicalEntry` mirrors the field (separate PR).
- **Sell-screen overlay.** `/ui/plugin-buttons` draws `salePluginButtons`:
  an `opens_view` button becomes an `hx-get` of its page route into
  `#plugin-overlay-view`, re-checking the install rule and the plugin's
  `ui:page` grant (skipped and logged otherwise, never drawn as an event
  button). The event endpoint ignores overlay buttons (404). The overlay
  is inline in `base.html` after `#ut-page` (survives htmx navigation),
  non-modal, `data-ut-no-scrim`, closes by hiding (✕, Esc inside it),
  focus to ✕ on open and back to the opener on close; till: inline-end
  top corner with the bug-report panel's tender band reserved; phone: a
  bottom sheet. `servePluginView` answers an `HX-Target:
  plugin-overlay-view` request for a slot-less entry as a body fragment
  (actions, failed-action alert and job poll stay in the overlay; paging
  links off, as in a slot panel).
- **Role-aware asks.** Every `ui.view.ask` / `ui.action.ask` (page, slot
  panel, overlay, and the job an action starts) carries
  `operator: {user_id, role, permissions}` when someone is signed in;
  permissions come from the new `AuthRepo.RoleGrants` (same rows as
  `HasPermission`; `[]` on error — fail closed).
- The AC's `admin.pages` / `reports.panels` gating already existed
  (#3872, #3973); verified unchanged and not bypassable via the overlay.
- Docs: `docs/plugin_guidelines.md`; ut-docs `reference/plugin-views.md`
  (operator payload, "The sell-screen overlay", Not-yet row removed) and
  `reference/plugin-manifest.md` (`opens_view`).

## Findings (Fable)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | A late answer for another view (a job poll, or a slow GET of the previously shown view) could replace the view now open in the overlay. | **Fixed**: `htmx:beforeSwap` guard — only answers for the open button's route swap into the overlay. Verified in a browser (stale route ignored, same route swaps). |
| 2 | minor | Same URL answers differently per `HX-Target`, but only `Vary: HX-Request` was set. | **Fixed**: `Vary: HX-Target` on every view answer; test. |
| 3 | minor | An overlay-targeted request to a slot entry's route got a body fragment under `ui:page` (gate held, no bypass). | **Fixed**: overlay only for `entry.Slot == ""`; test, mutation-checked. |
| 4 | note | New behaviour keyed on a fixed target id; no existing request shape sends it. | No change. |
| 5 | minor | `operator` absent with sign-in off is indistinguishable for a plugin. | **Fixed in docs**: absence means "no role limits" (sign-in off lets everyone do everything), never a cashier. |
| 6 | minor | A refused/failed open left an empty overlay. | **Fixed**: `htmx:responseError`/`sendError` fill the overlay's alert with `plugin.view.unavailable`; verified in a browser (page deleted after the button was drawn). |
| 7 | nit | Comments named a non-existent `plugin_overlay.html`. | **Fixed**. |
| 8 | nit | `75dvh` with no fallback. | **Fixed**: `75vh` line first. |
| 9 | nit | A manifest with `opens_view` signed by an old ut-cloud degrades to an event button (no signature break either way). | Merge/deploy ut-cloud's mirror first. |

## Verified beyond unit tests

- TDD: new tests failed first (compile, then assertions); mutation checks
  by hand — removing the operator injection, the overlay branch, the
  `ui:page` draw check, the event-endpoint exclusion and the slot guard
  each turns its test red; restored green.
- Driven run on a throwaway till (temp data dir, port 8097, seeded
  `test-3699` plugin with no wasm, so its view answers "can't be shown
  right now"), Playwright headless Chromium:
  - 1024×600 (en): button 51 px tall; overlay opens at the inline-end top
    corner, title = button label, ✕ 44×44 and focused; no scrim; the
    tender's Add button is not covered; Esc and ✕ hide it and focus goes
    back to the button; an item rung up by barcode reaches the basket
    (0 → 1) with the overlay open; it stays open across a rail
    navigation to `/menu`. Screenshot looked at: nothing overlaps or
    clips.
  - 360×800 phone (touch emulated): bottom sheet, full width, ✕ 44×44.
    Screenshot looked at.
  - 1024×600 `fa` (RTL): overlay mirrored to the left edge, ✕ at the
    inline end.
  - Not checked: real touch hardware (tablet/Pi), dark theme, a real wasm
    plugin answering a document inside the overlay (no fixture plugin
    exists; the handler tests cover the document path).
- Gate: `go build`, `go vet`, `go test ./...`, golangci-lint v2.14.0
  (0 issues), CI guards (data-access, i18n, core-neutral, no-showmodal,
  compliance, competitor, page-http-error, help-topics, help-drift,
  kiosk-engine, card-data, deadcode, netaccess, plugin-menu-read,
  readme-links, pipefail, demo-env), Playwright: sale-screen, faq,
  bug-report panel (desktop + phone sheet), popup-scrim, phone-popup-fit,
  sale-rail, nav-rail-lock — 73 passed.
- No new locale keys (`common.close`, `plugin.view.unavailable` reused).
  No file writes, no new paths. User manual: no shop-owner-visible change
  until a plugin ships an overlay button; nothing to document in
  `web/help/` yet.

## Deferred

- Pressing an overlay button again re-asks the view, so a job still
  polling in the hidden overlay is replaced by a fresh view. ADR-0121's
  amendment ("shown again") is for the rail button; noted on #4131.

**Verdict: safe to merge.**
