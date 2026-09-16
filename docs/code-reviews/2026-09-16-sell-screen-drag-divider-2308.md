# Code review: sell-screen hold-and-drag basket/products divider (ut-docs#2308)

**Date:** 2026-09-16
**Author (implementation):** Dev subagent (Sonnet, isolated worktree), orchestrated by the `:24` cloud pipeline lane
**Independent reviewer:** Opus subagent, fresh context, isolated worktree, model deliberately different from the Sonnet implementer per `MODEL-ROUTING.md` (`complexity:medium` → build at Sonnet, review at Opus)
**Card:** universaltill/ut-docs#2308

## What shipped

A draggable divider between the basket column and the products/quick-buttons
grid on the sell screen (`web/ui/pages/index.html`, `.pos-container` in
`web/public/app.css`):

- Touch-and-hold (~300ms) then drag, or plain mouse drag, resizes both panes
  live via a `--basket-col-width` CSS custom property; a plain tap/swipe
  elsewhere is unaffected (proven by e2e: tapping a tile immediately adjacent
  to the divider still adds to the basket).
- Visible grip affordance using existing theme CSS custom properties only
  (`--border`/`--accent`/`--radius-lg`), ≥24px hit area over a thinner visual
  divider.
- Clamped between a 22rem basket floor and a 60rem basket ceiling (leaving a
  16.5rem products/tender floor); inactive below the existing 900px
  stack breakpoint.
- Persisted per till via a new KV-settings key (`internal/pages/common/state.go`),
  mirroring the existing `KeyUIScale`/`RuntimeState.UIScale` pattern end to
  end (load/clamp/save round-trip), saved via a plain `fetch()` POST on
  pointer release — no page reload/navigation, since an operator mid-sale
  must never lose the live basket. `POST /api/settings/basket-panel-width`
  validates bounds server-side independently of the client, and is not in
  the auth-exempt route list.
- Reset via Settings → Display and via double-tap/double-click on the grip.
- RTL-safe: which pane is visually first is determined at runtime via
  `getBoundingClientRect()`, not a hardcoded LTR assumption — this also
  makes the divider adapt automatically to a theme plugin's own layout order
  (`theme-buttons-left`/`theme-screen-top`, external repos not in this
  checkout, deliberately not touched).
- Kiosk/self-order isolation: `web/ui/pages/self_order.html` never renders
  `.pos-container`/this divider at all — confirmed by both agents, no
  `kiosk-engine-guard:allow` needed.
- i18n: 3 new keys added to `web/locales/{en,ar,fa,tr}.json` with real
  translations (`de`/`es` are external plugin packs, deliberately untouched
  — follow-up owed separately, see below).
- Help docs: `web/help/{en,ar,de,fa,tr}/sell.md` updated with real,
  same-structure prose in all five locales; screenshots regenerated.

## Independent review — findings

Full detail in the Opus reviewer's own report; summarized here.

| # | Severity | Finding | Fix |
|---|---|---|---|
| F1 | **Blocking** | `{{ if gt .basketPanelWidthRem 0.0 }}` in `index.html:145` is a hard Go-template render error (`error calling gt: invalid type for comparison`) when the key is absent from the data map — which it is when the same content template renders via `internal/ui.NewRenderer` from `/ui/buttons` (`internal/pages/buttons_api.go:48`). Broke `go test ./...`. | Changed to `gt (or .basketPanelWidthRem 0.0) 0.0`, the same `or` idiom already used by `.currency` on that line. Re-verified across 7 cases (missing key, nil, 0, 22, 28.5, 33.33, 60) — no error. |
| F2 | **Blocking** | `guard-docs-shots.sh` failed — the sell screen's visible layout changed (new divider) and 4 locales' `sell.md` changed, but screenshots weren't regenerated; this guard is CI-blocking in `ci.yml`'s `build` job. | Ran `make docs-shots`; pixel-diffed all 124 regenerated PNGs against the sandbox's non-pinned Chromium (141.0.7390.37 vs the `@playwright/test` pin 149.0.7827.55 — ut-docs#622) and restored the 120 unaffected screenshots (pure font-rasterization drift on unrelated topics), keeping only the 4 genuinely-changed `{en,ar,fa,tr}/sell.png` + `manifest.json`. `manifest.json` hashes markdown + surface only, never PNG bytes, so the guard is legitimately green either way. |
| N1 | Note | Grip is subtle for first-time discovery (mitigated by help topic + `aria-label`); a long-press-then-release with no drag counts toward double-tap credit. | Not fixed — minor UX polish, out of scope for this pass. |
| N2 | Note | `en.json` gained 3 keys; `ut-plugin-language-{de,es}` need follow-up PRs. `lang-pack-drift` is advisory on this PR but blocking on push to `main` — `main` will show red on that check until the pack PRs land (expected, per `reviewer` skill's "brand-new key" case, not a mistake). | Owned by this same pipeline lane, same cycle — see close-out. |
| N3 | Note | `guard-deadcode-baseline.sh` fails in the review sandbox on missing `webkit2gtk-4.1` C headers — pre-existing environment limitation (ut-docs#1581), unrelated to this diff. `shellcheck` unavailable in sandbox (no `.sh` file changed by this diff anyway). | Environmental, not a diff defect. |
| N4 | Minor/i18n | Turkish `settings.display.basket_panel_help` renders "trackpad" as "im-pad", not idiomatic Turkish; rest of the tr copy is fine. | Noted — translation-quality call, not fixed here. |

**Verdict: PASS-WITH-FIXES.** Both blocking findings are fixed and re-verified
(full gate green after fixes, see below).

## TDD re-verification (independent, not taken on the implementer's word)

The Dev agent's report claimed a real bug found via TDD: `.basket`/`.products`
element references cached once at script-parse time went stale once htmx's
own `hx-trigger="load" hx-swap="outerHTML"` replaced those fragments,
silently breaking the live-width read and the RTL visual-order check.

The independent reviewer reverted *only* the fix (`liveBasketEl()`/
`liveProductsEl()` live queries → script-time-captured variables,
`web/ui/pages/index.html:591-593`), leaving the regression test untouched,
and re-ran the e2e suite:

- **6 of 8 tests failed with real assertion failures** (not compile errors):
  `mouse drag…` (`Expected: > 518.765625  Received: 392.875`), `drag
  direction is visually consistent under RTL` (`Expected: > 442.875
  Received: 392.875`), `touch-and-hold then drag…`, `persists across a
  reload`, `Settings Reset`, `double-click reset`.
- `392.875px` ≈ the 22rem floor — exactly the predicted failure mode: a
  detached node's `getBoundingClientRect()` reads all-zero, collapsing
  `startWidthRem` to the minimum on every drag.
- Restored the fix → **8/8 pass** again.

The claim is genuine, not a false-pass test.

## Verified beyond automated tests

- Full `go test ./...` (not just the touched packages) — green after F1.
- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean.
- `golangci-lint run ./...` — 0 issues.
- All CI-blocking guards relevant to this diff — `guard-i18n.sh`,
  `guard-help-topics.sh`, `guard-help-drift.sh`, `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-compliance-claims.sh`, `guard-docs-shots.sh`
  — green (re-run personally after applying the reviewer's F1/F2 fixes, not
  just trusted from the subagent's report).
- e2e: `pos-divider-resize-2308.spec.ts`, 8/8 passing, re-confirmed after F1.
- Manual read of the diff for repository-pattern/money/secret/client-name
  concerns: no SQL outside `internal/data`/`internal/db`, no money/fiscal
  file touched, no new file I/O (so the recurring `os.MkdirAll`/
  `paths.Data(...)` bug classes don't apply), no secret-shaped literal, no
  real client/shop name.
- Auth: `/api/settings/basket-panel-width` confirmed absent from
  `internal/auth/middleware.go`'s exempt-route list.
- Products/tender floor at max-drag (1280×800): no horizontal overflow on
  any tender tab, verified via `document.scrollWidth == clientWidth`.

## Explicitly deferred

- **Physical-device verification on the real pilot tablet (10.1", 1280×800)**
  — not available to this cloud pipeline lane. Covered instead via Playwright
  touch-emulation e2e at the exact same viewport/resolution. Recorded here as
  an outstanding manual step, not silently marked done.
- **Language-pack follow-up** (`ut-plugin-language-{de,es}`) for the 3 new
  `en.json` keys — owned by this same lane, same cycle, tracked separately in
  the close-out comment on ut-docs#2308.
- N1/N4 above (minor UX/translation polish) — left as-is, not blocking.

## Safe to merge

Yes — both blocking findings are fixed and independently re-verified; the
full gate is green.
