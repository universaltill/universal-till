# Code review — Help page feature guide (2026-07-17)

Branch `feat/help-feature-guide`. Farshid: "in the help or somewhere create a
list of features, like a list that can expand and shows the explanation and
guide usage of the feature."

## What changed
- `web/ui/pages/help.html`: NEW "Features" section above the existing quick
  how-tos — one native `<details>`/`<summary>` accordion per feature. Each
  expands to a plain-language explanation (`.what`) plus numbered usage
  steps. The old beginner cards stay under a "Quick how-tos" heading.
- `internal/pages/help_page.go`: `helpFeatureIDs` (ordered) + `helpFeatureSteps`
  (id → step keys) drive the template; render passes them in. 15 features:
  sell, catalog, inventory, reports, invoices, printing, designer, payments,
  multitill, plugins, claim, users, backups, updates, display.
- `web/public/app.css`: `.feature-list` / `.feature` accordion styling —
  logical properties (RTL-safe), 44px min touch target on summaries, uses
  theme vars.
- i18n: `help.features.*`, `help.guide.quicksteps`, and
  `help.feat.<id>.{title,what,s1..}` added to ALL 4 locales
  (en/tr/fa/ar) — 73 keys each, full translations (not English
  placeholders). guard-i18n green (623 keys, locales match).

## Design notes
- Native `<details>` = no JS, works offline, keyboard-accessible, fine in the
  webview and kiosk. Matches ADR-0008 (server-rendered HTMX, no SPA).
- Content is written for a shop owner/cashier, not a developer — describes
  what the feature is for and the steps to use it.

## Tests
- `TestHelpPageRendersFeatureGuide`: renders the REAL /help (per the
  test-everything rule — rendered-page path), asserts one accordion per
  feature and that keys resolve (a leaked `help.feat.` prefix = missing
  translation → fail).

## Follow-ups
- As features change, keep the guide in sync (it's static copy). Consider
  later: a short inline video/GIF per feature (Farshid asked for teaching
  videos — separate, bigger task; backlogged).
