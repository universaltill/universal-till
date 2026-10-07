# Review: Settings cards show the server's refusal reason (ut-docs#2982)

- **Branch:** `fix/2982-settings-refusal-reason`
- **Author:** Opus 5.5 (lane:cloud-54)
- **Reviewer:** Fable, an independent subagent in its own worktree

## Scope

Most Settings cards are `hx-swap="none"` forms. On failure they only unhid
the page-wide "Could not save — please try again." banner, so a specific,
already-translated refusal never reached the operator. The main one is the
additional-till write-through refusal: "Can't reach the main till — …",
"The main till refused this change.". The `multitill` help topic (step 13)
already promised "refused with a message saying so"; the code now does
that, so no help prose changed.

## What shipped

- `internal/httpx/refuse.go`: `RefuseText(w, msg, status)` is `http.Error`
  plus `X-UT-Response: refused`. The header marks the body as an
  already-translated reason meant for the operator. It reuses the existing
  `X-UT-Response` vocabulary: `refused` was already the 200-fragment
  refusal on users / display-mode.
- `respondSettingsSyncError` (`settings_sync_proxy.go`) and every
  `http.Error(w, httpx.T(...))` in `settings_page.go` (13 sites) now go
  through `RefuseText`.
- **Deliberately unmarked:** untranslated developer strings ("could not
  save", "mode must be …"). The card's premise that every `text/plain`
  body is localized was wrong, so the marker, not the Content-Type, is the
  contract.
- `web/public/inline-actions.js`: a new DSL step,
  `save-error:<bannerId>`.
  - For a marked `text/plain` refusal it writes the text into a
    `role="alert"` line right after the posting form. A form-less checkbox
    carrier uses the end of its card instead.
  - In that case it hides the page banner, and hides `#pos-alert` when
    that is showing the generic server-error text this response just
    raised.
  - Anything else does exactly what `unhide:<bannerId>` did.
  - The form's next request clears the line (`clearSaveError` in the
    existing `htmx:beforeRequest` listener). It is scoped to `save-error:`
    carriers, so pollers never wipe it.
  - The step follows the file's contract: fixed name, id-only argument,
    `textContent` only.
- `web/ui/pages/settings.html`: all 28 `unhide:settings-save-error` became
  `save-error:settings-save-error`. The store-name chain drops its
  `text-response` (review finding 2).
- `web/public/app.css`: `.save-error-inline`, logical properties only.
- `web/help/img/manifest.json`: docs-shots surface-hash refresh only. The
  default render is unchanged; the line exists only after a refused save.
  Commit trailer: `Docs-Shots-Unchanged: true`.

## Tests

- `internal/httpx/refuse_test.go`
- `internal/pages/settings_refusal_reason_2982_test.go`, against a real
  replica handler with a dead, refusing or forbidding main till. It pins:
  - idle-lock, kiosk-idle-reset, kiosk-payment-mode,
    allow-negative-inventory, browsing-mode and upsert as 502 + marker;
  - 409 and 403 marked;
  - a translated 400 marked;
  - an untranslated 400 NOT marked.
- `e2e/tests/settings-refusal-reason-2982.spec.ts` (network-mocked
  responses of exactly that shape) covers seven cases:
  - a marked refusal shows inline, with both banners hidden;
  - an unmarked one keeps the banner and never shows the developer string;
  - a retry clears the reason;
  - a checkbox reverts and shows the reason;
  - RTL (fa) placement inside the card;
  - a multi-form card puts the reason right under the refused form,
    in the viewport;
  - store-name shows the reason exactly once.

## Findings (Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The line was appended to the end of the `.card`. `#settings-display` holds 7 forms, so a refused ui-scale save put the reason ~1100px below the form, off-screen, while both banners were hidden: "nothing happens". Also, a sibling form's request cleared it. | Fixed: a shared `saveErrorAnchor` = the form first, the card only for form-less carriers; used by the step and by `clearSaveError`. New e2e case asserts the line is the form's next sibling and is in the viewport. |
| 2 | should-fix | The store-name chain `fail text-response:store-name-msg save-error:…` showed the sentence twice. | Fixed: `text-response` dropped, template comment updated. New e2e case asserts the reason appears once. |
| 3 | nit | The header example in inline-actions.js still shows `unhide:`. | Fixed: a note on the new form added. |
| 4 | nit | No test for the 403 branch being marked. | Fixed: `TestSettingsRefusal2982_MainForbiddenIsMarked`. |

The reviewer also checked and found no issue with:
- marked-but-HTML 200 fragments (they never reach a `fail` chain);
- elevation and confirm prompts;
- `status:/not-status:` `text-response` chains, which keep precedence;
- the other `respondSettingsSyncError` callers (eod, invoice, update,
  print, report retention), where the header is additive and nothing keys
  on it;
- the detached-replay path, which falls back to the banner;
- XSS (`textContent` only);
- that every newly marked `T` refusal is operator-meaningful.

TDD re-verified by the reviewer in its worktree: reverting
`settings_sync_proxy.go` makes `TestSettingsRefusal2982_*` fail with
`X-UT-Response = "", want "refused"`; restoring it makes them pass.

## Verified beyond automated tests

Real browser screenshots of the refused idle-lock card, looked at:
- en and de (a long German string; de UI text comes from the language
  pack, which is not installed in the e2e till) and fa (RTL, real fa
  translation);
- light and dark themes;
- 1024×600 and 360px.

The reason wraps inside the card, aligns to the start side in RTL, and is
readable in dark mode. A real follower till with an unreachable main till
was not driven end to end in a browser: the server half is covered by the
Go replica tests, the client half by the mocked e2e.

## Gate

- `gofmt`, `go build ./...`, `go vet`: clean.
- `go test ./...`: green.
- Every `scripts/ci` guard in ci.yml's `build` job: green. Two exceptions:
  - shellcheck is not installed in this container (no `.sh` touched);
  - local `golangci-lint` could not load the Go 1.27 config here
    (toolchain mismatch). Only `unused` is enabled, and the diff adds no
    unused symbol; CI runs it.
- e2e: the new spec plus the 24 specs that post to `/api/settings/*` ran
  together: 102 passed, 1 failed. The failure was
  `phone-popup-fit-3351`, a 5-minute spec that timed out under parallel
  load. It passed alone twice on this branch and does not touch the
  changed code.

## Verdict

Safe to merge.

## Deferred

None. Translating the remaining English developer-string refusals is out
of scope: the UI never lets an operator trigger them, and they now fall
back to the generic banner as before.
