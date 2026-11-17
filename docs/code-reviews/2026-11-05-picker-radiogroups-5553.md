# Review: picker-tile a11y follow-ups (ut-docs#5553)

Date: 2026-11-05 · Lane: `lane:cloud-54` · Author model: Sonnet (Dev) · Reviewer: Opus 5.5 (independent, fresh context)
Parent: PR #2363 / ut-docs#1103 (`2026-11-05-setup-picker-radiogroup-1103.md`, Deferred section).

## What shipped
- `web/public/radiogroup.js` now loads from `web/ui/layouts/base.html` (idempotent window flag; added to `HeadAssets` in `internal/httpx/shell_signature.go`). `setup.html` keeps its own include (standalone page).
- WAI-ARIA radiogroups (one Tab stop, arrows move **and** select, RTL mirrored) for: the catalog item colour grid, the category dialog colour grid (its page-local focus-only keydown handler is removed), the photo-cutout background tiles (`photo-cutout.js` `run()` refocuses the chosen tile after the busy state), and the basket order type (stable ids so htmx 1.9 restores focus after the `#basket` swap; the scan input keeps focus on load and after a tap).
- Off-palette stored colour → no tile checked, first tile keeps tabindex 0 (both grids).
- Focus rings at `app.css` ~L2165 (global `input:focus`) and ~L6660 (Designer colour radio) use `var(--focus-border)` (WCAG 1.4.11). The active order-type segment gets a two-tone ring (white outline + inset black ring): ≥3:1 on every theme accent (lowest: slate 3.13).
- `@media (forced-colors: active)` block: selected `.picker-tile` (`aria-pressed`/`aria-checked`) and the checked order-type use `Highlight`/`HighlightText` with a system-colour edge; focus stays distinct from selection.
- Help: keyboard sentence in `web/help/en/{sell,catalog,categories}.md`; `make docs-shots` regenerated `sell.png` (en/ar/fa/tr) + `manifest.json`.
- e2e: new `picker-radiogroup-5553.spec.ts`, `picker-forced-colors-5553.spec.ts`; new radiogroup case in `photo-cutout-3127.spec.ts`; `categories-editor-2284` and one other spec moved from `aria-pressed` to `aria-checked`.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| UX-M1 / Tester-D1 | Medium | White focus ring inside the accent fill on the active order-type: 1.8–2.8:1 | Fixed: inset black ring added; spec asserts it (failed before) |
| F1 | Medium | docs-shots stale (opaque input ring changes sale-screen shots; push-to-main guard would go red) | Fixed: `make docs-shots`, guard exits 0 |
| F2 | Low | photo-cutout spec never asserted the roving tabindex after arrows (mutation passed) | Fixed: asserted; fails with the tabindex line removed |
| F3 | Low | catalog off-palette fallback untested (mutation passed) | Fixed: new case; fails with the fallback removed |
| F4 | Low | `categories.md` missing the keyboard sentence | Fixed |
| F5 | Nit | Black inset ring survived forced colors; HC padding override approximate | Fixed: `box-shadow: none` in HC; thick edge drawn as an inset system-colour ring, no padding maths |
| Tester | — | Catalog "dirties the form" test asserted nothing | Fixed by Tester: asserts `#item-color` |

## Verification
- Tester (not the Dev agent): 7 mutations, each failed its spec; 56 Chromium e2e passed, `login.spec` 20 passed; driven run of the sale screen, both colour grids, `ar` RTL and forced colours, screenshots inspected, no console errors. WebKit not run locally (browser download blocked) — CI's `webkit-phone` covers it.
- UX §7 gate: taps unchanged, one Tab stop, focus survives the swap, wedge scan after a tap still adds the line, 360px fine; touch and High Contrast emulated only.
- Reviewer re-verified TDD personally in a separate worktree (T1 fail-as-claimed; T2/T3 exposed F2/F3, now closed). Go build/vet/test, guard-i18n, i18n-meta, rtl-logical-css, no-showmodal, help-topics, fixed-dates green.
- Not run locally: `guard-deadcode-baseline` (needs Go 1.27), shellcheck (no binary; no shell scripts changed) — CI runs both.

Pre-flight: Till user-visible behaviour: `web/help/` topics updated (sell, catalog, categories) + `make docs-shots` re-run (sell.png en/ar/fa/tr, manifest); `reference/` n/a (no reference page describes picker keyboard behaviour); README n/a. en.json keys: n/a (none added). ADR: n/a (existing patterns, ADR-0008 unchanged). Review record: this file. Follow-ups: deferred → ut-docs#5601 (icon grids radiogroup vs listbox), ut-docs#5602 (setup wizard 1024×600 fold).

## Verdict
Approve. Merge once CI is green.
