# Review: status light says "Online" on Wi-Fi with no internet (ut-docs#3095)

Branch `fix/3095-cloud-reachability-light`. Author: Opus 5.5 (`:54` cloud
lane, Dev subagent). Independent review: Fable (read-only subagent in a
separate worktree), one round.

## What shipped

- **`internal/netreach`** (new): a `Monitor` that probes the till's own
  cloud host, `scheme://host/healthz` of `cfg.Marketplace.EndpointURL` (the
  host every till already talks to for plugins; the probe carries no till
  data). Any HTTP response = reachable; a transport error or the 5 s timeout
  = unreachable. `Status()` never blocks: it returns the cached tri-state
  and, when the result is older than 10 s, starts one background probe
  (single-flight, injected clock). Empty, invalid, non-http(s) or loopback
  endpoints disable it (unknown), so dev/e2e/docs-shots are unchanged.
- **`GET /ui/net-status`** → `{"data":{"cloud_reachable":true|false|null},"error":null}`.
  Classified like its sibling `/ui/main-till-status`: `backgroundPollPaths`
  (auth idle-lock) and the demo-mode read allow-list. Not auth-exempt; the
  self-order kiosk doesn't use `base.html`.
- **`base.html` status bar**: polls the route on load, every 10 s, on the
  `online` event and when the tab becomes visible. With the network up, no
  main-till link and `cloud_reachable === false`, the light shows the
  existing **No internet** text in the offline style. Everything else is as
  before. No new locale keys.
- **Checkout `offline` flag unchanged** on purpose (ADR-0044 Decision 1: it
  skips fiscal signing, and a local signer works without internet).
  Follow-up: ut-docs#3137.
- Manual: `web/help/{en,de,fa,ar,tr}/sell.md`, the network bullet. Manifest
  regenerated (`make docs-shots`, 124 passed). PNGs differed by font
  rasterisation only and the light is not in any shot, so they were not
  committed (same as #3088).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | blocker | The status-bar script sits inside `#ut-page` and re-runs on every boosted navigation; the shell's cleanup drops listeners, not timers, so each navigation stacked one more 10 s poller (reproduced: 1 nav → 2× requests, 3 navs → 4×), and each new closure flashed "Online" until its first answer. | **Fixed**: the answer and the single interval live on `window.__utNet`; a re-run clears the old interval before starting one. New e2e test (`boosted navigations do not stack pollers…`) failed on the pre-fix script (6 polls in 30 s, expected ≤ 4) and passes now. |
| 2 | should-fix | Hidden tab skipped polls and didn't re-poll on return. | **Fixed**: `visibilitychange` → poll (a `document` listener, so the page-scoped cleanup removes it on swap). |
| 3 | nit | A linked till still polled and probed for an answer it ignores. | **Fixed**: no poll while `.has-link`. |
| 4 | nit | Help: the new "No internet" state could be read as recording offline sales. | **Fixed** in all five locales ("…and are not recorded as offline sales"). |
| 5 | nit | `http.DefaultClient` + a plain-http non-loopback endpoint: a captive portal's page counts as reachable. | **Accepted, documented** in the package comment. The production endpoint is https, where a portal or proxy fails TLS/CONNECT. Refusing redirects wouldn't help (a 30x is still a response, and would misjudge a cloud http→https redirect). |
| 6 | nit | Load: ~1 probe per 10 s per till with an open visible page. | Accepted per the card's ≤ 30 s window; the probe is to the unauthenticated `/healthz` only. Noted on the card. |
| 7 | nit | WIP commit message. | Fixed (squashed). |
| 8 | CI | `persistent-shell-2224` (a boosted rail tap must fetch only the page) failed: the re-run script polled `/ui/net-status` at once on every navigation. | **Fixed**: one shell-lifetime interval calls the current page's `pollNet` (`window.__utNet.poll`); only the first load polls immediately, and a navigation shows the answer already held. Both specs pass locally (15/15). |

The review's rule checks were clean: no SQL, no money, i18n guard passes,
envelope and snake_case correct, offline-first (display only), no file writes.

## Verified

- TDD: netreach unit tests failed against a compiling stub before the
  implementation; `TestEveryPollerIsClassified` fails with base.html
  reverted (the reviewer reproduced this); the e2e spec 5/6 failed with
  base.html reverted (the reviewer reproduced this); the new stacking test
  failed on the pre-fix script (I ran it). The e2e spec passes 7/7.
- **A real run of the binary**:
  - Unroutable endpoint: `/ui/net-status` gave `null`, then `false`
    after the timeout. Screenshots at 1024×600 (en, fa/RTL) and at 360 px
    (en) show "No internet" / "اینترنت قطع است", with a red dot and text,
    and no clipping.
  - A non-loopback host serving HTTP: the probe hit `/healthz` (404), which
    correctly counts as reachable (`true`).
- Commands: `go build ./...`, `go vet`, `gofmt`, `go test -race`
  (netreach, auth, pages NetStatus/Demo), and the full `go test ./...`
  (Dev) all pass; `golangci-lint` 0 issues.
- Guards pass: i18n, help-topics, help-drift, docs-shots, core-neutral,
  data-access, compliance, competitor-naming, kiosk-engine, plus the
  reviewer's wider list.
- `shellcheck` is not installed in this container, so it was not run; no
  `.sh` files changed.
- **Not verified**: a real till with the router's internet unplugged, i.e.
  real hardware and real Wi-Fi.

## Verdict

Safe to merge once CI is green.
