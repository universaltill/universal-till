# Review: prompt legs on the unbounded paths are never timed (ut-docs#5454)

This follows ut-docs#5404 (deferred item 1 of
`2026-11-04-action-timeout-5404.md`). #5404 stopped the 30 s client timeout on
`UT_UNBOUNDED_PATHS` (tender, refund, kiosk checkout, backup, import, update,
join, pairing-restart). It did not do this for prompt legs. An htmx form inside
`[data-after-request~="elevation-done"]` was timed before the path was ever
checked, and so were both `utPostWithElevation` fetch legs. A future PIN prompt
on a payment path would therefore show a false "may not have been done" and
invite a second charge. Phases ran as separate agents: BA, Architect, Dev
(Sonnet), Tester, and an independent reviewer (Opus 5.5).

## What shipped

- `web/public/app.js`:
  - The `htmx:configRequest` listener now returns early on
    `utIsUnboundedPath(d.path)`. This runs after the `d.timeout > 0`
    (own `hx-request` timeout) check and before the prompt/timed check, so one
    predicate covers ordinary and prompt htmx requests. The path test was
    removed from `utIsTimedRequest`; its only caller is this listener, so
    non-prompt behaviour is unchanged.
  - `utIsUnboundedPath` is exported as `window.utIsUnboundedPath`.
    `utPostWithElevation.send` arms its per-leg timer only when the URL is not
    listed. `url` is the same on the first send and the PIN retry, so both legs
    are covered.
  - Comments were updated, including the outcome contract above
    `utPostWithElevation`.
- `docs/sale-screen-notifications.md` §2 now says the listed paths are
  exempt on every leg. It also says the server bounds those paths (the
  payment-gate 30 s is a floor), not that the server's 30 s is the limit.
- e2e:
  - **A9** (`action-timeout-5404.spec.ts`): prompt forms to five listed paths
    record `xhr.timeout` 0. The cases include a query string and a relative
    URL. A sibling prompt to `/api/pos/remove` still gets the override.
  - **T9/T10** (`prompt-timeout-5335.spec.ts`): the first send and the PIN
    retry to `/api/refund` are held past 2.5× the override. Each test checks
    that the promise has neither resolved nor rejected and that no outcome
    alert shows. It then releases the request and checks that `onDone` ran
    once.
- Reviewer additions: `web/help/{en,de}/elevation.md` and
  `web/help/img/manifest.json` `surface_sha256` (see R-1 and R-2).
- No Go change, no template change, no new i18n key, no ADR.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R-1 | Medium (manual) | The change affects live prompts today, not only future ones. `/api/backup/now` and `/api/backup/restore` answer with `renderElevationPrompt` (`internal/pages/backup_api.go:127,268`), and their prompt forms post to `/api/backup/…`, which is on the list. Those prompts were timed at 30 s by #5335 and no longer are. That is the right result: a backup or restore on a Pi can legitimately take more than 30 s, and the ordinary leg was already exempt for this reason. However, `web/help/en/elevation.md` step 4 still said every PIN check that goes unanswered for 30 s closes with "may or may not have been done". | **Fixed**: one sentence added in en and de saying that a backup or restore approved in the dialog waits for the till's answer. This matches the existing sell.md wording. `elevation` has no screenshots and no topic hash. tr/fa/ar are deferred (see below). |
| R-2 | Medium (CI) | `scripts/ci/run-docs-shots-guard.sh` is a step in the `ci.yml` build job, but it is not named `guard-*`, and the Dev's guard sweep missed it. Any `web/public/**` edit moves `surface_sha256`. The step only warns on a PR, but it fails on the push to `main` right after the merge. | **Fixed**: ran `scripts/ci/update-docs-shots-surface-hash.sh`. This is a pixel no-op: the change touches timer logic and comments only, and the Tester's sale-screen shot looked unchanged. Only that one field moved (`547c08d3…` → `5a985763…`), and the guard now passes. **The commit that carries it needs a `Docs-Shots-Unchanged: true` trailer.** |
| — | Checked, no issue | **Relative URL in `utPostWithElevation`**: `utPathOf` resolves through `new URL(p, location.href)`, which is the same resolution `fetch` uses. A relative URL is therefore matched against the path that is actually requested. A9's `api/setup/join` case pins this for htmx. Every current caller passes an absolute `/api/…` URL. | — |
| — | Checked, no issue | **Order of the early returns**: `d.timeout > 0` is still first, so an explicit `hx-request` timeout on a listed path is still honoured. The path check now runs before `utIsPromptElt`, which is the fix. `data-ut-no-timeout` still does not opt out a prompt (Architect R5, unchanged). | — |
| — | Checked, accepted | **Non-payment prompts on listed paths** (backup now/restore today; join/import/update if a prompt is ever added). For these, the exemption is the intended effect: a client abort there was always a false outcome alert. | Accepted |
| — | Checked, accepted | **A listed prompt can now wait indefinitely on the client.** This is the same trade #5404 accepted for the ordinary leg. The server bounds tender and refund (`paymentGateTimeout` floor plus the plugin's own timeout). A real network drop still rejects through `fetch`/`htmx:sendError` as outcome-unknown (#5298). While the request waits, the prompt shows a spinner and Cancel waits, and the status bar stays reachable (`.show()`, no `showModal`). Checkout is not blocked. | Accepted |
| — | Checked, no issue | **`window.utIsUnboundedPath` availability**: it is set synchronously by the top-level IIFE in the same deferred script that defines `utPostWithElevation`, and it is read only when a request is sent. | — |
| — | Checked, no issue | **False passes**: A9 polls a recorded `xhr.timeout` per path, so a request that is never sent reads `undefined` and fails. The sibling prompt pins under-exemption. T9/T10 would reject on an abort and require a real late `onDone`. T10 also checks `fulfilled === 1`, which proves the retry was not aborted. | — |
| — | Checked, no issue | i18n (no new user string; JS has no literals), RTL (no CSS), no `showModal`, no inline handlers. The guards agree. | — |

## Extra verification (reviewer)

- **TDD re-verified** in the review worktree. With `web/public/app.js` at
  `HEAD~1`, `npx playwright test --project=default
  tests/action-timeout-5404.spec.ts tests/prompt-timeout-5335.spec.ts` gives
  **3 failed (A9, T9, T10), 18 passed**. With app.js restored to `HEAD`:
  **21 passed**. After the reviewer's docs/manifest edits: 21 passed, and
  `--project=auth tests/prompt-timeout-pin-5335.spec.ts` 3 passed.
- **Tester's run**: 38 default and 23 auth regression specs passed. The
  Tester also drove the app (run-till.sh, headless Chromium, override
  1000 ms). `utPostWithElevation('/api/pos/remove')` rejected with
  `utNetworkError` after the timeout, while `/api/refund` was still pending
  at 3 s. Screenshot: `sale-screen.png` (1280×800, light, EN; no visual
  change expected). Dark mode, RTL and kiosk were not looked at.
- `node --check web/public/app.js` and `go build ./...` pass. Every
  `scripts/ci/guard-*.sh` in the `ci.yml` build job passes, plus
  `check-brand-assets.sh` and `run-docs-shots-guard.sh` (after R-2). The one
  exception is `guard-shellcheck-version`: there is no `shellcheck` binary in
  this environment, and no `.sh` file changed. `go test ./internal/manual/...`
  and `./internal/pages -run 'Help|Manual'` pass.

Pre-flight: Till user-visible behaviour: `web/help/` elevation topic updated (en, de; a backup/restore PIN check is no longer cut off at 30 s); tr/fa/ar deferred → ut-docs#5461; sell topic n/a (en:130/de:117 already say payment, refund, backup/restore, import and join are never cut off); `make docs-shots` n/a (no pixel change; surface hash refreshed, R-2); `reference/` n/a (the rule lives in `docs/sale-screen-notifications.md`, updated); README n/a (not mentioned). en.json key: n/a. ADR: n/a. Review record: this file.

## Verdict

**Approve with R-1 and R-2 applied.** The fix is minimal, and one predicate now
covers all three legs. The tests are real red/green and cannot pass falsely.
The only gaps were outside the code: a help sentence the change made untrue,
and a surface hash that would have turned `main` red.

## Deferred (follow-up cards)

1. ut-docs#5461 — **elevation help: unbounded-path sentence in tr/fa/ar.** Add the R-1
   sentence to `web/help/{tr,fa,ar}/elevation.md` step 4.
2. ut-docs#5456 (already filed) — still open from #5404: guard `UT_UNBOUNDED_PATHS` against the server's
   long-running routes (#5404 deferred item 4). This change makes that guard
   more valuable, because a missing entry now also times that route's prompts.
