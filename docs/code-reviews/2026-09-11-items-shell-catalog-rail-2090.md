# Code review: items shell top actions keep the left rail (ut-docs#2090)

**Date:** 2026-09-11
**Author:** Sonnet (implementation), independent review by Opus (fresh-context subagent)
**Card:** ut-docs#2090 — "Catalog top actions leave the /items shell — the
left rail is lost, and the way back lands on a railless page"
**Follow-up:** ut-docs#2095 (Import/Tax-codes still-outbound-navigation
piece, deliberately split out of this card — see below)

## What shipped

The product owner reported that pressing one of `/catalog`'s top-action
buttons (Import, Tax codes, Modifiers, Option sets) from the `/items`
master-detail shell lost the left rail entirely — each was a plain
full-page `<a href>`, not an htmx in-panel swap — and each destination's
only way back pointed at bare `/catalog`, which also renders standalone
(no rail), so it was a one-way door out of the shell. This matters more on
kiosk hardware, which has no browser chrome or hardware Back.

Modifiers and Option sets are already `/items` rail sections
(`itemsnav.Resolve`'s five rows) — their handlers already answer an htmx
fragment request with the rail's own OOB swap (ut-docs#1950). The fix
wires `catalog.html`'s own buttons to use that same existing mechanism
(`hx-get`/`hx-target="#items-panel"`/`hx-push-url`), conditioned on a new
`.InItemsShell` template flag that is true only when the response is an
htmx fragment swap actually happening inside the shell (`httpx.IsFragmentSwap(r)`
on the `/catalog`, `/modifiers` and `GET /catalog/option-sets` handlers).

Every one of the four destinations' back-links (including the
easy-to-miss *second* one — Modifiers' empty-state "Open Catalog" CTA,
caught in review) now points at `/items` (the shell itself, default
section Catalog) instead of bare `/catalog`; for the two rail sections
this is further upgraded to an in-panel swap back to Catalog when already
inside the shell.

Import and Tax codes are **not** rail sections (neither has a
fragment-render mode at all today) — giving them the same in-panel/
full-screen-dialog treatment is real, separate scope (a new fragment
render path, plus auditing Import's own wizard state for anything that
assumes it's the page's own top-level content) and is split out to
ut-docs#2095 rather than folded into this card. Their back-links are
still fixed (`/items` instead of bare `/catalog`), so this card does close
the "one-way door" half of the bug for all four; only Import/Tax-codes'
outbound navigation still leaves the shell.

## Files changed

- `internal/pages/catalog/handlers.go` — `InItemsShell` added to the
  `/catalog`, `/modifiers` and `GET /catalog/option-sets` data maps.
  Deliberately **not** added to `renderModifiersList`'s mutation-re-render
  path (see that func's own comment) — a mutation POST always carries
  `HX-Request: true`, so `IsFragmentSwap` would misread standalone-page
  mutations as shell-embedded; the map's own nil-key default (false) is
  the correct fallback there.
- `web/ui/pages/catalog.html` — Modifiers/Option-sets top-action buttons
  conditionally hx-enabled (`id="catalog-modifiers-btn"`/
  `id="catalog-option-sets-btn"` added for stable selectors); Import/
  Tax-codes left as plain links with a comment pointing at ut-docs#2095.
- `web/ui/pages/modifiers.html` — page-head back-link AND the empty-state
  "Open Catalog" CTA both retargeted/upgraded.
- `web/ui/pages/option_sets.html` — back-link retargeted/upgraded; also
  picked up the "←" prefix every sibling back-link already had (review
  finding, pre-existing, on the exact line this diff touches).
- `web/ui/pages/tax_codes.html`, `web/ui/pages/import.html` — back-link
  href only (`/catalog` → `/items`), no hx- attributes (not rail sections).
- `web/ui/pages/items.html` — the narrow-width (<52rem) capture-phase
  fallback handler's selector widened from `a.items-row` to also match
  `a[hx-target="#items-panel"]`, so the new buttons/back-links get the
  same "fall back to plain navigation at narrow width" treatment the
  rail's own rows already have (review finding — see below).
- `internal/pages/catalog/modifiers_shop_page_test.go` — existing exact-
  string back-link test updated for the new href.
- `internal/pages/catalog/items_panel_test.go` — two new tests covering
  both the positive (htmx-fragment, hx-enabled) and negative (bare page,
  plain link) branches of the top-action buttons (review finding — the
  negative branch had zero coverage before this).
- `e2e/tests/items-shell-catalog-top-actions-rail-2090.spec.ts` — new
  Playwright regression: rail survives Modifiers/Option-sets from `/items`,
  each back-link returns in-panel, and a bare `GET /modifiers` still
  renders standalone with no rail.
- `web/help/img/**` — `make docs-shots` regenerated (guard-docs-shots.sh
  hashes `web/ui/**`/`internal/pages/**.go`, both touched here).

## Independent review (Opus, fresh context)

Full pass on the diff (not just a description of it) — ran `gofmt`,
`go build`, `go vet`, the affected Go test packages, `golangci-lint`, and
`guard-i18n`/`guard-docs-shots`/`guard-help-topics`/`guard-help-drift`/
`guard-e2e-fixtures-import` itself, then read every touched file plus the
templates/handlers around them. Findings, and what happened to each:

1. **MUST FIX — Modifiers' empty-state CTA still pointed at bare
   `/catalog`.** `modifiers.html`'s own comment (present before this
   diff) explicitly named the page-head link and the empty-state card as
   the *two* ways back to Catalog; the first pass fixed only the head
   link. For a brand-new shop with zero modifier groups, the empty-state
   card's "Open Catalog" button is the only, most prominent control on
   the page — reproducing the exact reported bug for exactly the shop
   state a new merchant is in. **Fixed** — same `.InItemsShell`-conditioned
   treatment as the head link, with an explicit warning comment (in both
   the template and `renderModifiersList` itself) against blindly adding
   `InItemsShell` to the mutation-re-render path, since that would
   incorrectly read `IsFragmentSwap` as true for a standalone-page
   mutation.
2. **Gap in test coverage — nothing pinned the negative (non-shell)
   branch.** Every existing test only exercised the htmx-fragment case; a
   dropped or inverted `.InItemsShell` guard would have shipped two
   silently dead buttons on bare `/catalog` (htmx `preventDefault()`s a
   click carrying `hx-get` before resolving `hx-target`, so a missing
   target is a silent no-op, not a visible failure) with the whole
   existing suite staying green. **Fixed** — two new Go tests
   (`TestCatalogPage_NonHXRequest_TopActionButtonsAreNotHXEnabled` /
   `..._HXRequest_TopActionButtonsAreHXEnabled`) pin both branches
   directly on the button markup.
3. **Narrow-width (<52rem) had no fallback for the new buttons.** The
   `/items` shell's existing capture-phase handler only special-cased
   `a.items-row` (the rail's own rows); the new buttons/back-links swap
   the same `#items-panel` but weren't covered, so at phone width the
   result was functional but incoherent — the rail's own Modifiers row
   did a full navigation while catalog.html's own Modifiers button did an
   in-panel swap, same destination, two different screens. **Fixed** —
   widened the handler's selector to `a.items-row, a[hx-target="#items-panel"]`,
   a plain attribute-equals addition with no behavioural change to what
   `a.items-row` already covered. Manually verified at 400×700: the click
   now falls back to a real navigation to the standalone page, matching
   the rail row's own behaviour.
4. **Wrong follow-up issue number in the e2e spec's own comment**
   (said #2092, everywhere else said #2095). **Fixed** — typo, no
   behavioural effect (it was in a comment only).
5. **Other in-shell exits the ut-docs#2095 follow-up should also
   enumerate** (not part of this card's own scope, but the same bug
   class): `catalog_variants.html`'s "manage option sets" link inside the
   item editor, `modifiers.html`'s per-item `/catalog?item=` link, and
   `import_page.go`'s post-commit "View catalog" link (now visibly
   inconsistent with the same page's own head back-link, which this diff
   retargeted). **Added to ut-docs#2095's issue body** as a follow-up
   comment rather than folded into this diff — none of the three are the
   reported bug and each needs its own design pass (the variants-panel
   one has no `InItemsShell` in its own render path at all today).
6. **Confirmed correct, no action needed:** the whitespace-preservation
   claim on the `{{ if .InItemsShell }}` conditionals (verified byte-exact
   with `cat -A` and by the passing exact-string test); the two rail-section
   handlers' `InItemsShell` wiring is consistent with each other and with
   `/catalog`; the history-restore path stays on the standalone branch;
   Import/Tax-codes carry no stray hx- attributes; i18n guard clean (no
   new user-facing strings — only attributes/ids/comments); the new e2e
   spec is not a false-pass (each assertion genuinely distinguishes fixed
   from broken, verified by re-deriving what the broken state would render).
7. **Noted, not actioned:** the option-sets back-link's missing "←" was
   pre-existing but on a line this diff already touches — fixed alongside
   the retarget rather than left as a separate concern (see file list
   above). "← Catalog" now lands on a page titled "Items" rather than
   strictly back-where-you-came-from when reached from a *standalone*
   `/catalog` visit — deliberate and net more navigable, called out as an
   accepted, non-surprising trade-off rather than a defect.

No blocker-class issue (money/tax/data-loss/security) was found, so per
this repo's model-routing rules a second review round was not required —
this record covers the one round, its finding (1 must-fix), and the fix.

## Verified beyond automated tests

- `go build ./...` / `go vet ./...` / `gofmt -l .`: clean.
- `go test ./...` (full suite, twice — before and after the review's
  fixes): green, no regressions.
- `golangci-lint run ./internal/...`: 0 issues.
- CI-blocking guards run directly: `guard-i18n.sh`, `guard-docs-shots.sh`
  (regenerated via `make docs-shots`, 120 screenshots across 30 topics ×
  4 locales, real headless-Chromium run), `guard-help-topics.sh`,
  `guard-help-drift.sh`, `guard-data-access.sh`, `guard-page-http-error.sh`,
  `guard-kiosk-engine.sh`, `guard-htmx-loaded.sh`, `guard-compliance-claims.sh`,
  `guard-plugin-menu-read.sh`, `guard-emoji-font.sh`,
  `guard-autofill-suppression.sh`, `guard-e2e-fixtures-import.sh`,
  `guard-makefile-version.sh`, `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `check-brand-assets.sh` — all green.
  `guard-shellcheck-version.sh` could not run in this sandbox (no
  `shellcheck` binary present) — no shell script was touched by this
  diff, so this is an environment gap, not a finding; real CI runs it
  with its pinned `shellcheck`.
- **Real driven verification against the live app**, twice (before and
  after the review's fixes): booted the actual e2e till
  (`e2e/run-till.sh`), drove the new Playwright spec plus 6 adjacent
  catalog/import/tab-strip specs (25 tests total) — all green — and, by
  hand via a throwaway script, confirmed the narrow-width (400×700)
  fallback actually falls back to a real navigation rather than an
  in-panel swap.
- Not verified: the physical pilot tablet viewport (1280×800) or RTL
  rendering on real hardware — no device available in this cloud sandbox,
  consistent with this card's own scope (a routing/rendering-mode fix,
  not a layout change).
