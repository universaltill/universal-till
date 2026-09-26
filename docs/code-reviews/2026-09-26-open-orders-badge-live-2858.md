# Code review — Open orders badge follows changes made on other tills (ut-docs#2858)

- **Date:** 2026-09-26
- **Branch:** `fix/2858-open-orders-badge-live`
- **Author:** pipeline (lane:cloud-24, Opus 5.5)
- **Reviewer:** independent Fable subagent (different model from the author)

## What shipped

Owner report: an order was held on a satellite (Pi) and paid on the main
till. The Pi's **Open orders** badge kept its red count until the sale
screen was reloaded.

Root cause: the main till already sends a `held_sales` link nudge (ADR-0114)
whenever it parks, claims/resumes or moves a held sale. But the satellite's
link client (`sync_link_client.go` `OnSync`) ignored that scope, because held
sales are read live and need no pull. The badge only re-fetched on the
`held-changed` event its **own** document's hold/resume answered with.

Fix:
- `common.Deps` gains an in-memory held generation (`MarkHeldChanged`,
  `HeldToken` = boot nonce + counter). `NudgeLink` moves it for any
  `held_sales` scope, including on a replica with no hub. `OnSync` moves it
  on a received `held_sales` nudge.
- Each badge render reads the token **before** listing and emits an
  out-of-band `#open-orders-watch` hidden span. The span polls
  `GET /ui/open-orders-badge/watch?v=<token>` every 3 s while the page is
  visible. The endpoint is an in-memory compare with no DB and no cross-till
  hop. It always answers 204, adding `HX-Trigger: held-changed` on a
  mismatch; htmx 1.9.12 fires HX-Trigger before its 204 no-swap decision.
  The badge then re-fetches the normal way.
- `index.html` gets the placeholder; the demo-mode allowlist gets the route.
- Help: `web/help/{en,de,tr,fa,ar}/open-orders.md` now says the count covers
  the whole shop and updates itself within seconds.
  `web/help/img/manifest.json` was refreshed by `make docs-shots`. The PNGs
  were reverted as container rendering noise: no rendered pixel changes,
  since the span is hidden and the prose isn't shown in any screenshot.

## Tests (TDD claims re-verified by reverting the fix)

- `TestReplicaLink_HeldOrderResolvedOnMainClearsTheSatelliteBadge` (Go, real
  link end to end): the satellite parks through write-through, the main till
  resumes it (`heldSaleClaimForResume`), the satellite's watcher fires within
  3 s, and the badge re-renders empty. **Reverted the `OnSync` bump → fails**
  "the satellite's watcher never saw the main till resolve the order".
- `TestOpenOrdersWatch_MainTillSeesAnOrderParkedThroughIt`,
  `TestNudgeLink_HeldScopeMovesTheHeldToken`, `TestLinkScopesTouchHeld`,
  `TestOpenOrdersWatch_EndpointContract`,
  `TestIndexTender_OpenOrdersWatchPlaceholder`.
- e2e `open-orders-badge-live-2858.spec.ts`: another client (not this
  document) parks and then resumes an order, and this screen's badge shows 1
  and then clears, with no navigation. **Reverted the trigger (watch never
  fires) → fails** "badge counts an order parked elsewhere"; reverted the OOB
  watcher → fails on the watcher assertion.
- `TestOpenOrdersBadge_CountsHeldSales`: the "visible" assert is now scoped
  to the badge tag, because the response also carries the always-hidden
  watcher.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major (pre-existing, #2765) | A cookie-bearing background poll touches the session, so the server-side idle auto-lock never revokes a session left on the sale screen. `/login` then 303s back to `/`. `sell-screen-watch.js` (5 s) already did this; this PR adds a second poller. | **Deferred to ut-docs#2901 (p1, security)**: a no-touch resolve tier for heartbeat paths, covering both pollers with auth tests. Accepted for this PR because it doesn't widen the exposure (the #2765 poller alone already defeats the lock). Side effect worth knowing: once revoked, the watcher's next poll gets HX-Redirect, so the screen goes to /login within 3 s. |
| 2 | minor | The Go test's 1.5 s wall-clock assert was flake-prone under `-race`, and the echo wait was discarded. | **Fixed**: the latency bound is the 3 s `waitFor`, and the test now waits explicitly for both generation moves (local write + the main till's echo, which can land before or after the write-through returns). `-race -count=10` green. |
| 3 | minor | The data wipe (`data_api.go`) and remove-demo (`settings_page.go`) delete `held_sales` without a nudge, so other tills' badges stay stale until the next change or reload. | Accepted: rare admin actions, outside the AC (hold/resume/recall/move, replica writes and kiosk counter orders all nudge). |
| 4 | minor (pre-existing) | `heldSaleForResume` nudges on a pure read (the move-table lookup); the move's write-through nudges again. | Accepted: the hub's per-peer dirty mask coalesces it. Now costs at most one extra badge re-fetch per move. |
| 5 | nit | Stale `index.html` comment ("re-fetched on hold_api.go's held-changed event"). | **Fixed**. The ADR-0114 table wording is unchanged: `held_sales` was already a no-pull scope before this change. |

Reviewer also verified: htmx `every Ns [cond]` parsing, the OOB span is
processed in settle and restarts the poll with the new token, and the OOB
element is stripped before the badge's own outerHTML swap. No loop paths:
the list/reconcile never nudge, a list error still answers 200 and
re-seeds, and a missing `v` never fires. `atomic.Int64` is copylocks-clean.
No raw SQL, no new strings, no `Engine` in kiosk files.

## Verified beyond automated tests

- Driven run: the Playwright spec above, plus the existing badge/hold/table-move,
  #2765 live-refresh and tender-reachability specs (22 passed), against a
  real till binary.
- Visual: no visible surface changed (the watcher is `hidden` +
  `aria-hidden`). The badge's look is unchanged and was exercised at
  1280×800 in en/light only. Dark theme, RTL and the 1024×600 kiosk size were
  **not** re-checked, because no pixel changed.
- Not checked on real hardware (Pi satellite + tablet main); the link path is
  covered by the real-hub Go test.

## Gate

`gofmt -l .` clean; `go vet ./...`; `go test ./...` green; `golangci-lint`
0 issues; guards: data-access, i18n, kiosk-engine, compliance-claims,
competitor-naming, help-topics, help-drift, htmx-loaded, osk-loaded,
page-http-error, autofill, demo-env, e2e-fixtures-import, docs-shots,
migration-version-collision all pass. `guard-deadcode-baseline` fails locally
identically on `main` (the desktop root is skipped without GTK headers:
`internal/logging` false positives), so it isn't from this change.

**Verdict:** safe to merge.
