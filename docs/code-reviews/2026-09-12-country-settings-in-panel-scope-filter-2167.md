# Country settings: in-panel scope filter (ut-docs#2167)

- **Branch:** `fix/2167-country-settings-in-panel-scope-filter`
- **Card:** universaltill/ut-docs#2167 (`bug`, `p2`, `ux`, `source:user`, `complexity:medium`)
- **Reported by:** the product owner, in conversation, 2026-09-12, on the pilot tablet (v0.14.13)
- **Reviewed:** 2026-09-12, independent Opus subagent in an isolated worktree (card is `complexity:medium` → Sonnet builds, Opus reviews)

## What shipped

`/country-settings` is one of the six `/admin` tree destinations, swapped into `#admin-panel` by htmx since ut-docs#2116. Its two view-scope controls were plain `<a href>` anchors, so following either was a full navigation: the two-pane shell was torn down and the tree's selection lost, to reload the one page already on screen. Same escape class ut-docs#2116 fixed for the tree's own rows; these in-content links were simply not in its scope.

They are now two htmx chips:

- `web/ui/pages/country_settings.html` — everything below `.page-head` wrapped in `#country-settings-view`; the anchors replaced by a `.filter-chips` row of two `<button class="chip" aria-pressed=…>`, each `hx-get`ting the other view with `hx-target="#country-settings-view" hx-swap="outerHTML" hx-select="#country-settings-view" hx-push-url="true"`. Chip vocabulary borrowed from `web/ui/partials/category_filter.html` (ut-docs#2119).
- `web/public/app.css` — the `.category-filter` chip block generalized so every rule carries a `.filter-chips` twin. `.filter-chips` is now the shared name; `.category-filter` is kept only as `/catalog`'s and `/inventory`'s existing call-site class. No second implementation, so the 3rem touch floor, the WCAG 1.4.11 focus ring and the pressed-state checkmark are inherited rather than re-derived.
- `internal/pages/admin_page.go` — new `isAdminPanelSwap(r)` (`HX-Target == "admin-panel"`).
- `internal/pages/country_settings_page.go` — the out-of-band admin-tree write is guarded by it.
- Four new handler tests, one new Playwright spec, the manual updated in five locales, and `make docs-shots` regenerated.

**No i18n keys added or removed**, deliberately: all three `countrysettings.*` scope keys stay referenced (`guard-i18n.sh` fails on orphans), so this card owes **no `ut-plugin-language-{de,es}` follow-up**.

## What the independent review found

Three blockers, all fixed on this branch before merge:

1. **Keyboard focus was destroyed on every toggle.** The chips carry no `id` and live *inside* the element they replace. htmx 1.9.12's post-swap focus restoration only fires for a previously-focused element that has an `id` (it re-finds it via `getElementById` in the swap callback) — without one, a manager who reaches a chip by keyboard and presses Enter has focus dumped to `<body>`, nothing announced, next Tab restarting at the nav rail. The reviewer traced this in the vendored htmx source and noted the repo's own precedent: `web/ui/pages/setup.html:160`, the setup wizard's own "show all countries" control, hand-rolls the same restoration with `$nextTick(...).focus()`. **Fixed:** `id="cs-scope-mine"` / `id="cs-scope-all"`, and the template comment now records why they are not optional.
2. **`role="group"` with no accessible name.** `category_filter.html` carries `role="group"` *with* an `aria-label` — a named UX-gate finding on ut-docs#2119 — because its chips are bare category names. An unnamed group role announces a boundary that says nothing, which is worse than no role. **Fixed:** role dropped (these two chips are self-describing), with a comment saying not to add it back without a label key.
3. **No code-review record.** This file.

One accepted finding fixed opportunistically:

4. **`TestCountrySettings_ScopeFilterPressedStateFollowsView` was whitespace-brittle.** Its first cut matched `aria-pressed="X"` followed by a newline, ten spaces and the chip's `hx-get` — which pinned the right chip but also pinned the template's exact indentation; the reviewer verified that reindenting one attribute line by a single space, changing no behaviour, failed it. Its own comment claimed this "is not brittle", which was wrong. **Fixed:** it now finds each chip's `<button>` tag by `id` with a regex and reads `aria-pressed` out of it. Re-mutated both ways afterwards: inverting the chip's condition fails it; reindenting no longer does.

Deferred to **ut-docs#2178** (filed, Backlog), with the reviewer's reasoning carried over:

5. The guard is applied to 1 of 6 identical call sites (`locations`, `registers`, `translations`, `fiscal_register`, `fiscal_device` still write the tree unconditionally), so six destinations of one shell now answer the same request shape differently.
6. `HX-Target` fails closed into an invisible bug — htmx omits the header entirely when the target has no `id`, so a future `hx-target="closest .card"` would silently drop the tree with no error. A positive marker header (the pattern `X-UT-Admin-Embed` already uses) would be unambiguous.
7. No `aria-live` announcement when the table swaps — a gap shared with `category_filter.html`, so pre-existing rather than introduced here.

Accepted as-is, with reasoning:

- **`countrysettings.showing_mine`** ("Showing settings for your shop's country.") is now somewhat redundant with the pressed chip above it. Kept: removing it would orphan the key and cost a five-locale edit for a cosmetic gain.
- **`countryUnknown`** renders no chips at all. Identical to the pre-change behaviour — the old template rendered no link there either, deliberately, because the handler forces `showAll` in that state and a "back to yours" link would only re-enter the same fallback. Coherent before, coherent now.
- **`web/help/en/users.md`'s own "Show all countries"** mention is the *setup wizard's* country step (`setup.country.show_all`, a different Alpine control on a different screen) — correctly left untouched.

## Verified beyond automated tests

- **Driven in a real browser** (Playwright against a throwaway till), not asserted from rendered HTML: the tree survives the toggle at 1024×600 and keeps `/country-settings` marked current; the standalone page's control works with no shell around it; no console errors in any run.
- **Screenshots taken and looked at** at 1024×600 (kiosk floor), 360px (phone), and RTL `fa` — chips render, the pressed one carries the checkmark as well as the accent colour, the RTL layout mirrors correctly with the tree on the trailing side. Chip height measured at **51px** (above the documented 46px touch floor) in every locale tested.
- **Longest-locale check**: `tr` ("Yalnızca kendi ülkemi göster", the longest of the shipped set at 264px) and `ar` both render with **no horizontal page overflow**.
- **A negative control that failed, and what it changed.** The first e2e spec was checked by reverting the `isAdminPanelSwap` guard — and it stayed **green**. htmx 1.9 does not raise a console error for an out-of-band fragment with no matching target (it removes the fragment and fires `htmx:oobErrorNoTarget`, and `htmx.logger` is never set in this app), so `watchConsole` cannot see it. The spec's header comment now says so explicitly, and the guard's real coverage is named as the Go test rather than implied to be the browser one. The reviewer independently re-derived this from the htmx source and corrected one detail: in the *shell* test the tree element exists, so the OOB swap is genuinely *applied* (repainting an identical tree) rather than discarded — only the standalone half sees the discard.
- **Reviewer's own mutation run:** all four new Go tests were confirmed to kill a distinct mutant (guard→`if true`; wrapper id renamed; chip condition inverted; `hx-select` dropped). No false-pass test found.

## Not checked

- Not run on the **real pilot tablet** — verified in desktop Chromium at the tablet's viewport, which is not the same as real touch hardware. The change adds no `pointer*` handler and no drag gesture, so the touch risk is low, but this is a browser-emulated check and is recorded as such.
- The **German, Turkish, Persian and Arabic** sentences added to the manual were written without a second reviewer (the local translation model was unreachable). The reviewer read them and confirmed each maps to the right Administration-screen label per locale, and flagged the German as stiff but not wrong (`"…daneben stehen"` would read better as `"…daneben erhalten"`).
- `ut-plugin-language-de`'s own `menu.group.administration` wording was not cross-checked against the German manual sentence — that repo is not part of this checkout.

## Gate

`gofmt -l .` clean · `go build ./...` · `go vet ./...` · **`go test ./...` green (full suite)** · `golangci-lint run ./internal/pages/...` 0 issues · `guard-i18n.sh` ✓ (1653 keys, all locales match) · `guard-help-topics.sh` ✓ · `guard-help-drift.sh` ✓ · `guard-htmx-loaded.sh` ✓ · `guard-docs-shots.sh` ✓ · e2e `admin-country-scope-filter-2167.spec.ts` 2/2 passing, re-run after the review fixes.

The post-review edits (two `id` attributes, one removed `role`, comments) alter no rendered pixel, so the surface hash was refreshed with `scripts/ci/update-docs-shots-surface-hash.sh` rather than a second full `make docs-shots` — see the `Docs-Shots-Unchanged: true` trailer on that commit.

## Verdict

**Safe to merge.** The reviewer's verdict was "not safe as-is, but narrowly so" against three cheap blockers, all now fixed and re-verified. It rests on: a green full test suite plus five mutation runs proving the new tests are real and complementary; every CI guard green; a source-level read of htmx 1.9.12's swap path confirming OOB-is-processed-before-`hx-select` (which is *why* the suppression has to be server-side) and no duplicate-id path; and confirmation that no shipped JS binds per-element inside the swapped subtree, so the on-screen keyboard still reaches swapped-in inputs.
