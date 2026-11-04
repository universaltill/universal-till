# Code review — confirm/PIN prompt timeouts say the till did not reply (ut-docs#5484)

Date: 2026-11-04. Lane: `lane:cloud-41`. Built by Sonnet; tested and reviewed independently by Opus 5.5, in a separate worktree.

## What shipped
- `web/public/app.js` `utPostWithElevation`: when a leg's own 30 s timer fires (#5335), the error now also carries `e.utTimeout = true`. `utNetworkError` stays set. This covers three paths: the fetch abort rejection, a late response after the abort, and an abort while the re-prompt body is being read.
- New `window.utOutcomeTimeoutText()` reads `#pos-alert` `data-msg-outcome-timeout` (`common.error.outcome_timeout`, added by #5463). If that is missing, it falls back to the outcome-unknown text, the same way the htmx path does.
- `utElevationErrorText(e)`: `utTimeout` returns the timeout text, any other `utNetworkError` returns the outcome-unknown text, anything else returns `e`.
- The contract comment above `utPostWithElevation` is updated.
- Specs:
  - `prompt-timeout-5335.spec.ts`: T3 now expects the timeout text, and T4 expects `utTimeout`. New T3b: a connection reset still shows the outcome-unknown text.
  - `prompt-timeout-pin-5335.spec.ts`: T10 expects `utTimeout`.
- `web/help/en/sell.md`: the bracketed sentence now says where these messages appear (the card of the confirmed Settings/Reports action) and that a 30 s timeout says the till did not reply in time.

## Findings
1. **Low, docs: fixed.** The help sentence described the sale-screen banner. But every `utElevationErrorText` caller (Settings → Data, Reports end of day/export) shows the text in its own card. It is reworded.
2. **Low, out of scope: deferred → ut-docs#5491.** On the sale screen, `postButtons` (a PIN-confirmed tile reorder) shows the generic "keeps working offline" banner for any rejection, a timed-out or dropped elevation included.
3. **Info, accepted.** No test covers a timer firing during the re-prompt body read. The tag works there because `timedOut` is set before `abort()`. Holding a body stream partway through costs more than it proves.

## Verification
- **TDD re-run by the reviewer:** with `app.js` reverted, T3, T4 and pin T10 fail for the claimed reason ("The connection dropped…" received; `to: false`). T3b passes. With the fix restored, 40/40 pass across prompt-timeout-5335, prompt-timeout-pin-5335, action-timeout-5404 and prompt-inflight-*.
- **Guards:** guard-i18n, guard-help-topics, guard-help-drift, guard-no-showmodal and guard-rtl-logical-css pass, plus the JS-scanning guards in ci.yml `build`.
- **UX gate:** pass. Only the choice between two existing translated messages changes. The timeout text is the right one for Data reset, customer erase and end of day, and it matches the htmx path.

Pre-flight: web/help topic updated (sell.md); docs-shots n/a (text-only, shows only on a timeout); reference/ n/a (no page covers this path); README n/a; en.json/meta/locales/lang packs n/a (existing key `common.error.outcome_timeout`); ADR n/a; mirrors n/a; review record this file; follow-up deferred → ut-docs#5491.

## Verdict
Approve.
