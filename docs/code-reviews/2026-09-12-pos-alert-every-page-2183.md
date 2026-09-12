# 2026-09-12 — Extend #pos-alert's htmx-failure banner to every page (ut-docs#2183)

## What shipped

`web/ui/partials/pos_alert.html`'s `#pos-alert` element is the app-wide
client-side request-failure surface (ut-docs#213): app.js's
`htmx:responseError`/`htmx:sendError` listeners (`showAlert()`) already fill
it in on *any* page, on every page load (app.js loads via base.html). But
until this card, only three pages — `index.html`, `admin.html`, `items.html`
(ut-docs#2179) — actually declared `{{ template "pos_alert" . }}` in their
own content block. Every other page (`/pin`, `/catalog`, `/inventory`,
`/settings`, `/catalog/tax-codes`, ...) had no `#pos-alert` DOM element at
all, so the existing app-wide mechanism silently had nothing to show:
`document.getElementById('pos-alert')` returned `null` and `showAlert()`
returned early.

Fix: `base.html` itself now declares `#pos-alert` once, as a sibling of
`nav`/`#pairing-notice-mount`/`bugreport_panel`, outside `<main>`/the page's
own content block — so every page rendered through the normal layout gets
it for free, and it survives whatever in-panel htmx swap (`#admin-panel`,
`#items-panel`) the current page does internally. The three original
per-page copies were removed to avoid a duplicate `id="pos-alert"`.

## The real gap this surfaced: hand-built template file sets

`base.html` now unconditionally references `{{ template "pos_alert" . }}`.
`html/template` only resolves a `{{ template "name" }}` call at *execute*
time, not at parse time — so any hand-built file set that parses
`base.html` and later executes the `"base"` template needs `pos_alert.html`
in that same parsed set, or that execution fails at runtime with
`no such template "pos_alert"`. Four real call sites needed a fix beyond
the two intentional page removals:

- `internal/httpx/httpx.go`'s `NewRenderer` — `pos_alert.html` now rides
  along automatically, same as `bugreport_panel.html` already did. This one
  broke **12 existing tests** the moment `base.html`'s change landed
  (`sync_banner_test.go` ×4, `template_helpers_test.go` ×8) — caught by
  running the actual test suite, not by inspection.
- `internal/pages/catalog/handlers.go`'s `catalogFiles` — `/catalog`'s
  plain (non-fragment) `GET` executes `"base"` directly; would have broken
  in production with no test coverage to catch it (no existing test drives
  `/catalog`'s full-page render path).
- `internal/pages/tax_codes_page.go`'s `taxCodesFiles` — same shape, same
  fix, for `/catalog/tax-codes`.
- `internal/ui/buttons.go`'s existing `pos_alert.html` entry (added by
  ut-docs#2179 for a narrower reason) stays required, comment corrected.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean.
- `go test ./...` — full suite, 58 packages, all `ok`, zero `FAIL`.
- `golangci-lint run ./...` (v2.5.0, matches the repo pin) — 0 issues.
- Guards run: `guard-i18n.sh`, `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-page-http-error.sh`, `guard-compliance-claims.sh`,
  `guard-htmx-loaded.sh`, `guard-docs-shots.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh`, `guard-plugin-menu-read.sh`, `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-autofill-suppression.sh`,
  `guard-e2e-fixtures-import.sh`, `check-brand-assets.sh`,
  `guard-makefile-version.sh` — all pass. `guard-shellcheck-version.sh`
  couldn't run (`shellcheck` not installed in this sandbox) — a pre-existing
  environment gap, not a regression; this diff touches no `.sh` file.
- TDD confirmed both by me and independently by review: reverting only
  `base.html`'s added `{{ template "pos_alert" . }}` line makes all four
  new/updated tests fail with the exact "#pos-alert absent, got 0 want 1"
  symptom (not an unrelated error); restoring the line passes them again.
- `web/help/img/manifest.json`: only `surface_sha256` refreshed via
  `scripts/ci/update-docs-shots-surface-hash.sh`, not a full regeneration.
  `#pos-alert` ships `hidden` by default and `app.css:2199`'s
  `.pos-notice[hidden] { display: none; }` removes it from the render tree
  regardless of DOM position, and no CSS selector keys off its sibling
  position — so moving it changes zero rendered pixels. Independently
  confirmed by review, who also swept `web/help/**` for any topic
  documenting `#pos-alert` (none exists) — no manual update needed.

## Independent review

Fresh-context Sonnet subagent, isolated worktree (per this card's
`complexity:easy` routing). **Verdict: SAFE TO MERGE, no blockers.**
Independently re-ran the full gate (build/vet/test/lint/guards) for real —
including the full ~10-minute `go test ./...` run — and independently
reproduced the TDD revert/restore with its own transcript. Repo-wide swept
every `.go` file (production and `_test.go`) referencing `layouts/base.html`
or executing `"base"`/`RenderWith(...)("base", ...)` and found **no
additional broken call site** beyond what this diff already fixes.

Findings, both non-blocking, both addressed in this same commit:
- `internal/ui/buttons.go`'s carried-forward comment (originally from
  ut-docs#2179) claimed the missing partial fails "at parse time" — review
  wrote a standalone Go program proving parse/Clone always succeed
  regardless, only *executing* the template that invokes the missing block
  fails. **Fixed**: comment corrected to name the actual failing call
  (`render_cwd_test.go`'s `TestButtonsNewRenderer_WorksFromAnyWorkingDirectory`,
  which really does execute `"base"` through this renderer).
- `web/public/app.js`'s comment explaining why several admin handlers
  force-swap their own fragment instead of relying on the generic banner
  said "no #pos-alert equivalent" — no longer true now that `#pos-alert`
  exists everywhere. **Fixed**: reworded to the actual reason (those
  responses carry an actionable, specific answer a generic banner would
  lose), which still holds regardless of `#pos-alert`'s reach.

Two further nits noted as accepted, out-of-scope pre-existing gaps, not
fixed here:
- `internal/httpx/httpx_test.go`'s `TestRenderWithBaseOmitsPageTitleHeader`
  builds its own minimal file set and executes `"base"`, but only asserts
  on a header unrelated to whether that execution actually succeeded — so
  it would not catch a missing-partial regression. Pre-existing, unrelated
  to this diff.
- Cosmetic-only: `#pos-alert`, when shown, now renders above the page's own
  title/banners rather than interleaved among them as it was on the three
  original pages. No test or help topic depends on the old position.

## Deferred / non-goals

- No change to `showAlert()`/`app.js`'s dispatch logic itself — the
  mechanism already worked app-wide (ut-docs#213); this card only fixed
  which pages have the DOM element for it to fill in.
- No manual/help topic update — this is internal template-wiring
  infrastructure, not a new or changed screen a shop owner sees.
