# Review — tap check/resolve requests skip the 30 s client timeout (ut-docs#7029)

Date: 2026-11-25 · Lane: lane:cloud-41 · Built by: Dev (Sonnet) · Reviewed by: independent Opus subagent ·
Tester: separate Sonnet subagent · Size XS (complexity:easy) · ADR: none (follows ut-docs#5404/#5456/#7025).

## What shipped

`web/public/app.js` aborts a user-started htmx request after 30 s and shows "may not have been done"
(ut-docs#5404). Three tap routes ask the payment broker several times before they settle. Each broker call can
take up to `paybroker.Timeout` (15 s), and the work runs detached, so the server finished the settle while the
screen said it might not have happened. They now join `UT_UNBOUNDED_PATHS`, paired with `unboundedRoutes` in
`internal/pages/unbounded_routes_test.go`:

- `POST /manager/pay/tap/resolve`: the sale screen's manager resolve (`tap_resolve.html`). Status, cancel,
  status, then the native release: up to about 60 s.
- `POST /pay/tap/check`: the sale screen's Check / Cancel this payment buttons (`basket.html`). Up to about 75 s
  with cancel=1. Found by the review (see below).
- `POST /api/pay/attempts/attention/resolve`: the card's literal ask. It has no browser caller today
  (`git log -S` on `web/` is empty). It is declared anyway, since it has the same broker shape as
  `/api/pay/legs/resolve` (#7025).

The card named only the API route. BA found that the sale screen's real path is `/manager/pay/tap/resolve`.
`GET /ui/pay/tap/resolve` (the panel) makes one status ask of 15 s or less and stays timed.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R1 | major | `POST /pay/tap/check` (`tap_pending.go:313`, the sale-screen Check/Cancel buttons) has the same bug: a user click, timed at 30 s, while the server asks the broker for up to ~75 s detached. | **Fixed** in this branch: added a JS prefix and a Go row. Red without the JS prefix (`POST /pay/tap/check is declared long-running … but web/public/app.js does not exempt it`), green with it. |
| R2 | nit | The `why` for `/manager/pay/tap/resolve` left out the return of earlier split-tender legs on not_charged. | Fixed (text only). |
| — | ok | Prefix reach: no new prefix exempts another registered route (check c2 green, no `alsoExempted` entry). | — |
| — | ok | Server bound: every broker call is wrapped in `paybroker.Timeout`, the native release in `nativeResolveTimeout`, and reversals in plugin call timeouts. The ctx is `WithoutCancel`, so an untimed request cannot hang forever. | — |

## Verification

- TDD: the Dev added the Go rows first, and the test failed for both routes. The reviewer re-verified it in
  its own worktree: removing the `/manager/pay/tap/resolve` prefix fails `TestUnboundedRoutesMatchAppJS`, and
  restoring it passes. I did the same for R1.
- Tester mutation: removing each prefix in turn fails the test and names that route.
- Tester driven check: loaded the real `app.js` in Playwright chromium. `utIsUnboundedPath` returns:
  - true for `/manager/pay/tap/resolve` and `/api/pay/attempts/attention/resolve`;
  - false for `/ui/pay/tap/resolve` and `/manager/pay/tap`;
  - no page errors.
  There was no end-to-end tap against a live broker, because no reader or broker is available in this
  container.
- `go build ./...` passes. `go test ./internal/pages -run 'Unbounded|Timed|Tap'` passes, `go vet` and `gofmt` are
  clean. These guards pass: i18n, no-showmodal, no-native-confirm, rtl-logical-css, fixed-dates,
  page-http-error, data-access, kiosk-engine, core-neutral, mail-addresses.

Pre-flight: help topic n/a (no user-visible text or screen change: a wrong error no longer appears) ·
`reference/` n/a · README n/a · en.json n/a (no keys) · review record updated (this file).

## Verdict

Approve. Nothing is deferred.
