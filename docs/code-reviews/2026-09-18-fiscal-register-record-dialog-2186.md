# Code review — fiscal register adopts the record_dialog/list_header pattern (ut-docs#2186)

- **Date:** 2026-09-18
- **Ticket:** universaltill/ut-docs#2186 (`complexity:medium`, `p2`, `source:user`, `ux`)
- **Branch:** `feat/2186-fiscal-register-record-dialog`
- **Reviewer:** independent pass, Opus subagent (per `complexity:medium`
  routing, `MODEL-ROUTING.md` — Sonnet built it, Opus reviewed it),
  isolated in its own git worktree branched from a WIP snapshot commit.
- **Verdict: SAFE TO MERGE.** Reviewer's initial pass was
  yes-with-fixes-needed (one blocker, four should-fix); all five applied
  and independently re-verified below before this record was written.

## What shipped

Ports `web/ui/pages/fiscal_register.html` + its handler
(`internal/pages/fiscal_register_page.go`) to the shared
list_header/record_dialog UI standard
(`ut-docs/reference/list-and-dialog-pattern.md`), the same pattern
`/locations` and `/categories` already adopted. Full BA requirement,
Architect design and UX-gate additions are recorded as comments on
ut-docs#2186 itself; summary of what makes this screen different from a
mechanical port:

- Fiscal-register entries have **no update endpoint** (only create +
  one-directional decommission) — a row tap opens the same shared
  `record_dialog` markup in a genuinely **read-only view mode**, not an
  edit form. A page-local `<script>` (mirroring `categories.html`'s
  `record-dialog:open`-listener precedent) disables every field and
  hides/disables Save in that mode.
- `web/public/record-dialog.js` — a file three other screens share — got
  one small, additive fix: its final "focus the first field" step now
  falls back to the dialog's Close button when every field is disabled
  (a case that couldn't happen before this card).
- The screen's per-location grouping (heading + each group's own
  untouched inline address-edit form) stays exactly as it was, outside
  the dialog — only the entries list adopts the shared pattern, via one
  `list_header` and one `record_dialog` shared across every group's rows.
- `web/public/app.css` gets a contrast override so a disabled (view-mode)
  field stays fully readable — compliance data (TSE serials, certification
  IDs), not a control the operator isn't meant to notice.
- 6 new locale keys (`fiscalregister.search_placeholder`, `.new`,
  `.view.title`, `.view.note`, `.decommission_confirm`, `common.view`)
  across all 4 core locales (`en`/`ar`/`fa`/`tr`); `web/help/*/fiscal-register.md`
  updated in all 5 shipped locales (`en`/`ar`/`de`/`fa`/`tr`) to describe
  the new popup flow, with regenerated screenshots.

## What the independent review found, and what was fixed

**B1 — blocker, fixed.** `scripts/ci/guard-docs-shots.sh` failed: the
screen's surface hash and all 4 `fiscal-register` topic-markdown hashes
had drifted with no regenerated screenshots — this screen changed more
than almost any other adopter (side-by-side create card → list header +
full-screen dialog) and needed a real `make docs-shots`, not the
surface-hash escape hatch. This sandbox has a pre-installed, resolvable
Chromium (`e2e/scripts/resolve-chromium.sh`), so `make docs-shots` was run
for real, twice (once before, once after the content fixes below, so the
final screenshots reflect the final strings) — 124/124 Playwright
assertions passed each time; `guard-docs-shots.sh` now passes.

**B2 — should-fix, fixed.** The row's tap-to-open control was a pencil
icon labelled `common.edit`/"Edit" — but the dialog it opens is read-only.
Directly contradicted the manual prose this same diff wrote
("that's expected, not a bug"). Added a new `eye` icon to
`internal/httpx/icons.go`'s shared registry and a new `common.view` locale
key (4 locales); the row's control now reads "View", `data-record-edit`
kept unchanged (record-dialog.js keys off the attribute, not the label).

**B3 — should-fix, fixed.** The register `<select>` in the view dialog is
prefilled from `data-field-register_id` against a picker populated from
*active* registers only (`ListRegisters`). A view of an entry whose
register was since deactivated (the normal retire-a-till sequence:
decommission the entry, then deactivate the register) left the `<select>`
at `selectedIndex -1` — silently blank, on exactly the historical entry a
§146a Abs. 4 AO lookup is about. Fixed with a synthetic, view-mode-only
`<option>` sourced from a new `data-field-register_name` row attribute,
added in the `record-dialog:open` handler when `selectedIndex === -1`,
and removed again before the next open so it can never leak a stale
option into the live create-mode picker.

**B4 — should-fix, fixed.** `fiscalregister.empty`'s string ("...use **Add
entry** to record your first till/TSE") pointed at a caption that no
longer exists — New is icon-only `+` now. Confirmed visually via the
regenerated screenshot before fixing. Reworded to "tap + to record your
first till/TSE" in all 4 locales, matching the existing
`locations.empty`/`categories.empty`/`registers.empty` convention.

**B6 — should-fix, fixed.** The shared list-header search only toggles
each `.entry-row`'s own `hidden`; filtering a whole location group's rows
to nothing left that group's heading, its own inline address form, and
its table header stranded on screen above the "no results" line — several
screens' worth of empty chrome at the 360px floor. Fixed with a pure-CSS
`:has()` rule (`.fiscal-register-group:not(:has(.entry-row:not([hidden])))
{ display: none }`) — reacts live to the exact same `hidden` attribute
`record-dialog.js` already sets, no extra JS, no timing/ordering
dependency on the shared filter script.

**Nit, fixed while in the area.** `fiscalregister.view.note`'s English
text said "use Decommission" but the button's actual accessible name is
"Mark decommissioned" — fixed. (The ar/fa/tr translations already
referenced the correct phrase; only English needed the correction.) Also
converted `app.css`'s fix-2 override from an id-selector
(`#fiscal-register-dialog[data-record-mode="edit"]`) to a reusable class
(`.record-dialog--readonly-fields`, toggled by the page script) per the
pattern's own theme/plugin-seam rule — keeps the "readable disabled field"
rule available to a future read-only adopter instead of wiring it to this
one dialog's id. Dropped a dead `list-empty-row` class from the no-results
`<p>` (that rule only ever targeted a `<tr>`; the actual show/hide is the
`hidden` attribute).

**Deliberately not fixed here — B5, noted and deferred.** No e2e spec was
added for this screen (every prior adopter — `/categories`, `/locations`,
`/registers` — shipped one). The client-side view-mode behaviour (the
disable loop, the register-select fallback, the `:has()` group-hide) has
no automated coverage beyond the Go-level slot-pinning test and the
manual reasoning below. Filed as a Backlog follow-up
(universaltill/ut-docs#2403) rather than folded in here — this card
already grew past its original scope fixing five real findings, and
"several honestly-scoped commits beat one commit claiming more than it
verified" (`BUILD-CYCLE.md`).

## Independently re-verified myself (orchestrator), separate from both subagents' own reports

- `gofmt -l .` clean; `go build ./...` clean; `go test ./...` — all
  packages `ok`, 0 failures (run twice: once before the five fixes above,
  once after); `golangci-lint run ./...` — 0 issues.
- `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh`, `guard-data-access.sh`, `guard-page-http-error.sh`,
  `guard-htmx-loaded.sh`, `guard-autofill-suppression.sh`,
  `guard-docs-shots.sh` — all pass on the final tree.
- TDD claim on `TestFiscalRegisterPage_RendersRecordDialogWithFieldsSlot`
  independently re-verified by the Opus reviewer (not just trusted): reverted
  only `fiscal_register.html` to pre-change, re-ran — fails on the real
  assertions (`page has no .list-header` → `Fatalf: page has no
  #fiscal-register-dialog`), not a compile error; restored, passes again.
- Traced `record-dialog.js`'s `open()` in full (reviewer + spot-checked
  independently): the shared focus-fallback fix is unreachable for
  `/categories`/`/locations`/`/registers` (all three still have an
  enabled field in edit mode today), so it changes nothing for them.
- Screenshots visually inspected (not just guard-green): the empty state
  (before and after the B4 fix) and confirmed the layout, search box, `+`
  button and message text render as expected at the standard docs-shots
  viewport.

## Explicitly deferred / follow-ups filed

- universaltill/ut-docs#2403 — add an e2e spec for `/fiscal-register`
  (view-mode field disabling, the register-select fallback, group-hide on
  empty filter), mirroring `categories-record-dialog-2010.spec.ts`'s shape.
- `lang-pack-drift` will show red on `main` after this merges (6 brand-new
  `en.json` keys, nothing for `ut-plugin-language-{de,es}` to translate yet
  — the "merge core first" case per the `reviewer` skill's own note, not a
  mistake). Follow-up PRs against `ut-plugin-language-de`/`-es` owed in
  this same cycle by whichever lane merges this.
