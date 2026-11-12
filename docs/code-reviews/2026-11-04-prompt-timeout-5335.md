# Review: confirm/PIN prompt requests time out after 30 s (ut-docs#5335)

Branch `fix/5335-prompt-timeout`. Follow-up to ut-docs#5298 (a prompt's network
failure = "may or may not have been done"). Before this, a prompt request that
never got an answer left the dialog greyed (#5288/#5291 in-flight guard) with
no way out. Phases ran as separate agents: BA, Architect + UX (Opus), Dev
(Sonnet), Tester, independent reviewer (Opus).

## What shipped

- `web/public/app.js`, one constant `UT_PROMPT_TIMEOUT_MS = 30000` behind
  `window.utPromptTimeoutMs()`. e2e can shorten it with
  `window.__utPromptTimeoutMs`, read on each request; production never sets it.
  - htmx prompt forms (`data-after-request~="elevation-done"`, which covers
    `elevation_prompt.html` and `confirm_prompt.html`):
    `htmx:configRequest` sets `detail.timeout` (htmx 1.9.12 copies it to
    `xhr.timeout`; checked in the vendored min.js). On `ontimeout`, htmx fires
    `afterRequest` first, which closes the dialog through `elevation-done`, and
    then `htmx:timeout`. The new prompt-only `htmx:timeout` listener shows the
    sticky `common.error.outcome_unknown` alert. `htmx:sendError` now uses the
    same `utIsPromptElt` check and behaves as before. `htmx.config.timeout` is
    unchanged, so no other htmx request gets a timeout.
  - `utPostWithElevation`: each leg gets its own `AbortController` timer,
    started in `send()`, so the PIN retry's clock starts when the PIN is
    submitted. The timer is cleared on 401, on the terminal path before
    `onDone` (so download body reads have no time limit), and on the re-prompt
    path once its small HTML body has been read. An abort rejects as
    `utNetworkError` and follows the #5298 path. Without `AbortController`
    there is no timeout, as before.
- Help: one clause in step 4 of `web/help/{en,de,tr,ar,fa}/elevation.md`.
  `docs/sale-screen-notifications.md` §2 has one paragraph.
  `web/help/img/manifest.json` has a refreshed surface hash (no pixels
  changed), so the commit needs the `Docs-Shots-Unchanged: true` trailer.
- e2e: `prompt-timeout-5335.spec.ts` (default: T1–T8) and
  `prompt-timeout-pin-5335.spec.ts` (auth: T9–T11, added to `AUTH_ONLY_SPECS`).
- No Go change, no template change, no new i18n key. No ADR: this applies the
  #5298 outcome contract to one more failure mode, and the constant is a client
  implementation detail.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R-1 | Medium (product) | OQ-1: the first `utPostWithElevation` leg is the action itself when the operator is already elevated. Reset-transactions and archive restore take 8.7 s and 10.9 s to headers with 52k sales on a 4-core SSD box, and both grow linearly with history. A Raspberry Pi on SD storage, or a shop with a few years of history, goes past 30 s. The operator then sees "may or may not have been done" while the server finishes the action. | **Accepted, kept at 30 s.** Follow-up card needed (see Deferred). |
| R-2 | Low (test robustness) | The held `page.route` handlers in both specs were released only at the end of a passing test. A failed assertion left the handler parked on its gate until the page closed. | **Fixed**: each spec keeps a `pendingReleases` list, and a top-level `afterEach` releases every gate whether the test passed or failed. |
| R-3 | Nit | T2, T3, T9 and T10 "late response" releases can never reach the page, because the XHR or fetch was already aborted, so `route.fulfill` throws and is caught. They pin the real semantics: no late swap and no second settlement. | Accepted |
| R-4 | Nit | The `if (timedOut)` guard at the top of `.then(r)` cannot be reached: the timer is a macrotask, and the `.then` runs as a microtask straight after fetch resolves. | Accepted as a defensive guard |
| — | Checked, no issue | `htmx:configRequest` detail has no `elt` key in 1.9.12. The listener falls back to `ev.target`, which is the form (htmx dispatches the event on it). | — |
| — | Checked, no issue | No non-prompt element carries `elevation-done`; it appears only in the two prompt templates. Neither prompt dialog contains another htmx element, so no other request picks up the timeout. | — |
| — | Checked, no issue | Timer leaks and double settlement. Every exit stops the timer: tagNet, 401, the re-prompt `.then`, and the terminal path. `ended` and `failOnce` stop a second outcome. After `close()`, `openDialog` is null, so a late re-prompt cannot reopen the dialog. `r.text()` aborted on the re-prompt path → tagged → B1/B2 catch → busy cleared, dialog closed. | — |
| — | Checked, no issue | Interaction with #5288/#5291: `ontimeout` runs the `hx-disabled-elt` cleanup and `endRequestLock`. B2's catch calls `setBusy(false)`. The self-heal on `afterRequest` ignores the sticky outcome alert. `htmx:timeout` and `sendError` never both fire, so the alert is shown only once. | — |

### OQ-1 decision: keep 30 s

- Raising the single constant does not fix the problem, only moves it. The
  slow endpoints grow linearly with sales history, so 60 s or 120 s still fails
  for a busy shop. Meanwhile every PIN or confirm prompt at checkout would stay
  greyed out twice as long on a real hang, which was the defect this card fixes.
- The 30 s failure is safe. Nothing is replayed, the server completes the
  action, and the archive list or transaction list shows the result. The help
  text already tells the operator to check before trying again.
- The real fix belongs to the slow actions: make reset-transactions, archive
  restore and cleanup-catalog run as jobs, or give them their own bounded
  progress flow. A per-endpoint timeout was rejected by the Architect. The help
  clause stays at "30 seconds" in all five locales.

## Verification (reviewer)

- TDD re-verified personally: with `app.js` reverted to `origin/main`, the
  default spec gave **6 failed, 3 passed** (T1, T2×2, T3, T4, T8 fail; T5–T7 are
  regression guards). Restored: the file matches HEAD.
- The Tester's mutation checks (alert removed, `stopTimer` before `onDone`
  removed, timeout on every htmx request) each made the expected tests fail.
- e2e after the R-2 fix:
  - default project: the new spec, `prompt-inflight-cancel-5288`,
    `prompt-network-outcome-5298`, `htmx-senderror-1287`,
    `htmx-senderror-refund-shifts-1707`, `pair-confirm-escape-5275` and
    `dialogs-above-osk-5309`: **27 passed**.
  - auth project: `login`, the new spec, `prompt-inflight-pin-5288`,
    `prompt-inflight-fetch-5291` and `pin-dialog-above-osk-5304`: **34 passed**.
- `go build ./...` OK; `gofmt -l .` clean.
- Guards pass: i18n, i18n-meta, help-drift, help-topics, help-terms,
  help-breadcrumbs, no-showmodal, rtl-logical-css, compliance-claims,
  competitor-naming, mail-addresses, `run-docs-shots-guard.sh` (surface
  27823c37…).
- Tester's run on the real till (auth on, held routes, 3 s override): the
  confirm dialog, the data-reset card and the cashier PIN dialog all close at
  about 3.05 s and show the sticky outcome alert. Late 200 responses are
  ignored. The status bar stays reachable and no `showModal()` is used.
- Not run by the reviewer: full `go test ./...` and `golangci-lint`. There is
  no Go diff; the Tester ran `go test`, and `internal/plugins` needs its own
  20 m leg as in CI.

## Verdict

**PASS.** Commit with the `Docs-Shots-Unchanged: true` trailer.

## Deferred

- universaltill/ut-docs#5445 (R-1): long data-management actions (reset-transactions, archive
  restore, cleanup-catalog, large exports to headers) can pass the 30 s prompt
  timeout on a Pi or with long history and show a misleading "may or may not
  have been done". Make them jobs or progress flows, and measure them on Pi
  hardware.
- Already out of scope (BA §5): server idempotency keys; other raw `fetch`
  sites; server `WriteTimeout`; limiting the caller's own body read in
  `onDone`.

Pre-flight: web/help topic updated (5 locales; no screenshot change, surface hash refreshed → `Docs-Shots-Unchanged` trailer); docs/sale-screen-notifications.md updated; reference/ n/a (no documented contract changed); README n/a (no stale text); en.json key n/a (reuses `common.error.outcome_unknown`); ADR n/a (applies the #5298 contract); review record this file; follow-up → universaltill/ut-docs#5445 (R-1).
