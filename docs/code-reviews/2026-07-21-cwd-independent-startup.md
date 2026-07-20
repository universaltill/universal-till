# 2026-07-21 — Make the app work when NOT launched from its own repo/install root

## Context
Farshid: "I don't have a shop" (his own "Task Runner" test data was mistaken
for a real business earlier in this work) — the actual goal is bringing
real, external shop owners onto the platform, with no one from this project
standing next to them to debug it. I did a cold-start walkthrough (fresh
install, zero prior state, exactly what a stranger would experience) to
find where that breaks.

## What I found
The app read its own UI (templates, locale JSON, static CSS/JS/logo/theme
assets) from disk via paths relative to the process's current working
directory (`filepath.Join("web", ...)`), with no embedding and no fallback.
This is fine when launched via `make run` from the repo root (every dev/CI
workflow does exactly that) — every packaging installer, systemd unit, or
desktop shortcut is not guaranteed to launch the binary from that directory.
Verified live, in order of severity:

1. **`config.NewI18n` failing was a hard `log.Fatalf`** — the whole process
   exits on startup, before serving a single request.
2. Once that's bypassed, **every page render panicked** — `RenderPartial`/
   `Render` did `template.Must(...ParseFiles(...))`, including the
   first-boot setup wizard itself, so a stranger could never get past first
   launch.
3. Once THAT'S bypassed, **the app served zero CSS/JS/logo/themes** — the
   login screen's PIN pad depends on Alpine.js (`x-data`/`@click`) loading
   from `/public/vendor/alpine.min.js`, which 404'd.
4. An independent review of the first round of fixes (below) caught that
   I'd fixed the *entry points* (locales, setup wizard, login, static
   assets) but missed **the actual checkout path**: `internal/ui/basket.go`
   (every add/remove/void), `internal/ui/journal.go` (right after a sale
   completes), `internal/pages/pos_api.go`'s `renderReceipt` (receipt
   printing), and `internal/ui/buttons.go` (the home screen's shortcut
   button grid, loaded via `hx-get` on every page load) all had the
   identical bug and were still unfixed — meaning after round one, the app
   got a stranger past setup and login, then **panicked the instant they
   scanned an item**.

## The fix
- `web/locales/embed.go`, `web/embed.go` (new): embed the base locale JSON
  files and the `ui`/`public` subtrees into the binary via `go:embed`.
- `internal/config/i18n.go`: new `NewI18nFS(fsys fs.FS, fallback string)`;
  old `NewI18n(dir string, fallback string)` kept as an `os.DirFS` wrapper
  (zero changes needed at the 7 existing test call sites).
- `internal/httpx/httpx.go`, `internal/ui/basket.go`,
  `internal/ui/journal.go`, `internal/ui/buttons.go`,
  `internal/pages/pos_api.go`: every `template.ParseFiles` → `ParseFS`
  against the embedded FS. A `stripWebPrefix` helper trims the caller-built
  `"web/..."` paths down to the embedded FS's own root (`ui/...`), so the
  ~20+ existing call sites across `internal/pages/*.go` needed zero changes.
- `internal/pages/static_page.go`: new `fallbackFS` — tries disk first (a
  shop's uploaded item images / receipt logo / theme override still write
  to and read from real `web/public/...` paths, untouched by this change),
  falls back to the embedded default. Implements both `Open` (per-file,
  disk-wins) and `ReadDir` (merges both sides by name, so an existing-but-
  empty or partially-populated on-disk directory doesn't hide embedded
  defaults from a listing the way a naive `Open`-only fallback would).
- `internal/pages/themes.go`: built-in theme listing/serving routed through
  the same `fallbackFS` — was previously invisible whenever
  `web/public/themes` didn't exist on disk (the common case; it's only
  created lazily by a customization action). Plugin themes are untouched
  (genuinely runtime-installed, correctly still disk-only).

## Independent review (two passes, different model)
**Pass 1** caught the checkout-path gap described above (item 4) — real,
high-severity, and would have shipped a "looks fixed, still crashes on the
first sale" state. Fixed and covered by new tests
(`internal/ui/render_cwd_test.go`, `internal/pages/receipt_test.go`'s
`TestRenderReceipt_WorksFromAnyWorkingDirectory`).

**Pass 2** (after the checkout-path fix) did an exhaustive repo-wide search
for any remaining instance of the same pattern (`ParseFiles`, `ParseGlob`,
`os.ReadFile`/`os.ReadDir` under `web/`, `http.Dir(`, `http.ServeFile(`,
literal `"web/` strings) — found none remaining. Confirmed the other
disk-relative `web/` reads in the codebase (`print_api.go`'s receipt logo,
`sync_assets.go`/`ai_api.go`'s item images, `httpx.go`'s cache-busting
`assetVersion`) are genuinely disk-only by design (shop-uploaded content,
not bundled defaults) and fail soft, never panic — correctly out of scope.
Two low-severity nits, both addressed: a small duplicated helper
(`stripWebPrefix` in both `internal/httpx` and `internal/ui` — left as
minor, harmless duplication rather than adding a cross-package dependency
for a 3-line function) and a test doc-comment that overstated what it
covered (fixed).

## Live verification (beyond the test suite)
Built the binary, ran it from a completely unrelated empty directory
(`/tmp/finaltest`, no relationship to the repo), and drove the real
first-boot flow via actual HTTP requests:
1. `POST /api/setup` (PIN, currency, country, store name) → 303 to `/`,
   session cookie set.
2. `GET /` with that session → 200, `<title>Universal Till</title>` (the
   real POS home screen, not a redirect back to login).
3. `GET /ui/buttons` → 200 (home screen's button grid — one of the round-1
   review's findings).
4. `POST /api/pos/scan` (a barcode) → 200, real rendered basket HTML
   fragment (`NewBasketView` — the exact request that panicked before the
   fix; "Item not found" toast because no catalog exists yet, which is
   correct/expected on a fresh install, not a bug).

## Verification
- `go build ./...`, `go test ./...`, `gofmt`, `go vet`,
  `scripts/ci/guard-data-access.sh`, `scripts/ci/guard-i18n.sh` all green.
- New tests: `internal/config/i18n_test.go` (2), `internal/httpx/httpx_test.go`
  (2 new), `internal/pages/static_page_test.go` (2), `internal/ui/render_cwd_test.go`
  (3), `internal/pages/receipt_test.go` (1 new), `internal/pages/themes_test.go`
  (1 new) — every one of them changes the process's CWD to an empty
  `t.TempDir()` before exercising the fixed code path, directly reproducing
  the trigger condition rather than relying on incidental CWD from
  `go test`'s package-directory convention.
