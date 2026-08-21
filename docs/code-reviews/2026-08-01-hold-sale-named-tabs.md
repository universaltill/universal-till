# 2026-08-01 — Named tabs for Hold Sale (ut-docs#46)

## Context
"Named open tabs / open receipts (Offene Belege)" asked for the ability to
label a held sale ("Haaft 1", "Table 4") the way the reference café POS
does. The underlying mechanism already existed in full — `held_sales.label`
(schema, `HeldSalesRepo`, the held-sales strip) already accepted and
rendered arbitrary text, and defaulted to the attached CRM customer name or
a bare `HH:MM` timestamp — but nothing in the UI let the cashier type one.
This change adds that missing naming step; it does not touch persistence,
which was already correct.

## Design
- `POST /api/pos/hold` (`internal/pages/hold_api.go`) now calls
  `r.ParseForm()` and reads an optional `label` field, taking it ahead of
  the existing `CustomerName` → timestamp fallback chain.
- `web/ui/pages/index.html`: a new `<dialog id="hold-modal">` with a text
  input (`#hold-label-input`) and Hold/Cancel actions; the existing "Hold
  Sale" button opens it instead of posting directly.
- Two new i18n keys (`hold.modal.title`, `hold.modal.placeholder`) added to
  all four locales (en/ar/fa/tr); `hold.action`/`common.cancel` reused for
  the dialog's own buttons.
- No DB migration, no money-typed values involved, not jurisdiction-specific
  (stays in core, not a plugin). No ADR — this extends an already-designed
  mechanism with a UI affordance, not a new cross-cutting decision.

## Independent review (Opus, adversarial brief)
Found 1 blocking issue and 4 non-blocking ones, all fixed here:

- **Blocking — the on-screen keyboard was inert behind the dialog.** The
  first draft opened the dialog with `showModal()`, which puts it in the
  top layer and makes everything else in the document inert per spec —
  including `#osk`, the custom on-screen keyboard kiosk Pis depend on
  (they have no OS keyboard). A cashier on real target hardware would see
  a focused text field and a dimmed, unclickable keyboard: the feature
  would have been unusable on the primary target device. Not caught by the
  Go tests (handler-level, no DOM) or the first e2e draft (desktop
  Chromium, no touch → OSK never engages). **Fixed**: the dialog now opens
  via `.show()` (non-modal), with explicit CSS (`#hold-modal { position:
  fixed; inset-block-start: 8vh; … }`) to anchor it near the top of the
  viewport instead of relying on `:modal`'s UA centering, so it also never
  sits under the OSK's fixed 15.5rem bottom panel. Added a regression e2e
  assertion (`document.body.inert` must stay `false` while the dialog is
  open) since this class of bug is otherwise invisible to a non-touch
  browser test.
- **Non-blocking — no length cap on the label.** Previously the label was
  always server-derived; this change made it free text for the first time,
  and it round-trips into the held-sales strip HTML on every sale-screen
  load until resumed. Live-probed by the reviewer: a 200 KB label was
  accepted and persisted. **Fixed**: `maxHoldLabelRunes = 64`, enforced
  server-side with a rune-safe (not byte-slicing) truncation helper, plus
  a client-side `maxlength="64"` as a soft affordance. New test
  (`TestHoldHandler_LabelTruncatedAtMaxRunes`, using a multi-byte rune to
  actually exercise the rune-vs-byte distinction) confirmed failing
  (panicked on a byte-slice of a multi-byte string) before the fix, passing
  after.
- **Nitpick — Escape closed the dialog without clearing the input.**
  Resolved as a side effect of switching to non-modal `.show()`: Chromium
  only auto-closes `showModal()`-opened dialogs on Escape, not `.show()`-
  opened ones.
- **Nitpick — missing `<legend>`/`aria-labelledby`.** Fixed: dropped the
  borrowed-from-modifier-picker `<fieldset>` (never had more than one
  field), added `aria-labelledby` on both the dialog and the input pointing
  at the dialog's own `<h3>`.
- **Deferred, not fixed — the dialog closes even on the "basket empty" /
  error-toast response.** `hx-on::after-request` treats any 2xx as
  `event.detail.successful`, and the empty-basket guard responds 200 with
  an in-place error toast rather than a non-2xx status (existing, pre-dated
  convention for every hold/resume error path — see
  `TestHoldHandler_EmptyBasketRejected` et al.). Fixing this properly needs
  the response to carry a machine-readable success/fail signal distinct
  from HTTP status, which is a bigger, separately-scoped change touching
  every hold/resume response, not this task. Noted, not filed as a new
  backlog card — low severity (a closed dialog with a visible toast
  elsewhere on the page, not a data-loss or blocking issue) and the
  existing convention it depends on is itself long-standing product
  behavior, not something this task introduced.

## Verified beyond automated tests
- Real running server + real Chromium (not headless-shell — this sandbox's
  pinned Playwright wants a newer revision than is preinstalled here, so
  `playwright.config.ts` was **temporarily** pointed at
  `/opt/pw-browsers/chromium` via `launchOptions.executablePath` for local
  runs only and reverted before commit — no config change ships).
  3/3 new e2e tests pass: named hold shows in the held strip, Cancel does
  not hold, blank name falls back to a timestamp. Zero console errors.
- Full existing e2e suite (34 default-project specs) re-run after the
  fixes: 33 pass, one pre-existing failure unrelated to this change (see
  below), including every OSK-related spec (`settings-osk.spec.ts`,
  `tender-panel-reachable.spec.ts`) — no regression from switching the
  dialog to non-modal.
- TDD genuinely verified, not just claimed: every new/changed assertion
  (label-priority tests, the truncation test) was confirmed failing against
  the pre-fix code with the real error, then passing after, via
  `git stash push -- <file>` / `stash pop` — both by me and independently
  by the reviewer.
- XSS: `HeldSale.Label` is escaped at render (`template.HTMLEscapeString`,
  `internal/pages/hold_api.go`) — reviewer live-probed a script-shaped label
  and confirmed it round-trips fully escaped.
- Offline-first: dialog is a static, same-page element — no new network
  call anywhere in this flow.
- No real client/shop name used — "Haaft 1" is quoted directly from the
  issue text; other examples use "Table 4"/"Sarah" placeholders.

## Known pre-existing failure (not from this change)
`internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure` fails in
this environment because the test relies on a `0o500` read-only directory
actually blocking a write, which doesn't hold when the test runs as root
(this container's user) — root bypasses that permission check. Confirmed
by stashing every file this change touches and re-running: fails
identically on unmodified `main`. Unrelated package, unrelated feature, not
introduced or worsened here.

## Gate
`go build ./...`, `go vet ./...`, `go test ./...` (full suite, one
unrelated pre-existing failure noted above), `guard-data-access.sh`,
`guard-i18n.sh` — all clean.

## Verdict
Safe to merge.

## Correction (2026-08-21, ut-docs#521)
The "No real client/shop name used" claim above was wrong, unknowingly, at
the time it was written. "Haaft" is in fact a real German café's real name
(a genuine prospect — see ut-docs#511, which surfaced this eight days
after this review). The example/test data has since been changed to
"Tab 1" throughout (`internal/pages/hold_api.go`,
`internal/pages/hold_api_test.go`, `e2e/tests/hold-named-tab.spec.ts`,
`web/ui/pages/index.html`) — see ut-docs#521's PR. The "Context" and
"Verified beyond automated tests" sections above are left unedited as an
accurate record of what this review actually said at the time; they should
not be read as still describing the current code.
