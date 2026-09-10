# Settings: two-pane master-detail + individual-setting search (ut-docs#1960)

## What shipped

The Settings page (`web/ui/pages/settings.html`, 1,774 lines / ~24
sections) becomes a two-pane master-detail surface: a section list on
the inline-start side, the selected section's card on the other, with a
search box above the list that finds an individual setting by its own
(translated) label text, not just a section heading.

**Architecture, deliberately not the `/help` precedent.** The card
suggested reusing `/help`'s per-topic HTMX round-trip
(`internal/pages/help_page.go`), but that would mean splitting
`internal/pages/settings_page.go`'s `registerSettings` — a single
2,270-line function that interleaves ~40 POST handler registrations with
the template's data-gathering — into per-section GET routes. Judged too
risky for a UI-only ask. Instead: every card is still rendered by the
same single server response, in the request's locale, exactly as before;
a page-local vanilla-JS script (appended after `#settings-grid`'s closing
tag) shows/hides the already-rendered cards, builds the nav from the DOM
itself (so it can never drift from which cards a given session/role
actually rendered), and builds the search index by walking each card's
`h2, h3, label, legend, button, .btn` elements. Zero changes to
`internal/pages/settings_page.go` — verified: `git diff main.. --
internal/pages/settings_page.go` is empty. This keeps ADR-0008
(server-rendered HTMX UI, no SPA) intact — there is no fetch, no
client-side routing, one server render; the JS is a progressive-
disclosure affordance over content the server already produced, not a
second render path.

**Behavior:**
- Two panes at tablet+ width; first section selected on arrival so the
  panel is never empty. Section switching is instant client-side
  show/hide (no fetch), left pane keeps scroll position and marks the
  current selection (`aria-current="page"` + `.is-current`).
- Search matches individual settings against the already-rendered,
  already-localized DOM text — works correctly in every shipped locale
  (ar/fa/tr) with no extra i18n engineering, because it's reading the
  same text the operator is looking at.
- Two real inbound deep links preserved: `base.html`'s
  `/settings#registration` and `update_api.go`'s `/settings#android-update`
  (nested inside the "Software update" card, now `id="settings-update"`).
  The pre-existing ut-docs#1534 fix (that card's own inline script,
  unmodified) still runs first in document order and does its
  `scrollIntoView`; the new script re-applies it after hiding the other
  cards, since that reflow can invalidate the earlier scroll position.
- Phone width (`max-width: 40rem`, the same breakpoint `.settings-grid`
  already used) degrades to list-first: no hash → list only, wait for a
  tap; a real deep link → the section shows directly. A **Back to
  sections** control returns to the list. Tablet+ width is unaffected by
  this state.
- RTL: only logical CSS properties, mirroring `.manual`'s existing
  grid-template-columns technique — verified visually in ar and fa (see
  "Verified beyond automated tests" below); zero `left`/`right` literals
  in the new CSS block.

## Independent review (Opus, different model from the Fable implementation)

Full pass in an isolated worktree (`isolation: "worktree"`, per this
pipeline's own guard against mutating a shared checkout mid-review):
`gofmt`, `go build`, full `go test ./...`, `golangci-lint`, and every
CI-blocking guard in `ci.yml`'s build job — all clean (`guard-deadcode-baseline`
fails only for the pre-existing, unrelated GTK/WebKit-headers gap this
sandbox has, per `CLAUDE.md`'s own `cmd/unitill-desktop` note — zero
non-test Go changes in this diff). The reviewer could not execute
Playwright itself (its sandbox blocked the Chromium download and had no
system browser) and said so explicitly rather than skipping silently —
the orchestrator ran the real Playwright suite separately (see below).

The reviewer independently re-verified the TDD claim: reverted
`id="settings-update"` back to a bare `<div class="card">`, confirmed
`TestSettingsEveryCardHasAStableID` and
`TestAndroidUpdateAnchorNestsInsideTheSettingsUpdateCard` both fail with
on-topic errors, restored the fix, confirmed both pass again alongside
the pre-existing (unmodified) `android_update_placement_test.go`. Also
independently rendered both manager and cashier roles through the real
template and parsed the HTML: 24 cards (manager) / 20 (cashier), every
one a direct child of `#settings-grid`, every one with an id, zero
duplicate ids page-wide.

**Findings, and what was done with each:**

| # | Finding | Severity | Outcome |
|---|---|---|---|
| S1 | `web/help/{ar,fa,tr,de}/display.md` were untouched while the matching screenshots were regenerated to the new two-pane layout — a non-English shop owner would see a new-layout screenshot next to old-layout prose. | Should-fix (blocking) | **Fixed** — translated the same paragraph into ar/fa/tr/de, positioned identically to the English addition; `de/display.md` updated even though `web/locales/` has no `de.json` (German UI strings come from the external `ut-plugin-language-de` pack, but the manual's own German content is core and lives in this repo). |
| S2 | `e2e/tests/form-label-layout-300.spec.ts`'s "deliberately horizontal Settings rows stay horizontal" test silently narrowed from measuring the whole page's `.set-row`s (8, across 4 cards) to only 2 cards (`settings-display`, `settings-payments`) after the two-pane rewrite — `settings-update` and `settings-order-no`'s rows dropped out of coverage, invisible because the retained `>= 3` floor still passed at 5. | Should-fix (blocking) | **Fixed** — added `settings-update` and `settings-order-no` to the per-section loop; confirmed each renders a `.set-row` before the fix (grep) and that the full spec still passes after. |
| S3 | The `buildIndex()` fix (stripping `<select>/<option>/<textarea>` and `.help-hint` descendant text so a label wrapping its own dropdown doesn't index as label-glued-to-every-option-text) had no regression test — the existing search assertion used `toContainText` against a button with no nested select, so it would have passed even if the fix regressed. | Recommended | **Fixed** — added an assertion in `settings-two-pane-1960.spec.ts` searching "connection" (the label wrapping `<select name="mode">`) and asserting the hit text is the exact bare label `"Connection"`, not the glued `"Connection Off Network (IP) USB device Regular printer (system / HP, plain text)"` string this bug actually produced during development (caught by the orchestrator's own manual visual check before this review even ran — see below). |
| N2 | The `.settings-hit-target` CSS comment claimed the highlight ring "fades on its own" via `transition: outline-color`, but the JS removes the whole class after ~1.8s (`setTimeout`) — `outline` isn't smoothly animatable that way, so it disappears outright, not fading. | Nit | **Fixed** — corrected the comment and dropped the inert `transition` declaration. |
| N1, N3–N7 | Long search-hit text wrapping to ~6 lines for one label (`settings.printer.kitchen_addr` wraps a `.muted` help sentence `buildIndex` doesn't strip); no `aria-live` announcement on the results/no-results state change; Back button leaves the URL hash so a reload reopens the section instead of the list; a clicked search hit doesn't clear the query back to the section list; `buildIndex()`'s cached element references can go stale after an HTMX swap inside a card (printer discovery, enrol-devices, the raw-settings table) and silently no-op a stale hit's reveal; `toLocaleLowerCase()` with no locale argument uses the browser's locale rather than the page's, which could fold Turkish dotted/dotless I unexpectedly for an exotic query. | Nits | **Accepted as-is, not fixed this cycle** — none affect correctness of the acceptance criteria; each is a real, small UX/robustness polish opportunity for a follow-up, not a defect blocking this feature. Noted here so they aren't lost. |

Also confirmed clean by the reviewer, no fix needed: all 5 new i18n keys
present and correctly alphabetically placed in en/ar/fa/tr; RTL CSS is
100% logical-properties; phone-width `.has-selection` logic matches spec
exactly and has no effect above the breakpoint; the 13 edited (not
newly-created) e2e spec files are narrowly about accommodating the new
shell (opening a section before interacting with a now-hidden control)
and none weaken an existing assertion — `settings-fee-row-251` actually
strengthens its own check; no real client/shop name or secret-shaped
literal anywhere in the diff; OSK still opens on the new search box
(`osk.js` already whitelists `type="search"`); the page degrades
gracefully with JS disabled (every card still renders, just all at once
as before).

## Verified beyond automated tests

- **Real running app**, not just template-string assertions: booted a
  throwaway till (`UT_AUTH=off`, demo catalog seeded) and drove it with
  Playwright, screenshotting and actually reading each result (not just
  asserting text/geometry):
  - Desktop/tablet (1280×800), English: default arrival (first section
    selected, right pane never empty), clicking a nav item switches
    sections instantly, the `/settings#registration` deep link.
  - Search: typing "printer" returns section-attributed hits and no
    longer includes the glued label+options text the S3 fix addresses
    (screenshotted both before and after that fix); clicking a hit
    switches section, scrolls the exact control into view, and applies
    the visible highlight ring.
  - Phone width (375×700): no-hash arrival shows the list only; tapping
    a section reveals the panel with a working **Back to sections**
    button.
  - **RTL, for real**: ar and fa at 1280×800 — the whole shell mirrors
    correctly (nav on the visual right, panel on the left, text
    right-aligned), no leftover LTR artifacts, matching the logical-
    properties-only CSS.
  - **Long-string locale**: tr at 1280×800 — no truncation, clipping, or
    layout break in the nav column despite longer Turkish section names.
  - **Dark theme**: not applicable — this app has no built-in dark theme
    at the core CSS level yet (`web/public/app.css` defines only one
    `:root` palette; "Theme" in Settings selects a theme *plugin*,
    per `ux-guidelines.md`'s "new visual variety is a theme plugin's
    job", not a light/dark toggle). Confirmed by reading the CSS rather
    than assuming.
- **A real regression caught and fixed during this same cycle, before
  the independent review even ran**: the first implementation's search
  index concatenated a `<label>`'s own text with every `<option>` inside
  its nested `<select>` (e.g. "Connection" + the printer mode dropdown's
  three option strings + the charset dropdown's first option string, all
  run together). Found by actually looking at the search-results
  screenshot, not by reading the code. Fixed by stripping
  `select`/`option`/`textarea` (and `.help-hint`) descendants before
  reading an indexed element's `textContent`; re-screenshotted to confirm
  the fix, then given a real regression test (S3 above).
- Full Playwright suite for every spec this diff touches (14 files, 89
  tests: the new `settings-two-pane-1960.spec.ts` plus 13 edited
  existing specs) — all green, run twice (once before the review's
  fixes, once after, since a test-file diff was part of the fix set).
- `make docs-shots` run twice (once for the initial `display.md` prose,
  again after translating it into ar/fa/tr/de) — 112/112 passed both
  times; `guard-docs-shots` confirmed fresh.

## Not verified

- **Real 10.1" pilot tablet** — this card's own acceptance criteria asks
  for it explicitly; unreachable from this cloud sandbox (no physical
  device). Filed as a `blocked:env` follow-up rather than claimed as
  done — see the linked issue's closing comment.
- The independent Opus review's own environment could not run Playwright
  at all (blocked Chromium download, no system browser) — its structural/
  static findings stand, but its own pass did not include a browser run;
  the orchestrator's real Playwright runs (above) are what actually
  exercises the browser-behavior layer for this PR.

## Safe-to-merge verdict

**Yes.** Both blocking findings (S1, S2) fixed and re-verified; the
recommended regression test (S3) added; the architecture's core safety
claim (zero `settings_page.go` diff) independently confirmed; the full
gate (Go build/vet/test/lint/guards) and the full affected Playwright
suite are green. The nits are real but non-blocking polish, listed above
rather than silently dropped.
