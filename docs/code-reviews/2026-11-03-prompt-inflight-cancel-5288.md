# Review: confirm / PIN prompts — Cancel and Escape wait for an in-flight request (ut-docs#5288)

Branch `fix/5288-prompt-inflight-cancel`. BA/Architect notes were written by the
orchestrator from the card (its AC was already precise). A Sonnet dev agent built it.
A separate Sonnet Tester agent ran the package tests, one mutation and a driven
screenshot. An independent Opus reviewer reviewed it. The orchestrator applied the
review fix.

## Design

While a prompt's own request is in flight, **Cancel and Escape are inert**. The
request is never aborted: `htmx:abort` is client-side only, and these retries are not
idempotent (`POST /api/enrol/pair` clears the till's cloud identity first). An abort
could leave the action done with no answer shown, which is exactly what the AC rules out.

- `confirm_prompt.html` and `elevation_prompt.html`: `hx-disabled-elt` now covers
  both buttons. The selectors are `#confirm-modal button:not([data-osk-toggle])` and
  the `#elevation-modal` equivalent. Vendored htmx 1.9.12's `find X` matches only the
  first element and does not split a comma list. The PIN keyboard toggle stays enabled.
- `base.html`'s Escape handler still `preventDefault`s, but it returns without
  closing while a `data-ut-prompt` dialog has `form.htmx-request`.
- `inline-actions.js` `closeDialog`: refuses a `data-ut-prompt` dialog whose form is in
  flight (belt and braces for other close paths).
- A late re-prompt can no longer re-open a closed dialog, because the dialog can't be
  closed mid-flight. When the request ends (any outcome), htmx clears
  `.htmx-request` and re-enables the buttons before `afterRequest`, so
  `elevation-done` still closes on a terminal answer and an error never strands the
  operator.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| M1 | major | The `closeDialog` guard was global. On the self-order kiosk, a Cancel (`close-clear:selforder-modal`) during the checkout POST was refused, but `close-clear` still emptied the dialog. That left a blank modal with no exit, and the 409/429/5xx answer was lost on a detached form. Kiosk dialogs whose Cancel isn't disabled would also feel dead. | **Fixed**: the guard is scoped to `data-ut-prompt` in both `closeDialog` and base.html's Escape handler. The Go test pins the scoping. Self-order and kiosk e2e specs pass. |
| M2 | major | `utPostWithElevation` (app.js, a `fetch` path with a hand-inserted PIN dialog) never sets `.htmx-request`, so Cancel/Escape mid-retry still runs `onCancel` and then `onDone`. A wrong-PIN re-prompt re-opens the dialog. Callers: reset transactions, customer erase, cleanup catalog, archive export, EOD range. | **Deferred → ut-docs#5291**. This is a separate code path from the two templates the card names (#5275 accepted it as n4). It needs its own e2e. |
| m1 | minor | Global scope affected cashier dialogs (modifier picker, gift voucher). | Fixed with M1. |
| m2 | minor | `htmx.config.timeout` is 0, so a slow upstream leaves greyed buttons with no busy indication. | Deferred → ut-docs#5291 (busy state). |
| n1 | nit | Browsers never click a disabled button, so `closeDialog`'s guard is proven only by a string pin, not behaviourally. | Accepted. The disabled state and the Escape path are mutation-proven in e2e. |
| n2 | nit | The failed-request e2e accepts "closed or Cancel enabled". | Accepted: it tests the "never stranded" requirement. |
| T1 (tester) | — | Network error: `elevation-done` closes with only the generic toast. That is pre-existing, and it isn't Cancel/Escape, so it is outside the AC. | Optional item on ut-docs#5291. |

Checked and fine (reviewer):
- htmx evaluates the `hx-disabled-elt` selector at send time against the live,
  OOB-swapped dialog.
- The confirm→PIN hand-off closes the dialog directly, outside the guard.
- The scrim tap needs `data-ut-scrim-dismiss`, and neither prompt has it.
- No focus loss: a Danger confirm's autofocused Cancel is never focused at submit.
- No `showModal`, no new strings, no left/right CSS, no new inline script.
- UX §7: the OSK toggle and the PIN field stay usable mid-request.

## Verified

- `gofmt -l .` is clean, `go vet ./internal/pages/` passes, and `go build ./...` builds.
- `go test ./internal/pages/` passed (Tester, 292 s, pre-fix-M1). The 5288 and
  enrol-pair tests were re-run after M1.
- Go markup test `internal/pages/prompt_inflight_cancel_5288_test.go` fails before the fix.
- e2e after M1:
  - default: 11/11 (5288, 5275, kiosk-counter-order-held-2703, self-order age picker 3396, self-order language picker 4200)
  - diagnostics: 4/4 (pair confirm, including the new in-flight test)
  - auth: 18/18 (login, then the new PIN in-flight spec)
- Mutations:
  - With all `web/` fixes reverted, the in-flight tests fail at `toBeDisabled()` in all three projects (dev).
  - With only base.html's Escape guard removed, the default-project Escape test fails (dev and Tester independently).
- Guards: no-showmodal, i18n, rtl-logical-css, no-inline-handlers, data-access,
  kiosk-engine, ci-tests-sandboxed, mail-addresses, core-neutral.
- Tester screenshot: Escape pressed with the POST held, and the "Make this a satellite?"
  confirm is still open over the scrim. Both buttons are greyed, the status bar is
  visible, and nothing clips.

## Gaps

- No dark/RTL/locale/10-inch visual pass. No CSS changed.
- The full e2e suite was not run locally; CI runs it.
