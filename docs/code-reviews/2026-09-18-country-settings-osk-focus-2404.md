# Code review — Country settings edit dialog auto-focuses a locked field, OSK never opens (ut-docs#2404)

- **Date:** 2026-09-18
- **Ticket:** ut-docs#2404 (`complexity:easy`, `bug`, `p3`, `ux`)
- **Branch:** `fix/2404-country-settings-osk-focus`
- **Reviewer:** independent pass, fresh-context Sonnet subagent (per this
  card's `complexity:easy` routing, `MODEL-ROUTING.md`), isolated in its
  own git worktree (cleaned up on completion).
- **Verdict: SAFE TO MERGE.** No findings — no blockers, no should-fix
  items, no nits.

## The bug

`web/public/record-dialog.js`'s `firstField(form)` helper picks the
dialog's auto-focus target on open: the first non-hidden, non-disabled
`input`/`select`/`textarea`. On Country Settings' edit dialog, the page's
own `record-dialog:open` listener (`web/ui/pages/country_settings.html`)
locks the `code` field with `readonly` in edit mode — it's the primary
key, and the save endpoint has no other way to tell "edit this row" from
"create a new one with a different code" (ut-docs#2405 hardens the
server side of the same constraint). `firstField()` didn't exclude
`readonly`, so auto-focus landed on that now-locked field.

`web/public/osk.js` (the custom on-screen keyboard for kiosk
touchscreens with no OS keyboard) correctly refuses to open on a readonly
field — and, separately, by design (ut-docs#155) never opens from
*programmatic* focus at all, only from a real user click/tap. So the
observable symptom wasn't "no keyboard ever" categorically — it was a
**dead first tap**: the field that visually looks focused/ready when the
dialog opens is the locked one, and an operator's natural first tap on it
does nothing, correctly but confusingly. They have to notice and tap a
different field before the keyboard appears at all.

## What shipped

- `web/public/record-dialog.js`: `firstField()`'s selector now also
  excludes `:not([readonly])` on the `input`/`textarea` branches (`select`
  has no `readonly` semantics, so that branch is untouched — confirmed by
  the reviewer, nothing to clean up there). Auto-focus now lands on the
  first genuinely-editable field (Currency, on this page).
- `e2e/tests/country-settings-record-dialog-2187.spec.ts`:
  - Edit-mode test extended to assert Currency (not Code) is focused
    after opening for edit, then simulates the operator's real first tap
    (`.click()` on the now-focused field) and asserts `#osk` becomes
    visible — proving the practical fix (a tap on the auto-focused field
    now actually opens the keyboard), not just the focus target itself.
  - Create-mode test extended to assert Code *is* still auto-focused
    there (it isn't readonly on create) — closes a coverage gap, not a
    regression risk.
  - `setOskMode(page, 'on')` + a describe-level `afterEach` restoring
    `'auto'`: the default Playwright project has no coarse pointer, so
    the OSK's own `auto` mode never activates without this; OSK mode is a
    server-side setting shared across specs on the same server, so the
    unconditional restore (same shape `osk-central-guard.spec.ts` uses)
    matters even on a failed run.

No Go files touched; no i18n/money/schema/plugin-signing impact.

## What the independent review found

**TDD claim independently re-verified, not trusted on the implementer's
word**: reverted `firstField()`'s selector alone, ran the edit-mode test
— red, `toBeFocused()` failed on Currency (`Expected: focused, Received:
inactive`) before ever reaching the click/`#osk` assertion. Restored the
fix, working tree byte-identical to the commit, all 7 tests in the spec
green again (26.0s). Confirmed the test is not a tautology: it fails for
the right reason when the fix is absent, at the line that actually
exercises the fix.

**Cross-page check done independently, not assumed**: grepped every
`record_dialog`/`firstField` adopter — `locations.html`, `registers.html`,
`categories.html`, `inventory.html` use no `readonly` at all;
`fiscal_register.html`'s view-mode uses `.disabled` (already excluded,
confirmed by its own code comment); `catalog.html` has `readonly` fields
but doesn't use `record_dialog.html`/`firstField()` at all. Only Country
Settings is affected, as claimed — no other adopter can regress.

**Fallback-path check**: `open()`'s existing "every field disabled"
fallback (ut-docs#2186, focus stays on the close button rather than
nowhere) is generic on `firstField()` returning `null` for *any* reason —
"every field readonly or disabled" already falls into the same safe path
as "every field disabled." No new all-fields-unfocusable gap introduced.

**Full gate run live**: `gofmt -l .` clean, `go build ./...` clean (no-op,
no `.go` files in diff), `guard-i18n.sh` and `guard-data-access.sh` both
pass. Regression suite — `country-settings-record-dialog-2187`,
`locations-record-dialog-2124`, `registers-record-dialog-2185`,
`categories-record-dialog-2010`, `osk-central-guard` — **51/51 passed**
(52.6s), confirming no regression in the shared `record-dialog.js`/
`osk.js` code path.

**Cleared, checked explicitly by the reviewer**: no secret-shaped
literals or real client/shop names in the diff (grepped, no matches).

## Visual check (Tester pass, independently looked at by this reviewer's briefing)

Screenshots taken at the kiosk floor (1024×600) and phone width (360px)
after opening the edit dialog: Currency field shows a correct visible
focus ring, no overlap/clipping/wrapping, every other control unaffected.
No touch hardware available in this session — verified via emulated
Chromium only, stated explicitly per the `ux` skill's gate.

## Explicitly deferred

None — this is a fully scoped, single-cause fix with no adjacent gaps
found.
