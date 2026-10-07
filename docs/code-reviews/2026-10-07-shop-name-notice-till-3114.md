# Review: till-side "Name your shop" notice (ut-docs#3114)

- **Branch:** `feat/3114-shop-name-notice`
- **Author:** Opus 5.5 (lane:cloud-41)
- **Reviewer:** Fable, an independent subagent in its own worktree

## Scope

ut-docs#3114 asks for a dismissible "Name your shop" prompt on both the till
and my. for a shop whose `store.name` is still a placeholder ("", "My
Store", "Universal Till store"). This covers the **till-side slice**: the
dismissible nav-level notice, admin/manager-gated, never shown to a cashier.
The dependency this card named (the till-side shop-name editor + two-way
sync, ut-docs#3115) is already Done. The my./ut-cloud slice is reviewed
separately (ut-my-shop and ut-cloud's own `docs/code-reviews/`).

## What shipped

- `internal/pages/shop_name_notice.go`: `registerShopNameNoticeUI` wires
  `GET /ui/shop-name-notice`, modeled 1:1 on the existing `GET
  /ui/pairing-notice` (`pending_pairings.go`). Gated on
  `canPerform(d, r, "settings")` — the same gate the Settings page itself
  uses, and the role catalog never grants `settings` to `cashier`
  (migration 001). Reads `store.name` via `d.Settings.Get(ctx,
  common.KeyStoreName)` (same call `self_order_page.go` already uses); a
  read error, unset key, or a real (non-placeholder) name all give `200`
  with an empty body — only a placeholder name renders the partial.
- `web/ui/partials/shop_name_notice.html`: a `role="status"` strip, a link
  to `/settings`, a dismiss button reusing the existing `notice.dismiss`
  key. Dismiss is a `sessionStorage` flag (`ut-shop-name-notice-dismissed`)
  — clears itself in a new browser session, same convention as
  `pairing_notice.html`.
- `web/ui/layouts/base.html`: a poll mount (`load, every 60s`) next to the
  pairing-notice mount.
- `internal/pages/init.go`, `internal/auth/middleware.go`
  (`backgroundPollPaths`, `shellGetPaths`), `internal/pages/demo_mode.go`
  (`demoAllowedRoutes`): the route is wired and classified identically to
  `/ui/pairing-notice` in all three places — no broader access than that
  precedent.
- `web/public/inline-actions.js`: the dismiss button uses
  `data-action="dismiss-shop-name-notice"`, not an inline handler
  (`guard-no-inline-handlers` would reject one).
- `web/locales/{en,ar,fa,tr}.json`: 2 new keys each (the till's 4 core
  locales only — DE/ES/PT are separate `ut-plugin-language-*` repos,
  correctly out of scope here; their catch-up PRs follow in this same
  cycle once this merges, per the lang-pack-drift rule).
- `web/help/{en,de,tr,ar,fa}/display.md`: one sentence added to the
  existing Shop name item (item 9) in all five help locales — found by
  the reviewer as a stale-prose gap (the manual-ships-with-the-feature
  rule), fixed and amended into this commit.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The Shop name help topic (`display.md`, item 9, all 5 locales) said nothing about the new banner. | Fixed: one sentence added per locale, amended into this commit. `guard-help-topics`, `guard-help-drift`, `guard-compliance-claims` all re-verified green after. |
| 2 | nit | The banner links to bare `/settings` rather than `/settings#settings-store-name`. | Accepted: the pairing-notice precedent also links to a bare page. |

The reviewer also checked and found no issue: `store.name` is never
rendered by this banner (only fed to the placeholder predicate), so there
is no escaping/XSS surface; no new file writes; no real client/shop name or
secret literal in the diff; the 4-core-locale scope is deliberate (`web/
locales/` has no de/es/pt files to miss).

## Verification

**TDD.** The reviewer independently reverted the production files (in three
separate configurations: partial deleted, permission gate forced off,
placeholder check forced true) and re-ran the new tests each time:
- Partial deleted → `TestShopNameNoticeMount_KeepsPollingAndUsesADistinctID`
  fails (`expected the shop-name-notice placeholder in base.html`).
- Gate forced off → `TestShopNameNoticeUI_NeverShownToCashier` and
  `TestShopNameNoticeUI_EmptyWithoutSession` both fail, each showing the
  notice markup where an empty body was expected.
- Placeholder check forced true → `TestShopNameNoticeUI_EmptyForRealName`
  fails, showing the notice for a real name.

With the source restored, all 5 new tests pass
(`go test ./internal/pages/ -run ShopNameNotice -v`, `ok` 0.487s).

**Gate.** `go test ./internal/pages/... ./internal/auth/...` passes (no
regressions, `internal/pages` 197s, `internal/auth` 3.0s). `go build ./...`
clean, `gofmt -l` empty, `go vet` clean. `guard-i18n.sh` passes (2055
template keys resolve, all locales match `en.json`). `guard-help-topics`,
`guard-help-drift`, `guard-compliance-claims`, `guard-core-neutral`,
`guard-data-access`, `guard-kiosk-engine`, `guard-no-inline-handlers`,
`guard-no-showmodal`, `guard-htmx-loaded`, `guard-page-http-error`,
`guard-netaccess` all pass. `golangci-lint` could not run in this sandbox
(built for a different Go toolchain version than the module targets) —
flagged, not run.

**Driven run.** A real till was started (Chromium via Playwright) with a
placeholder `store.name` simulated directly in SQLite for a manager and a
cashier session. All 14 manual checks passed (manager sees it at both
1024×600 and 360×800 in English and Arabic/RTL with no clipping or
overlap; cashier never sees it; dismiss persists for the session and clears
in a new one; renaming the shop clears the banner within one 60s poll with
no reload). Screenshots reviewed directly by both the orchestrator and,
structurally via the code, the reviewer (who had no browser available and
says so explicitly).

## Follow-up (tracked, not in this PR)

- `ut-plugin-language-de`, `ut-plugin-language-es`, `ut-plugin-language-pt`:
  the 2 new `en.json` keys need a translated catch-up PR each once this
  merges (`lang-pack-drift` will be red on `main` until then — expected).
