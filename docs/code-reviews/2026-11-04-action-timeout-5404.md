# Review: ordinary till actions time out after 30 s (ut-docs#5404)

Branch `fix/5404-action-timeout`. This follows ut-docs#5335, which timed out
confirm/PIN prompt requests only. Before this change, an ordinary htmx action
whose request never got an answer stayed busy with no message. Phases ran as
separate agents: BA, Architect + UX, Dev (Sonnet), Tester, and an independent
reviewer (Opus 5.5).

## What shipped

- `web/public/app.js` reuses the #5335 block. `UT_REQUEST_TIMEOUT_MS = 30000`
  sits behind `window.utRequestTimeoutMs()`. `utPromptTimeoutMs` is kept as an
  alias, and the e2e override `window.__utPromptTimeoutMs` still works.
  `htmx:configRequest` sets `detail.timeout` on a prompt request (as before)
  and on any request that `utIsTimedRequest` accepts. A request is timed only
  when all of these hold:
  - its triggering event is a user event: click, dblclick, submit, change,
    input, keyup, keydown or search;
  - it is not boosted;
  - it is not multipart;
  - it has no `[data-ut-no-timeout]` on the element or an ancestor;
  - its resolved pathname is not on `UT_UNBOUNDED_PATHS`, and it is not a
    `…/pairing-restart` path;
  - it does not already have its own `hx-request` timeout.

  Polls, `load`, custom-event refreshes, `htmx.ajax` and SSE are never timed.
  When a request times out, a GET shows the network text. Any other verb, or a
  prompt, shows the sticky `common.error.outcome_unknown` text.
  `htmx.config.timeout` stays 0.
- `docs/sale-screen-notifications.md` §2 describes the rule and lists what is
  never timed.
- e2e `action-timeout-5404.spec.ts`: A1–A7 and A6b, plus A8 added in review.
- No Go change, no template change, no new i18n key, no ADR. This extends the
  #5298/#5335 outcome contract and is a client implementation detail.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R-1 | **High** | `/api/pos/tender`, `/api/refund` and `/api/self-order/checkout` were timed. They block on the payment plugin's `.authorize`/`.refund` gate, whose own server deadline is 30 s (`internal/plugins/wasm_runtime.go` `paymentGateTimeout`, sized for a slow EMV dip). TSE/ÖKC signing and the DB commit come after it. A card payment that took 29 s would show "may or may not have been done" while the sale was still completing. The client also drops the response swap, so the basket still shows the old sale although the server has already reset it. The cashier is invited to charge again. | **Fixed**: the three paths were added to `UT_UNBOUNDED_PATHS`, with a comment giving the reason. |
| R-2 | Medium | `/api/sync/join` and `/api/setup/join` (`completeJoin`) use a 60 s LAN client and back up and replace this till's data. On a Pi this can take longer than 30 s. A false outcome alert there hides the join-success/pairing-restart fragment, and a retry would reuse a one-time token that has already been consumed. | **Fixed**: both paths were added to `UT_UNBOUNDED_PATHS`. |
| R-3 | Medium (manual) | `web/help/en/sell.md` "Selling with no network connection" still said the banner only appears when the till can't be reached, and that it always clears itself. It did not mention the 30 s timeout or the sticky outcome text. | **Fixed**: one sentence added in en and de, and the `sell` en topic hash was refreshed in `web/help/img/manifest.json`. No pixels changed, because the sell screenshots show no alert. tr/fa/ar were not changed; see Deferred. |
| R-4 | Nit | The #5335 spec titles "T6 a non-prompt htmx request is never timed out" and "T8 … for prompt requests only" are no longer true as worded, because both tests use `htmx.ajax`. | **Fixed**: the titles now say `htmx.ajax`. The assertions are unchanged. |
| — | Checked, no issue | **Predicate**: `utPathOf` resolves through `new URL(p, location.href).pathname`, so a query string and a relative `hx-post="api/…"` both match. A8 pins this. `requestConfig.boosted` and `triggeringEvent` exist in the vendored 1.9.12 `configRequest` detail. Polling calls the handler with the element, not an event, so `te.type` is undefined and the request is not timed. `hx-request` timeouts are respected (`d.timeout > 0`). | — |
| — | Checked, no issue | **Double alerts**: htmx calls only one of `ontimeout`/`onerror`, so `htmx:timeout` and `htmx:sendError` never both fire. The self-heal on `afterRequest` checks `successful`, which is unset on a timeout, and leaves the sticky alert alone. `chatRefocusFailed` only clears a mark. | — |
| — | Checked, no issue | **Other slow work**: printing is server-bounded at 10–15 s or less (`print_api.go`, `kitchen_print.go`). Cloud pair is bounded at 20 s. Plugin entry actions publish without blocking. Plugin store install, plugin identify and bg-removal use `fetch`/client code, not htmx. Voucher import and backup upload are multipart. Data reset and archive restore go through `utPostWithElevation`, are already timed by #5335, and are tracked on #5445. The setup wizard's language and tax-plugin installs are plain form posts. | — |
| — | Checked, no issue | **False-pass**: A6/A8 poll a recorded `xhr.timeout` per path, so a request that is never sent fails (`undefined` ≠ 0). A8 also asserts that a sibling path is timed. | — |

## Extra verification (reviewer)

- TDD re-verified in the review worktree. With app.js restored to `HEAD~1`,
  `action-timeout-5404` gives 7 failed (A1–A5, A7, A8) and 2 passed (A6/A6b,
  the not-timed baselines, by design). With the Sonnet version of app.js,
  minus the R-1/R-2 fix, A8 fails with `/api/pos/tender?x=1 Expected 0
  Received 1000`. With the fixed version, all pass.
- `node --check web/public/app.js` passes.
- Default project, `action-timeout-5404` + `prompt-timeout-5335` +
  `prompt-network-outcome-5298`: 19 passed. Auth project,
  `prompt-timeout-pin-5335` + `prompt-inflight-pin-5288`: 5 passed.
- Guards from the `ci.yml` build job all pass, except
  `guard-deadcode-baseline` and `guard-shellcheck-version`, which fail
  identically at `HEAD` (environment, not this change). `go test`
  `./internal/manual/... ./internal/httpx/...` and the `internal/pages`
  Help/Manual tests pass.

Pre-flight: Till user-visible behaviour: `web/help/` sell topic updated (en, de) + manifest topic hash (no pixel change); tr/fa/ar deferred → card "sell help: 30 s action timeout sentence in tr/fa/ar"; `reference/` n/a (rule documented in `docs/sale-screen-notifications.md`, updated); README n/a (not mentioned). en.json key: n/a (no new key). ADR: n/a. Review record: this file. Follow-ups: cards below.

## Verdict

**Approve with the fixes above.** R-1 was a real checkout risk (a false
"may not have been done" on a card payment). It is now fixed and pinned by A8.

## Deferred (follow-up cards)

1. **Prompt-leg tender/refund is still timed at 30 s.** A refund or tender
   that needs a manager PIN re-posts through the elevation prompt, which #5335
   times however long the card terminal takes. Exempt the payment paths there
   too, or give the prompt leg the payment deadline.
2. **Measure EOD run and shift close with a fiscal device on a Pi.**
   `/api/reports/eod/run` and `/api/shifts/close` are now timed. If TSE
   signing plus report build can pass 30 s, mark them `data-ut-no-timeout`.
3. **Slow report GETs show "Connection problem" after 30 s.** Report tab
   filters (`change`-triggered `hx-get`) on a large history on a Pi may
   legitimately take longer. Measure, then opt out any that do.
4. **Guard the unbounded-path list.** `UT_UNBOUNDED_PATHS` lives only in JS.
   Add a test that pairs known long-running routes (payment gate, join,
   backup, import, update) with the list, so a new slow route isn't silently
   timed.
5. **sell help: 30 s action timeout sentence in tr/fa/ar.** Add the new
   en/de sentence to the translated sell topics. ar also lacks the whole
   offline bullet.
