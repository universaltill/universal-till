# Code review: sale-screen nav rail icon consistency — ut-docs#1348

**Branch:** `fix/1348-nav-rail-icon-consistency` · **Reviewer:** independent
fresh-context Sonnet subagent (complexity:easy → fresh-context review per
the `scrum-master` skill's model-routing table)

## Change

Two related visual defects in the sale-screen left icon rail
(`web/ui/partials/nav.html`'s `.nav`), both reported live on a real tablet
by the product owner:

1. **`?` help link left-aligned and undersized.** It kept the bare
   `.help-hint` markup — a small, deliberately low-contrast outlined circle
   meant to sit next to a page heading elsewhere in the app (`helpLink`
   template func, `internal/httpx/httpx.go`, used across settings.html,
   permissions.html, fiscal_register.html, etc.) — instead of the
   `.nav-toggle`/`.nav-toggle-ico` button structure every other rail icon
   uses. Root cause: `.nav-right` is a column flexbox with
   `align-items: stretch`; stretch only centers an item that has no fixed
   cross-axis size of its own. The previous `.nav .help-hint` override gave
   it a fixed `inline-size: 1.6rem`, which defeats stretch and falls back
   to `flex-start` — i.e. pinned to the rail's start edge instead of
   centered, exactly the reported symptom.
   - Fix: `nav.html`'s help link now uses `class="nav-toggle"` with an
     inner `<span class="nav-toggle-ico">` + `<span class="nav-toggle-label">`,
     identical structure to every sibling (till/menu/inventory/bugreport/
     profile/lock). It now stretches to the rail's full width like its
     siblings and centers its own content via `.nav-toggle`'s own
     `align-items: center; justify-content: center`.
   - The now-dead `.nav .help-hint { … }` override (app.css) is removed.
     `.help-hint`'s own base rule (small outlined circle, `border-radius:
     50%`) is untouched — still used verbatim by every `helpLink` call site
     outside the rail.
2. **Bug-report glyph (🐞) undersized vs. profile (👤).** `.ico-boost`
   (`font-size: 1.55rem`, vs. `.nav-toggle-ico`'s base `1.25rem`) already
   exists precisely for this class of problem — different emoji have
   different intrinsic ink coverage at the same font-size, documented in
   app.css's own comment and already applied to ☰/♻️/🔒. 🐞 just wasn't
   caught in that first pass. `web/ui/partials/bugreport_chip.html`'s icon
   span now carries `ico-boost` alongside `nav-toggle-ico`. `session_chip.html`'s
   lock (🔒) already had `ico-boost`; profile (👤) doesn't need it — visually
   confirmed close to full-box already (see Verification).

## Verification

- **Live visual confirmation** (same method the codebase's own comments
  document the product owner using): built and ran the app locally,
  screenshotted `.nav` before/after at 1024x600 via Playwright + the
  pre-installed Chromium. Before: `?` renders as a small outlined circle
  pinned to the rail's start edge; 🐞 visibly smaller than the surrounding
  rounded-square buttons. After: `?` renders as a full centered
  rounded-square button matching its siblings; 🐞 fills its box the same
  way 🔒 does. Also spot-checked a real manual screenshot
  (`web/help/img/en/sell.png`, post-regen) showing the fixed rail in
  context.
  Isolated glyph comparison (profile / bug-no-boost / bug-boost /
  lock-boost / help side by side, same CSS) confirmed bug-report needed
  the boost and profile didn't.
- **New e2e regression coverage**
  (`e2e/tests/nav-rail-icon-consistency-1348.spec.ts`, 3 tests):
  the help link is `.nav-toggle` not `.help-hint`; its horizontal center
  matches an ordinary sibling's within 3px; the bug-report icon span
  carries `ico-boost`. **TDD claim independently re-verified**: stashed the
  three source files (nav.html, bugreport_chip.html, app.css) and reran —
  all 3 fail red against the pre-fix code with the expected assertion
  messages; restored and reran — all 3 green.
- Full regression sweep: `phone-width-layout-413.spec.ts` (44 tests across
  it + bugreport-panel/manual/this file) — all pass, including the
  existing `.nav .nav-toggle` label-visibility assertion at 360px, which
  now also covers the help link for free since it's a real `.nav-toggle`.
- `osk-central-guard.spec.ts` unaffected (different subsystem) — not rerun
  here; covered by universal-till#672's own review.
- `gofmt -l .` clean, `go build ./...` clean, full `go test ./...` clean —
  **after** fixing the regression the independent review caught (see
  below); this branch's CSS/markup change broke one existing Go test that
  string-matched a CSS class name.
- All 16 CI-blocking guards (`guard-data-access.sh` … `guard-makefile-version.sh`)
  pass. `guard-docs-shots.sh`: `nav.html`/`app.css` are app-surface, so
  `make docs-shots` was rerun (92 screenshots regenerated); only 4 files
  redrew for reasons unrelated to this fix (sub-pixel AA noise from an
  unrelated earlier rerun) plus all 92 legitimately show the fixed rail
  now that it renders on every page.
- `web.locales/en.json`'s `help.open` key is pre-existing (unchanged by
  this diff) — `guard-i18n.sh` passing confirms no drift.

## Independent review (fresh-context Sonnet subagent)

**One blocker found, fixed same-session:**

`internal/pages/help_hint_test.go`'s `TestHelpHintResolvesPerPage` matched
the nav's help link with a regex anchored on the literal string
`class="help-hint" href="…"`. This diff's whole point is to rename that
link's class to `nav-toggle` — so the regex matched nothing on `/catalog`
and the test failed (`"no help hint rendered on /catalog"`), reproducibly,
not a flake. `go test ./...` was in fact red against the first version of
this diff, contradicting an earlier draft of this record's verification
claim.

Fix: decoupled the test from the CSS class. It now matches the whole
opening `<a>` tag anchored on the stable `data-testid="help-hint"`
attribute (present in both the old and new markup, order-independent),
then extracts `href` from within that tag — so it no longer cares which
class the link carries, only that a help-hint tag with the right `href`
exists. Reproduced red on the unfixed regex, green after the fix; full
`go test ./...` (42 packages, 0 failures) and the two targeted Playwright
suites re-ran clean afterward.

**One non-blocking nit found, fixed same-session:** the new e2e centering
test called `.boundingBox()` without a prior `toBeVisible()` — low risk in
practice (the elements are always rendered at this viewport) but a
`TypeError` on `null` is a worse failure mode than a Playwright assertion.
Added `expect(...).toBeVisible()` for both locators before reading their
boxes.

Everything else the reviewer checked came back clean: the flexbox
root-cause reasoning (`align-items: stretch` degrading to `flex-start` for
a fixed-size child) verified correct by independent inspection;
`.help-hint`'s base rule and its other call sites (`helpLink`,
settings.html, permissions.html, etc.) untouched and still passing their
own tests; phone-width fallback (`≤480px`) correctly inherited via the
shared `.nav-toggle` classes; `help.open` i18n key pre-existing in all
four locales; accessible name unchanged (title/aria-label stayed on the
outer `<a>`); `#bugreport-toggle .nav-toggle-ico` selector correct; no
dangling references to the removed `.nav .help-hint` override anywhere
else in the codebase.

## Risk

Minimal — pure CSS-class/markup restructuring reusing an existing,
already-proven button pattern; no Go code, no data access, no new
strings. The one shared file (`app.css`) only loses a now-dead override
rule; nothing else references it.
