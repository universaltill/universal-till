# Page-route error rendering (ut-docs#1455)

**Branch:** `fix/1455-page-http-error-render` · **Dev model:** Sonnet (subagent) ·
**Review model:** Opus (independent, fresh-context subagent, isolated worktree)

## What shipped

`internal/pages/` had 357 raw `http.Error(...)` calls (plus two thin wrappers
in `internal/pages/common/errors.go` — `LocalizedError`/`LogAndLocalizedError`,
both `http.Error` under the hood). On a top-level page GET route this
replaces the ENTIRE WebView document with a bare-text body — no nav rail, no
way back — a lock-up on the pinned Android kiosk (no browser Back button).
Reported live against `/tables`: `ListTablesWithState` failed and the WebView
showed nothing but `failed to load tables`.

This card adds the fix mechanism and closes the reported incident, without
attempting the full ~40-site migration (split to #1458) or the Android-side
transport-failure fallback (split to #1457):

- **`httpx.RenderError(w, r, status, msgKey, err)`** (`internal/httpx/error_page.go`) —
  renders the error through the full base+nav layout (so the rail and its
  contextual "?" help link stay reachable), sets `Cache-Control: no-store`,
  and logs `err` server-side only (5xx→Error into the ADR-0018 Problems ring,
  4xx→Info, same split `LogAndLocalizedError` already used) — the raw error
  never reaches the response body. `httpx.Render()`'s own composition was
  factored out into a shared `renderFullPage` helper so both share the exact
  same template/cache-key/locale logic; `Render()`'s own signature and
  behavior are unchanged.
- **CI guard** (`scripts/ci/checkpagehttperror` + `scripts/ci/guard-page-http-error.sh`),
  mirroring the existing `checkhelptopics` AST-based pattern: fails the build
  if a page-route handler (or one level of local-closure indirection) calls
  `http.Error`/`common.LocalizedError`/`common.LogAndLocalizedError` without
  a `// page-error:allow <reason>` escape-hatch comment. Wired into
  `.github/workflows/ci.yml`'s `build` job and `CLAUDE.md`'s guard list.
- **7 call sites migrated** to `httpx.RenderError`: `tables_page.go` (the
  reported bug), `my_reports_page.go` (×2), `permission_settings_page.go`
  (×2 — also removed a now-redundant `logging.L().Errorf` call there to
  avoid double-logging), `plugins_page.go`, `plugins_store_page.go`.
- **2 new i18n keys** (`common.error.super_admin_required`,
  `common.error.marketplace_not_configured`), real translations added to
  all four shipped locales (en/ar/fa/tr); everything else reuses existing
  keys (`common.error.manager_or_admin_required`, `common.error.server`,
  `menu.back_to_sale`).
- **The other ~46 pre-existing call sites** across ~22 files were annotated
  `// page-error:allow ut-docs#1458 (pending migration ...)` — comment-only,
  no logic touched — so the new guard is fully enforcing (no unmarked gaps)
  immediately, without forcing an oversized single PR. Independently
  verified: stripping all 46 annotations makes the guard report exactly 46
  violations — a 1:1 match, no decorative annotations.
- **New tests:** `internal/httpx/error_page_test.go` (status/header/nav-rail/
  no-leak assertions), `internal/pages/tables_page_test.go`'s
  `TestTablesPage_RepoErrorRendersFullLayout` (forces `ListTablesWithState`
  to fail, asserts full layout on the 500 — the actual reported-bug
  regression test), `scripts/ci/checkpagehttperror/main_test.go` (guard
  unit tests, both violation-detected and allow-suppressed directions).
- `make docs-shots` regenerated (this diff's new `web/ui/pages/error.html`
  and 30 edited `internal/pages/*.go` files invalidated the manual's
  screenshot-freshness hash) — 96/96 shots regenerated, `ar/sell.png`,
  `en/invoices.png`, `en/sell.png` and `manifest.json` changed.

## Independent review — verdict: safe to merge

Full independent Opus review ran in an isolated git worktree (ut-docs#386
mitigation — never reverts files on the shared orchestrator checkout). It
re-ran the entire validation gate itself (`gofmt`, `go build`/`vet`/`test ./...`
— 46 packages, 0 failures — plus every guard listed above), and
independently re-verified all three TDD claims by reverting only the
relevant production code, confirming each test failed with a real assertion
failure (not a compile error), then restoring and confirming green again.

It also read the new `renderFullPage` extraction, `RenderError`'s
write-ordering (confirmed no superfluous-`WriteHeader`/double-write risk),
confirmed the raw `err` genuinely never reaches the rendered body (dumped
actual rendered HTML and checked), and checked each of the 7 migrated call
sites individually against its original call (status preserved, message
intent preserved, `err`/`nil` threaded correctly, no double-logging).

**One real finding, accepted as a documented deferral, not fixed in this
PR:** the guard only descends into an inline `*ast.FuncLit` handler at the
registration site; `plugins_store_page.go`'s `/plugins/store` route is
registered as `PluginStoreHandler(deps)` — a call, not a literal — so it's
currently invisible to the guard. The reviewer proved this empirically by
reinstating the pre-fix bare `http.Error` at that exact site and confirming
the guard, its unit test, and its shell regression test all stayed green
with the bug back in place. Nothing is broken today (the code there is
correct); the guard simply can't defend that one route from a future
regression. Fixed in this PR: the guard's doc comment now names
`/plugins/store` explicitly as the one live route it currently misses, so
the gap is discoverable rather than only "theoretically documented".
Closing the gap itself (resolve the top-level `FuncDecl` a handler-call
expression refers to, scan the `FuncLit` it returns) is left for #1458,
which already touches this guard's target-file list.

**Fixed in response to review, before merge:**
- `make docs-shots` (mandatory — `guard-docs-shots.sh` is CI-blocking and
  this diff's new template + 30 edited handler files invalidated the
  existing manifest; the base branch's manifest was already stale for
  unrelated reasons, but this diff independently re-invalidates it either
  way).
- `web/ui/pages/error.html`: the error message was rendered twice verbatim
  (an `<h1>` duplicating the `<p>` right below it — a visible stutter,
  worse at kiosk font sizes). Simplified to a single `.card` with the
  message once.
- Same file: the "Back to sale" link — the *only* way off this page on a
  pinned kiosk — used the plain `.btn` class instead of the product's own
  46px touch-target floor (`.btn-touch`, used by every other "Back to sale"
  control, e.g. `menu.html`). Changed to `btn primary btn-touch`.

**Accepted deferrals, not fixed here (noted, some tracked separately):**
- No key-existence validation for a `msgKey` typo (pre-existing class of
  issue — `httpx.T` falls back to returning the raw key — `RenderError`
  just makes a typo more visible since it's now the whole page, not a
  buried error string). Backlog-worthy follow-up, not blocking.
- `tables_page.go`'s `tiles` closure is shared by `GET /tables` (the page)
  and `GET /ui/tables/state` (the 15s HTMX floor-plan poll fragment) — a
  repo failure on the poll now renders a full page instead of a short
  fragment body. Verified no visible regression: the vendored htmx 1.9.12
  doesn't swap non-2xx responses into the DOM, it fires
  `htmx:responseError` instead. Wasted render work only, out of this
  card's scope (the poll route isn't one of the 7 targeted sites).
- `RenderError` passes no `.theme` in its data map, so a themed shop's
  error page renders in the default theme rather than its chosen theme.
  Confirmed harmless (`base.html`'s `{{ if and .theme ... }}` no-ops on a
  missing key) and purely cosmetic.

## Verified beyond automated tests

- Actual rendered HTML dumped and inspected for the no-leak guarantee (a
  sentinel error string confirmed absent from the body).
- The guard's blind spot proven empirically by reinstating the original bug
  and watching all three of its own defenses (guard, guard's Go tests,
  guard's shell regression test) fail to catch it.
- Guard's negative-suppression claim verified end-to-end against the real
  tree: stripping all 46 `page-error:allow` annotations reproduces exactly
  46 violations.
- All four locale files re-validated as parseable JSON with identical key
  counts (1851) after the new keys; translations back-translated and
  confirmed to match each locale's existing store/plugin vocabulary, not
  copy-pasted English.
- `docs-shots` re-verified green after regeneration.

## Safe-to-merge

Yes. No blocking correctness defects. Merged via `merge_method: "merge"`
(never squash/rebase — see `reviewer` skill's note on commit
re-attribution).
