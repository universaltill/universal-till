# 2026-09-17 — Persistent app shell: boosted navigation, immutable assets, no client history cache (ut-docs#2224)

**Card:** ut-docs#2224 (p1, `complexity:hard`, blocked:env → lane:local) · **ADR:** ADR-0098 (extends ADR-0008/ADR-0097; ut-docs#2350) · **Lane:** local (Opus 5 built inline; independent review at Fable 5.1 in an isolated worktree, plus an Opus 5 design review of the ADR before code)

## What shipped

- `web/ui/layouts/base.html`: `#ut-page` region (`hx-boost="true" hx-history-elt hx-history="false"`) around rail + pairing mount + alert + `<main>` + status bar; `<meta name="ut-shell">`; `htmx-config` `historyCacheSize:0`/`refreshOnHistoryMiss:true`; the inline shell script (signature gate, `<html lang dir>`/`--ui-scale`/body class+`data-*` sync via `data-shell-classes`, page-scoped listener tracking, forms never boosted / anchors-in-forms opted back in, `UT-Shell-Nav` request header, history-cache purge, `data-nav-dir` on every swap, reduced-motion cancels the transition, `startViewTransition` watchdog, `UT.ready`). Bug-report panel and elevation dialog moved after the region.
- `internal/httpx/boosted.go`: `BoostedNavigation` middleware — `HX-Retarget/HX-Reselect: #ut-page` + `HX-Reswap` on every HTML response to a shell navigation (status held until Content-Type is known, so RenderError's 403/404/500 pages are addressed too); `IsFragmentSwap` false for shell navigations; wired innermost in `pages.Init`.
- `internal/httpx/shell_signature.go`: `ShellSignature(locale, theme)` over app version + `HeadAssets` versions + theme file + UI scale + OSK mode + idle lock + locale/dir; `shellsig` template func (`any`-typed — RenderError passes no theme).
- `internal/pages/static_cache.go` + `themes.go`: `assetCacheControl` — versioned `/public/` and built-in `/themes/` immutable for a year, unversioned `no-cache`, 404s never immutable, plugin themes always `no-cache`; the theme link is now versioned by the theme file's own mtime, not `app.css`'s.
- `httpx.imgVersion` → nanosecond mtime + size (`uploadVersion`) so a same-second photo re-upload gets a new immutable URL.
- `app.css`: `#ut-page` is the body's flex column; the `main` rules moved under it (no pixel change — all 124 regenerated help screenshots differ only by Mac-vs-Linux font rendering, checked by eye on `en/menu.png`).
- `app.js`: the `afterSettle` opacity ease skips the `#ut-page` swap; toast dismiss readyState-guarded. `index.html`/`catalog.html`/`inventory.html`: `UT.ready`; once-only tab-bar-fade guards moved from `window` to the `#ut-page` element. `plugin_install_modal.html`: `const` → `var`. Rail chips + pairing mount `hx-preserve`. Six export/external links `hx-boost="false"`.
- Guards: `internal/pages/persistent_shell_test.go` (region shape, no inherited target attrs, signature covers every head asset, no bare `DOMContentLoaded`, no top-level `const`/`let`/`class` in inline scripts, non-page links opt out, chips preserved), `static_cache_test.go`, `internal/httpx/boosted_test.go`, `shell_signature_test.go`; e2e `persistent-shell-2224.spec.ts` (8 tests) and `page-transitions-2223.spec.ts` updated for boosted direction.

## Independent reviews

**Design review (Opus, on ADR-0098 + the partial tree)** — 4 must-fix, 4 should-fix, 3 nits; all acted on before the first full run:
- M1 attribute inheritance: `hx-target/hx-select/hx-swap` on the region were inherited by every chip/poller and blanked the page on the first `load` (reproduced live) → server-side `HX-Retarget/HX-Reselect/HX-Reswap` headers instead.
- M2 forms boosted unconditionally (skips `onsubmit` guards, POST error → re-GET) → forms are never boosted (`htmx:beforeProcessNode`).
- M3 `location.assign` after a boosted POST error → moot once forms are not boosted; GET-only fallback kept.
- M4 14 handlers serve a bare fragment on `HX-Request` → `IsFragmentSwap` false for shell navigations.
- S5 listener stacking per visit → page-scoped listener tracking; S6 localStorage claim false under `hx-history="false"` → explicit purge at boot and per swap; S7 stale status bar / uiscale / osk / idle → status bar inside the region, settings in the signature; S8 six chip XHRs per navigation → `hx-preserve`.
- Nits accepted and documented: back is a reload with no pop motion; `afterSettle` guard added.

**Implementation review (Fable, isolated worktree, revert-then-restore TDD verification)** — verdict *not safe to merge* on the first pass; both must-fixes and all should-fixes fixed, re-verified:
- M1 `{{ shellsig .theme }}` aborted every RenderError page (nil `.theme`) — ~30 existing tests red, a blank error page on the till. Fixed: `shellsig(any)`; test `TestFuncsFor_ExposesShellSig` covers nil.
- M2 a boosted HTML error page fell to htmx's body-innerHTML swap (kills `#osk`, duplicates the bug-report panel). Fixed: the middleware retargets any HTML status (status held until Content-Type is known — RenderError writes the status first); tests `RetargetsHTMLErrorPages`, `RetargetsWhenStatusIsWrittenBeforeContentType`, `HeldStatusIsSentWithoutABody`; e2e covers both the in-place and the different-shell (unthemed RenderError on a themed till) outcomes.
- S3 `<html style>` sync clobbered `--osk-reserved-height` → only `--ui-scale` is merged.
- S4 once-only `window.__*Wired` flags defeated per-visit re-registration → flags live on the `#ut-page` element.
- S5 `Vary` overwritten by `IsFragmentSwap`'s `Set` → `Add`.
- Nits: anchors inside forms opted back in (6); `imgv` nanosecond+size (7); plugin theme / `load`-only chips documented in the ADR (8, 9); `Flush`/`Unwrap` on both writers (10).
- **Round 2 (Opus, fresh context) verified all seven fixes** and found one more must-fix: an in-form anchor opted back into boosting carries its own `hx-boost`, so `shellNav()` (closest `[hx-boost]`) no longer resolved to `#ut-page` and the audit "clear filters" link would have taken htmx's default body swap. Fixed: the opt-in marks the anchor `data-ut-shell-nav` and `shellNav()` accepts it; e2e clicks that exact link and asserts the `UT-Shell-Nav: 1` header and the same document. Round 2 also asked for held-status edge tests (redirect, `http.Error`, `Flush` before write) — added.
- TDD re-verification (by the reviewer): reverting `static_page.go`+`themes.go` → `TestStaticAssets_VersionedRequestsAreImmutable`/`TestThemes_VersionedRequestsAreImmutable` fail with `Cache-Control = ""`; reverting `httpx.go` → `TestIsFragmentSwap_BoostedNavigationIsNotAFragment` and `TestFuncsFor_ExposesShellSig` fail; restored → pass.

## Verified beyond the automated gates

- Headless Chromium probes (`e2e/.scratch`, not committed): Sell→Menu keeps the document (`UT.shellBootAt` unchanged), body class synced, 2 requests instead of 34; after 3 Sell↔Menu round trips the live body-level `htmx:afterSwap` listener count is flat (adds − removes constant); `localStorage['htmx-history-cache']` null; back = full reload; audit export = download event, URL unchanged; boosted link to `/login` and to a plain-text 404 → full document load; 0 of 32 plain forms boosted.
- Device numbers below were taken on the pre-review build of this branch; the review fixes change error paths and guards, not the navigation mechanism.
- **Pilot tablet** (Chrome 153, real device, pages served from the Mac over Wi-Fi, 8 alternating Sell↔Menu hops via CDP): **before** median **1864 ms** per hop (min 1207 / max 1897), **19 requests** Sell→Menu / **35** Menu→Sell; **after** median **653 ms** (min 384 / max 2299), **2–3 / 5–9 requests**, same document on every hop.
- **Pi 5** (WebKitGTK 2.52 — the engine `unitill-desktop` renders in — real `WebKit2.WebView`, same Mac-served till, 8 hops): **before** median **182 ms** (158–273) as full loads; **after** median **124 ms** (97–251), **1 / 4 requests**, same document.
- Windows WebView2: not measured this cycle (the acceptance names the tablet and the Pi); the Mac-served till is reachable from the VM for a later smoke.
- Full `default` e2e project: 540 passed (twice, before and after the review fixes); `go test ./internal/httpx ./internal/pages` green; `golangci-lint` 0 issues; CI guards green; `make docs-shots` regenerated.

## Deferred / follow-ups

- RenderError renders without the shop theme, so on a themed till a boosted error page is a full (unthemed) load — pre-existing; worth a p3 card.
- `preload` on `touchstart` (card step 3) and any server-side cache (step 4): not built — the step-1/2 numbers already deliver the bulk; revisit only with a device measurement that still wants it.
- Back/forward: full reload, no pop motion (accepted in ADR-0098).

**Verdict: safe to merge** after the second-round fixes; both reviews' must-fix items are closed with tests.
