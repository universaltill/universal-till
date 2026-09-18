# Code review — httpx.RenderError renders without the shop's theme (ut-docs#2362)

- **Date:** 2026-09-18
- **Ticket:** ut-docs#2362 (`complexity:easy`)
- **Branch:** `fix/2362-error-page-theme`
- **Reviewer:** independent pass, fresh-context Sonnet subagent (per this
  card's `complexity:easy` routing — a clean instance that never saw the
  dev reasoning, `MODEL-ROUTING.md`'s "different model relaxes to
  different instance" rule for easy cards).
- **Verdict: SAFE TO MERGE AS-IS.** No blocking or correctness findings;
  two non-blocking style nits noted, not folded in (see below).

## The bug

`httpx.RenderError` (every 403/404/500 page, ~80 call sites) builds its
template data map with only `{"title", "Message"}` — no `.theme`. Since
`base.html` reads `.theme` for both the actual stylesheet
(`<link id="theme-css" ... href="/themes/{{.theme}}.css" ...>`) and the
`<meta name="ut-shell">` shell signature (`{{ shellsig .theme }}`,
ADR-0098), a themed shop's error page rendered with no theme CSS at all,
and computed a shell signature that never matched the rest of that shop's
pages. Boosted navigation then (correctly, given the mismatched
signature, but undesirably) fell back to a full unthemed document load
onto the error page instead of swapping it in place. `RenderError` has no
`*common.Deps` in scope — it's called from too many packages to thread
one through — so it can't read `d.CurrentState().Theme` fresh the way
every other page render does.

## What shipped

- `internal/httpx/httpx.go`: new `currentTheme atomic.Value` /
  `InitTheme(theme string)` / `currentThemeVal() string`, mirroring the
  existing `displayMode`/`InitDisplayMode` pair added for the identical
  "RenderError has no Deps" reason (ut-docs#2154).
- `internal/httpx/render_error.go`: `RenderError`'s data map now carries
  `"theme": currentThemeVal()`.
- `internal/pages/init.go`: publishes the theme at boot (next to the
  existing `httpx.InitCurrency(state.Currency)` call) and in
  `newRederiveSettings` (next to its own `InitCurrency` call) — the one
  function shared by both the replica-drift loop and cloud `set_setting`
  directives (ADR-0018), so a theme applied either way republishes
  immediately instead of staying stale until restart.
- `internal/pages/settings_page.go`: publishes the theme from the two
  remaining live-mutation sites — the dedicated `POST /api/settings/theme`
  handler, and the generic `POST /api/settings/upsert` key/value table's
  `case common.KeyTheme` (this one had the same class of gap
  ut-docs#2121 already fixed for `display.mode` in the same switch).
- `internal/httpx/render_error_test.go`: two new tests —
  `TestRenderErrorCarriesCurrentTheme` (asserts the theme `<link>` and an
  exact `ShellSignature("en","dark")` match in the rendered
  `<meta name="ut-shell">`) and `TestRenderErrorDefaultThemeIsUnchanged`
  (no-regression guard for a shop that has never configured a theme).
- `e2e/tests/persistent-shell-2224.spec.ts`: the existing "boosted HTML
  error page" test's `if (theirs === mine) { in-place } else { full load
  is correct too }` branch is simplified to assert the in-place swap
  unconditionally, now that the two shell signatures always agree
  regardless of which theme is active.
- `web/help/img/manifest.json`: `surface_sha256` refreshed via
  `scripts/ci/update-docs-shots-surface-hash.sh` — the default-theme
  render is byte-identical to before this change (see below), so this is
  the documented escape hatch rather than a full `make docs-shots` rerun.

## What the independent review found

Grepped every `.Theme =` assignment repo-wide
(`settings_page.go:2370`, `:2767`) and confirmed both now call
`httpx.InitTheme`. Traced `newRederiveSettings` to `init.go`'s
`StartSyncPull`/`StartCloudSync` call sites and confirmed the one
`InitTheme(applied.Theme)` call there covers both the replica-drift and
cloud-directive paths the ticket asked about. `setup_page.go` has no
`Theme` field at all (the setup wizard doesn't expose a theme choice), so
there is no missed door there.

**Byte-identical default-case claim, verified independently, not just
trusted**: before the fix, `data` had no `"theme"` key at all — a missing
map-key lookup for an `any`-typed map value. Confirmed via a throwaway
`html/template` probe that both `base.html` use sites (`data-theme="..."`
and `{{ if and .theme (ne .theme "default") }}`) already treated a
missing key the same as an explicit `""`, and that `shellsig`'s own
signature `func(theme any) string` already anticipated exactly this call
site with a safe `t, _ := theme.(string)`. So the fix is a genuine no-op
for a shop that has never set a theme, which is what makes the
`update-docs-shots-surface-hash.sh` escape hatch the correct call here
rather than a full regeneration.

**TDD claim re-verified independently, done for real**: removed the
`"theme": currentThemeVal()` line from `render_error.go`, re-ran
`TestRenderErrorCarriesCurrentTheme` — failed red on the exact predicted
mismatch — confirmed `TestRenderErrorDefaultThemeIsUnchanged` still
passed (a no-regression guard, not meant to catch this bug), restored the
line, confirmed both pass again with a byte-identical diff back to the
committed version.

Ran the full gate: `go build ./...`, `go vet ./...`,
`go test ./internal/httpx/... ./internal/pages/...` (also the full
`go test ./...` and `golangci-lint run ./...` across the whole repo before
this record was written), `gofmt -l` — all clean. Also ran
`guard-page-http-error.sh`, `guard-i18n.sh`, `guard-data-access.sh`,
`guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
`guard-help-topics.sh`, `guard-help-drift.sh` (pre-existing baselined
drift only), `guard-compliance-claims.sh`, `guard-htmx-loaded.sh`,
`guard-autofill-suppression.sh`, `guard-e2e-fixtures-import.sh`, and
`guard-docs-shots.sh` (green after the surface-hash refresh above) — all
green.

**e2e spec change judged justified, not merely assumed**: the removed
`else` branch existed only because the two shell signatures happened to
coincide pre-fix solely when the fixture shop runs the default theme.
Theme is applied at boot via `pages.Init` → `httpx.InitTheme` regardless
of which theme the e2e fixture configures, so asserting the in-place swap
unconditionally is correct for any theme the fixture might run, not an
assumption about which one it happens to run today. (The e2e suite itself
could not be executed live in this sandbox — no Chromium available, same
constraint already documented on other recent PRs in this repo — so this
is a static read of the spec + the fix's own guarantee, not a driven run.)

No i18n/money/repository-pattern concerns apply: no new user-facing
strings, no SQL, no monetary values touched.

## Non-blocking nits (not folded in)

1. `currentThemeVal()` is a small named helper, while the otherwise
   line-for-line-mirrored `displayMode` inlines its `.Load()` at its one
   call site (`backtosaleurl`). Cosmetic only — `currentThemeVal()` also
   has exactly one call site today.
2. No `web/help/**` topic needed updating — this restores parity on an
   already-existing, already-documented error-page behavior rather than
   introducing a new user-facing screen or control.

## Explicitly deferred

Nothing — this card's stated scope (RenderError picks up the current
theme) is fully addressed with no follow-up gaps found.
