# Code review: OSK toggle for autofocused dialogs (ut-docs#1048)

**Branch:** `fix/1048-osk-toggle-autofocus-dialogs` · **Complexity:** easy ·
**Dev:** Sonnet (inline) · **Review:** Sonnet, fresh-context subagent
(isolated worktree), per the easy-tier review-model routing.

## What shipped

ut-docs#1022 suppressed the native on-screen keyboard fallback everywhere
the custom OSK is active. That left 3 autofocused dialogs — the hold-sale
naming dialog (`#hold-label-input`), the manager-override elevation prompt
(`override_pin`), and the change-PIN screen (`current_pin`) — with no way
to open a keyboard at all on load: per ut-docs#155, the custom OSK never
auto-opens on programmatic focus, only a deliberate tap or a
`data-osk-toggle` button does.

Product owner's answer (2026-08-27, ut-docs#1048 comment): "go with
option 1 — Add the ⌨️ toggle button to all three dialogs... matching the
existing sale-screen pattern. No change to ut-docs#155's standing
no-auto-open rule."

Implementation adds one `data-osk-toggle` button — byte-for-byte the same
markup as the existing sale-screen scan-row toggle
(`web/ui/pages/index.html`'s `.scan-row`) — to each of the 3 dialogs:

- `web/ui/pages/index.html` — inside `#hold-modal`'s form, after
  `#hold-label-input`
- `web/ui/pages/pin.html` — after the `current_pin` field's `<label>`
- `web/ui/partials/elevation_prompt.html` — after the `override_pin`
  field's `<label>`

Reuses the existing `osk.toggle` i18n key and `.osk-toggle` CSS class
verbatim — no new locale key, no CSS change, no `web/public/osk.js`
change. `osk.js`'s existing toggle-click handler and `updateToggles()`
already handle any `[data-osk-toggle]` button generically.

## Independent review — findings

**Blocking:** none. **Should-fix:** none.

The review traced `osk.js`'s actual toggle-targeting logic (walks
`t.form.elements`, targets the *first* OSK-able field found) against all
three dialogs specifically looking for the placement bug this pattern
invites — a toggle sitting in the wrong `<form>`, or a multi-field form
where it silently targets the wrong field. All three are correct:
`#hold-modal` and the elevation dialog each have exactly one OSK-able
field; `pin.html`'s form has three password fields, but the toggle always
resolves to the first (`current_pin`) regardless of the button's own
position in the DOM — which is exactly the field that needed it, since
`new_pin`/`new_pin2` are never autofocused and already open the OSK on a
direct tap via the existing click-handler path.

## Verified beyond automated claims

- **TDD re-verified independently**, not taken on trust: the reviewer
  reverted just the three template files against `main` (keeping the new
  tests), re-ran `TestElevationPrompt_CarriesOSKToggleForOverridePIN`,
  confirmed a real assertion failure (not a compile error), restored the
  templates, confirmed green again. The same red→green cycle was proven
  a second time during Dev/Tester for both `TestElevationPrompt_
  CarriesOSKToggleForOverridePIN` and the hold-modal e2e test.
- **Real driven app, not just rendered-HTML assertions**: e2e run for
  real via Playwright against the pre-installed chromium —
  `settings-osk.spec.ts` 6/6 (default project, including two pre-existing
  tests whose `[data-osk-toggle]` selectors needed scoping to
  `.scan-row`/`#hold-modal` now that a second toggle exists on the page —
  a real, necessary fix, not scope creep), `login.spec.ts` 14/14 (auth
  project, full serial first-boot/PIN flow intact), plus a regression
  pass on `hold-named-tab.spec.ts` + `form-label-layout-300.spec.ts`
  (9/9) since hold-modal/pin.html markup changed.
- **Visual check, actually looked at**: real screenshots taken and read
  for the hold-modal at light theme (1280×800), kiosk size (1024×600),
  and RTL/Farsi — toggle button renders cleanly below the field in all
  three, no overlap or clipping, correctly mirrors to the inline-start
  edge in RTL. Same for `pin.html` — toggle visible, and the numeric OSK
  panel opens correctly targeting `current_pin` on click.
- **Accepted, explicitly-flagged gap**: the elevation dialog
  (`elevation_prompt.html`) has no existing e2e harness reaching it (zero
  specs touch `#elevation-modal` — it needs a non-manager operator
  attempting a manager-gated action, e.g. till promotion or EOD, which no
  spec currently drives) and building one from scratch was judged
  disproportionate for an easy-tier, product-approved micro-affordance
  fix. Verified instead via a Go-level HTML-tree-parsed render test
  proving the toggle sits inside the correct `<form>` with the right
  classes/attributes, plus its markup being byte-for-byte identical to
  the two dialogs that *were* screenshot-verified, through the same
  generic `.osk-toggle` / `label:has(> input)` CSS rules. Independent
  review sanity-checked and accepted this judgment rather than treating
  it as a silently-skipped gap.
- Gates: `gofmt -l .` clean, `go build ./...` / `go vet ./...` clean,
  full `go test ./internal/pages/...` green, `guard-i18n.sh` /
  `guard-data-access.sh` / `guard-htmx-loaded.sh` /
  `guard-autofill-suppression.sh` / `guard-compliance-claims.sh` /
  `guard-help-topics.sh` all pass. No new locale key (confirmed
  `web/locales/` untouched in the diff), no new CSS, `osk.js` untouched
  (confirmed via diff stat) — ut-docs#155's no-auto-open logic lives
  entirely there and was not touched.
- Manual (`web/help/`): no update judged necessary — a micro-affordance
  on an already-documented OSK mechanism (`web/help/en/display.md`
  already describes the OSK generically), matching the fact the original
  scan-row toggle never got its own manual callout either.
  `guard-help-topics.sh` passes cleanly (no new routes).

## Verdict

**Safe to merge.** No client/shop-name or secret-shaped literals in the
diff (pure template/test markup). Matches the product-owner-approved
scope exactly — 3 named dialogs, no change to `osk.js`'s no-auto-open
rule.
