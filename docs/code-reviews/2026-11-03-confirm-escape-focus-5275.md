# Review: confirm and PIN prompts — Escape cancels, focus returns, hint cleared (ut-docs#5275)

Branch `fix/5275-confirm-escape-focus`. BA and Architect notes came from
fresh agents, then a Sonnet dev agent built it. A separate Tester agent drove
the real binary in Chromium, a UX §7 gate checked it, and an independent Opus
reviewer reviewed it. The orchestrator applied the review fixes.

## What shipped

- The confirm dialog (`confirm_prompt.html`) and the PIN-elevation dialog
  (`elevation_prompt.html`), plus their `base.html` placeholders, now carry
  `data-ut-escape-close` and `data-ut-prompt`. **Escape** goes through
  base.html's existing non-modal handler (ut-docs#2097). It closes the topmost
  prompt as Cancel and sends no request. The dialogs still open with `.show()`
  over `#ut-scrim`, never `showModal`.
- `web/public/inline-actions.js` does three things:
  - **Opener.** It records the opener at `htmx:beforeRequest`, before autofocus
    moves focus. A re-prompt raised from inside a prompt inherits the opener.
  - **Wiring.** At `htmx:afterRequest` it wires the prompt that arrived. It
    records the hint node only if this response placed it in the request's
    own swap target: a hint that was already there, or one in a
    `hx-swap="none"` form, is never recorded.
  - **Close.** A capture-phase `close` listener handles Cancel and Escape: it
    removes only that hint node and focuses the opener again. It skips a
    terminal answer (`elevation-done` marks `utAnswered`) and a confirm→PIN
    hand-off (`utHandedOff`). A real answer in the target is never touched.
- **Confirm→PIN hand-off.** The confirm dialog now closes instead of staying
  stacked under the PIN prompt.
- **Help.** `web/help/en/elevation.md` says Cancel or Esc backs out. The
  sentence goes inside an existing step, so the structure is unchanged and
  help-drift passes.
- **Tests:**
  - Go markup tests: `dialog_focus_trap_3212_test.go`, `till_role_test.go`,
    `settings_enrol_pair_test.go`.
  - e2e `confirm-dialog-escape-focus-5275.spec.ts` (display mode, worker till)
    and `pair-confirm-escape-5275.spec.ts` (Pair with a shop on a registered
    till, diagnostics project, in-process fake cloud).
  - `e2e/till_setting`: test tooling that sets or deletes one setting through
    `data.NewSettingsRepo`, with no raw SQL.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| T1 (tester) | major (test) | The display-mode spec passed on the old `inline-actions.js` as well: Chromium restores focus natively, and display mode places no hint. | **Fixed**: the pair spec asserts that `#pair-msg .elevation-hint` is gone. Mutation: with HEAD's `inline-actions.js` the pair Escape test fails (1 hint), and with the change it passes 3/3. |
| m1 | minor | In tile edit mode, app.js's `document` keydown handler took Escape (`preventDefault` + exit edit mode) before base.html's `window` handler, so a PIN prompt raised by a tile move stayed open. | **Fixed**: the edit-mode handler returns while any `dialog[data-ut-prompt][open]` is open. Not driven in a browser: it needs the auth project plus a cashier in edit mode (gap below). |
| m2 | minor | `elevation-done` set `utAnswered` on a dialog that was already cancelled mid-request. | **Fixed**: the flag is set only while the dialog is open. The older behaviour stays as it was: a Cancel mid-request still lets the in-flight request finish, and a late re-prompt can re-open. Filed as a Backlog card. |
| m3 | minor | The display-mode spec writes `sync.primary_url` into the shared worker till. If cleanup fails, the till stays a replica for later specs. | **Fixed**: delete with one retry in `afterEach`, plus an `afterAll` sweep. |
| n1 | nit | No 5275 line in `playwright.config.ts`'s per-spec comments. | **Fixed**. |
| n2 | nit | `elevation.md` did not mention Esc. | **Fixed** (en; the other locales follow through translation). |
| n3 | nit | During the confirm→PIN hand-off the scrim can drop for one frame. | Accepted. |
| n4 | nit | Raw-fetch `utPostWithElevation` prompts get no opener or hint handling, and rely on native focus restore. | Accepted: they are not htmx and place no hint. Their own close counts as cancel. |
| n5 | nit | A 4xx/5xx on the prompt form leaves the stale hint. | Accepted: this predates the change. |

## UX §7 gate

PASS with notes:
- Escape=Cancel matches the other till dialogs.
- `osk.js` has no Escape key, so touch users have Cancel.
- Barcode wedges send Enter or Tab, never Escape.
- The self-order kiosk pages don't use this layout.
- No CSS changed, so RTL, dark and small screens are unaffected.
- Screenshot reviewed: the dialog over the scrim, with the status bar and
  rail visible.

## Verified

- `gofmt -l .` is clean, `go build ./...` builds, and `go test ./internal/pages/...` passes.
- Guards pass: no-showmodal, no-inline-handlers, i18n, rtl-logical-css,
  data-access, help-drift, help-topics, compliance-claims.
- Tester: Escape and Cancel send 0 further POSTs on both callers. Focus lands
  on `#pair-code` and the confirm path keeps the server's answer. The status
  bar is not inert: there is no `[inert]` and no `dialog:modal`, and
  `elementFromPoint` hits the bar.

## Gaps

- Focus return is not shown to differ from Chromium's native restore. Only
  hint removal is mutation-proven.
- The wrong-PIN re-prompt path and the tile-edit-mode Escape fix (m1) were
  not driven. Both need the auth project.
- Dark theme, RTL, German and kiosk sizes were checked by reasoning only (no
  CSS change).
