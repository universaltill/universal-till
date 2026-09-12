# 2026-09-12 — menu.html / error_page.html: mode-aware "Back to sale" (ut-docs#2154)

## What shipped

`web/ui/pages/menu.html` and `web/ui/pages/error_page.html` (the latter
rendered via `internal/httpx.RenderError`, used by ~80 call sites across
many packages) both hard-linked their "Back to sale" button to bare
`href="/"` — the same mode-unaware shape ut-docs#2146 already fixed for
`/open-orders`:

- On a `display.mode=backoffice` till, `/` redirects to `/backoffice`
  (`registerIndex`'s own comment: "a default landing preference, not a
  role bypass"), so an explicit "Back to sale" tap wrongly bounced a
  manager to the dashboard instead of the sale screen.
- On `display.mode=self_order`, staying on the till's own home
  (`/self-order`, ADR-0020 kiosk containment) rather than opening a path
  toward the cashier screen is the correct destination, for the same
  reasoning ut-docs#2146 applied to `/open-orders`.

Fix:

- `internal/pages/menu_page.go`'s `registerMenu` now fetches
  `display.mode` fresh via `d.Settings.Get` and passes
  `saleScreenReturnURL(mode)` (the existing shared helper,
  `internal/pages/index_page.go`, added by ut-docs#2146) as
  `backToSaleURL` — the exact same pattern `open_orders_page.go` already
  uses. `menu.html`'s href now reads `{{ .backToSaleURL }}`.
- `error_page.html` is a different shape: `RenderError` has no
  `*common.Deps` in scope (called from ~80 sites across many packages,
  and `internal/pages` already imports `internal/httpx`, so the reverse
  import would cycle), so it can't read `display.mode` fresh per
  request the way the two handlers above do. Instead, `internal/httpx/httpx.go`
  gained a package-level `displayMode atomic.Value` + `InitDisplayMode(mode
  string)` publisher, mirroring the **existing** `kioskMode`/`selfOrderMode`
  atomic + `InitKiosk`/`InitSelfOrderMode` pattern already in that file
  (backing the `"kiosk"`/`"selforder"` template funcs). A new
  `saleScreenReturnURLFor(mode string) string` (same switch logic as
  `saleScreenReturnURL`, intentionally duplicated across the package
  boundary — verified no import cycle exists to avoid instead) backs a new
  `"backtosaleurl"` template func. `error_page.html`'s href now reads
  `{{ backtosaleurl }}`.
- The 4 existing call sites that already call `httpx.InitSelfOrderMode(...)`
  whenever `display.mode` changes (`internal/pages/init.go`'s boot-time
  read and its runtime re-derive; `internal/pages/settings_page.go`'s
  dedicated `POST /api/settings/display-mode` handler and its generic
  key/value settings path) now also call `httpx.InitDisplayMode(...)`
  right alongside, so the new atomic stays live-updated the same way
  `selfOrderMode` already is — no new call sites, no new triggers.
- `web/help/img/manifest.json` + a few screenshots regenerated via
  `make docs-shots` (required: `guard-docs-shots.sh` hashes the whole
  `web/ui/**` tree plus any non-test `internal/pages/**.go` registering a
  screenshotted route, and this diff touches both). `menu.png` itself is
  byte-identical (default/register mode is unchanged); `catalog.png`
  (en/tr) and `sell.png` (en) picked up unrelated non-deterministic
  re-render diffs — the same files PR universal-till#1110 (merged same
  day, unrelated card) also touched for the same reason.

## Tests

- `internal/pages/menu_backtosale_2154_test.go` (new) — handler-level,
  backoffice/self_order/default modes for `/menu`.
- `internal/httpx/render_error_test.go` (added) — same three modes for
  `RenderError`'s rendered output.
- TDD verified for real, by both the implementer and the independent
  reviewer, on both sides of the fix: reverting `menu_page.go`'s
  `saleScreenReturnURL(mode)` wiring (or `httpx.go`'s `"backtosaleurl"`
  func body) back to a bare `"/"` makes the backoffice/self_order tests
  fail with the expected assertion error; the default-mode test stays
  green throughout (proving it's a real behavioral pin, not a tautology).
  Restored, re-ran green.

## Independent review

Fresh-context Sonnet subagent (isolated worktree — `complexity:easy`
routing, per the pipeline's model-routing rules), briefed with the diff
scope and told explicitly to find real problems.

Verified for real, not taken on trust:
- `gofmt`, `go build`, `go vet`, `golangci-lint run` (0 issues) all clean.
- Full `go test ./internal/httpx/... ./internal/pages/...` — all green.
- Independently repeated the TDD revert/restore on **both** sides of the
  fix (`pages` and `httpx`) and got the identical failure signature each
  time; restored and confirmed `git status` clean.
- Independently verified the import-cycle claim rather than trusting the
  diff's own comment: 171 files under `internal/pages` import
  `internal/httpx`; zero real imports the other way.
- Checked for missed call sites (`grep -rn 'menu.back_to_sale' web/ui/`):
  exactly the 3 expected hits (`menu.html`, `error_page.html`,
  `open_orders.html` from #2146) — no fourth hard-linked instance left
  behind. Also checked `admin.html`'s "Back to menu" button (different
  destination, different bug class, correctly out of scope) and the nav
  rail's own `Sell` entry (an always-present nav link, not an explicit
  override action — correctly not treated as the same bug class).
- Confirmed no i18n key was added or touched — both templates reuse the
  existing `menu.back_to_sale` key, unchanged in `web/locales/en.json`.
- Diffed the two HTML files in isolation and confirmed only the `href`
  attribute value changed (plus explanatory comments) — no markup, class,
  icon, or label changed.
- Confirmed neither of the two recurring bug classes this pipeline
  watches for applies: no file writes anywhere in this diff, and no
  filesystem path construction at all.
- Checked whether the user manual needed an update: `web/help/en/menu.md`
  only describes default/register-mode behavior, which is unchanged — no
  topic update needed, matching the implementer's own judgment.

**Verdict: SAFE TO MERGE. No findings** — no correctness, safety, or
process-gap issues.

One non-finding observation carried forward: the new `render_error_test.go`
tests mutate the package-level `displayMode` atomic via `InitDisplayMode`
with `t.Cleanup` resets — the same pattern the pre-existing
`selfOrderMode`/`kioskMode` tests in that file already use. No test in
that file uses `t.Parallel()` today, so there's no live race; worth
keeping in mind if a future test there adds it without noticing the
shared global.

## Verified beyond automated tests

The changed button carries zero new markup or CSS — only its `href`
target changes, and only in two non-default till modes. The default
(register) mode's rendered output — the vast majority of real shops — is
byte-identical, confirmed both by the regression test
(`..._DefaultModeIsUnchanged` on both surfaces) and by comparing
`web/help/img/en/menu.png` before/after `make docs-shots`: no diff.
`error_page.html` has no dedicated screenshot topic (it's a fallback
render path, not a directly-navigable page), so its visual check is the
handler-level test rendering through the real `html/template` engine
(`TestRenderErrorBackToSaleURL_*`), not a screenshot.

## Deferred / out of scope

None — this card's acceptance criteria (mode-aware "Back to sale" on both
named surfaces, regression tests, no default-mode change) are fully
covered.
