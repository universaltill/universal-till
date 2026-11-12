# Review: utPostWithElevation retry in flight — Cancel/Escape inert, no re-open, busy ring (ut-docs#5291)

Branch `fix/5291-elevation-fetch-inflight`. Follow-up to ut-docs#5288
(`2026-11-03-prompt-inflight-cancel-5288.md`). Phases ran as separate agents:
BA, Architect (Opus), UX, Dev (Sonnet), Tester, independent reviewer (Opus).
The orchestrator applied the review fixes.

## What shipped

- `web/public/app.js` `utPostWithElevation`: the hand-inserted PIN dialog is not
  htmx-driven, so the retry now marks its form in flight itself —
  `.htmx-request`, `aria-busy="true"`, every button but the OSK toggle disabled.
  The #5288 guards (`base.html` Escape, `inline-actions.js` `closeDialog`) then
  apply unchanged. A second submit in flight is dropped. Exactly one of
  `onDone` / `onCancel` fires (`doneOnce` / `cancelOnce`). A dialog closed or
  removed by another path is never re-opened by a late wrong-PIN re-prompt.
  The request is never aborted: the callers (reset transactions, customer erase,
  cleanup catalog, archive export, EOD range) are not idempotent.
- `web/public/app.css`: a CSS-only ring on the submit button of
  `dialog[data-ut-prompt] form.htmx-request` (confirm prompt, htmx PIN prompt,
  fetch PIN prompt). Logical properties, static under `prefers-reduced-motion`,
  no height change, no text (no i18n key).
- `web/public/inline-actions.js`: `aria-busy` on the htmx-driven prompts too.
- `web/help/{en,ar,de,fa,tr}/elevation.md`: one sentence on the in-flight state.
- e2e: new `prompt-inflight-fetch-5291.spec.ts` (auth project, retry held with
  `page.route`), busy assertions added to both #5288 specs.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Busy state cleared before the re-prompt body was read; a second tap in that gap could start a parallel retry. | **Fixed** — on the re-prompt branch `settle()` runs after `r.text()`. |
| 2 | minor | A dialog *removed* (replaced by another prompt) rather than closed passed the "closed" check, so a late re-prompt could replace another action's prompt. | **Fixed** — check also `!openDialog.isConnected`. |
| 3 | minor | No test for "closed out of band, then a successful answer reaches onDone only". | Accepted — the path is a straight `close(); doneOnce(r)`; tests 1 and 3 cover the busy and re-open contracts. |
| 4 | nit | Help said every button gets a spinner (only submit does). | **Fixed** in all five locales. |
| 5 | nit | `aria-busy` could stick if `htmx:beforeRequest` is cancelled by another listener. | Accepted — no listener cancels it today. |
| 6 | nit | Spec duplicates cashier setup helpers. | Accepted. |

## Verification

- TDD re-verified by the reviewer in a separate worktree: with `origin/main`'s
  `app.js`, fetch tests 1 (Cancel still enabled) and 3 (closed dialog re-opened)
  fail; restored, pass. Tests 2 and 4 are regression guards (pass on old code too).
- After the review fixes: `prompt-inflight*` + `elevation*` auth specs 5/5; Tester
  ran 22/22 auth + 16/16 default prompt/elevation specs before them.
- Tester's driven run (windowed and `UT_KIOSK=1`): ring visible in light/dark and
  ar/fa RTL; Escape/Cancel inert in flight; nothing left busy after any outcome;
  OSK usable on the htmx path; Lock reachable (still `.show()`). Touch emulated
  only; de pack not bundled locally so German was not rendered.
- `go build ./...`, `gofmt -l .` clean; `node --check web/public/app.js`; guards
  rtl-logical-css, no-showmodal, i18n, help-drift/terms/topics/breadcrumbs,
  no-inline-handlers, compliance-claims, mail-addresses pass.

Pre-flight: web/help topic updated (5 locales; no screenshot change — transient state); reference/ n/a (no documented contract changed); README n/a; en.json n/a (no new key); review record this file; follow-ups → ut-docs#5298 (network-error outcome text), ut-docs#5299 (`.login-error` role=alert), ut-docs#5300 (OSK toggle hidden on the fetch-path PIN dialog).

## Verdict

Approve. Deferred: #5298, #5299, #5300.
