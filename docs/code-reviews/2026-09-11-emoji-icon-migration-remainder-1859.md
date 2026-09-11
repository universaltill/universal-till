# Code review: finish the emoji-to-icon migration outside the Menu screen (ut-docs#1859)

**Date:** 2026-09-11
**Card:** ut-docs#1859 — follow-up to #1845 (Menu screen icon set), found by
that card's own independent review, closing the surfaces it deliberately
left out of scope: the sale/tender screen, the catalog variant editor, the
shared statusbar chrome, and the bug-report panel.

## BA finding: the card's own claims needed re-verifying against current code

Several of #1859's per-file bullets had drifted since it was filed
(2026-09-09):

- `web/ui/partials/bugreport_panel.html`'s screenshot-capture button was
  claimed to carry a bare `📷` at "lines 68, 98" — those lines actually
  interpolate `{{ T "issuereport.screenshot_capture" }}`, and the glyph
  lived **inside the locale string value itself**
  (`web/locales/{en,fa,tr,ar}.json`), not the template. Rescoped to fix the
  locale strings, not the markup at those line numbers.
- `web/ui/layouts/base.html`'s "self-update banner/close controls...
  ✦/⬆/✕" bullet doesn't have a literal `✕` anywhere in that file — the `✕`
  the card meant is `bugreport_panel.html`'s own close button, grouped
  under `base.html` in the card's phrasing because that partial is part of
  the shared layout chrome `base.html` renders. Confirmed via
  `internal/pages/menu_page_test.go`'s own denylist-scope comment, which
  names exactly this set. Fixed on the partial that actually carries it.
- The catalog-variants line number (`~105`) had drifted to `143` (same
  element, unrelated edits since filing).

No card content was stale-and-already-fixed; every glyph the card named
was real, just needed the right file/line reconciled against current code.

## What shipped

- `internal/httpx/icons.go`: three new Lucide icons — `camera`, `arrow-up`,
  `sparkles` — paths fetched and verified against Lucide's actual upstream
  source (not typed from memory), same wrapper/viewBox convention as every
  existing entry.
- `web/ui/pages/index.html`: the AI-identify camera button (gained an
  explicit `aria-label` to match its sibling `barcode-scan-open`, since its
  only visible content is now an `aria-hidden` icon), the tender-row New
  Sale button in both places it renders (the default footer and the
  payment-overlay's own duplicate copy — the overlay copy already has its
  own `aria-label` so its accessible name is unaffected, but leaving one
  copy an icon and the other an emoji would have been a visible
  inconsistency between two identical-looking buttons). The quick-pay row's
  `⚡` prefix was purely decorative (unlike `🛒`, no semantic content) and
  was dropped rather than iconified — one less icon to maintain for zero
  information lost.
- `web/ui/partials/catalog_variants.html`: the variant-image upload label's
  camera glyph.
- `web/ui/layouts/base.html`: the statusbar's register-till (`✦`) and
  self-update/plugin-update (`⬆`) chips, reusing the existing `.btn-ico`
  class as-is (its `1em` SVG sizing already tracks whatever font-size
  `.sb-item` renders at — no new statusbar-specific CSS needed). Two of the
  self-update chips are also overwritten by inline `<script>` during a
  two-click confirm/re-arm flow (the Android-bridge and desktop update
  paths) — that JS switched its label capture/restore from `.textContent`
  to `.innerHTML`, since a `.textContent` restore would have permanently
  wiped the new icon `<span>` the first time either flow ran (transient
  in-flight states — confirm/downloading/locked/failed/etc. — stay plain
  text, matching the `⚡`-drop simplification above).
- `web/ui/partials/bugreport_panel.html`: the close button (`✕`, aria-label
  already present) and the screenshot-capture button (camera icon added in
  the template; the `📷 ` prefix stripped from the locale value instead of
  the markup, per the BA finding above).

Downstream fixes so nothing silently broke:

- `e2e/tests/payment-overlay-duplicate-labels-1625.spec.ts`: the tender-row
  New Sale button's accessible name changed from `"🛒 New Sale"` (the emoji
  was literal, non-`aria-hidden` text before) to plain `"New Sale"`.
  Updated the assertion and the comment explaining why.
- `e2e/tests/android-screenshot-bridge-1435.spec.ts`: asserted the old
  `"📷 Take screenshot"` full text.
- `internal/pages/index_quickpay_test.go`: asserted the now-dropped `⚡`
  prefix on both the first-active-method and preferred-method quick-pay
  labels.
- `internal/httpx/template_helpers_test.go`: the no-actionable-link update
  chip test sliced the chip's markup on the first `</span>` it found after
  the chip's opening tag — now the icon's own closing span, not the
  chip's. Fixed to skip past the inner icon span first.
- `internal/pages/menu_page_test.go`: updated a doc comment that
  enumerated these exact glyphs as still-open/out-of-scope for #1845 — now
  historical (they're fixed by this PR), reworded so it doesn't read as a
  still-open list.
- `android/README.md` and `web/help/{en,de,fa,tr,ar}/{quickstart,
  bug-reporting}.md`: prose quoting the now-removed emoji in a button name
  (`de` has a help manual for the German pilot despite not being a core UI
  locale — its `web/locales/de.json` doesn't exist, ships via the external
  `ut-plugin-language-de` pack — so only its help prose needed the edit,
  no locale JSON or screenshot for it).
- `web/help/img/**` + `manifest.json`: `make docs-shots` regenerated all
  108 images (27 topics × 4 core locales) — `guard-docs-shots.sh` hashes
  the whole `web/ui/**` surface as one blob, and `base.html` (shared
  layout, rendered on every page) changed, so the entire surface counts as
  stale by that guard's own design, not scope creep.

Explicitly out of scope, confirmed untouched: `payment-overlay-close` and
the other `✕` close buttons across `index.html`/`catalog_variants.html`,
`base.html`'s PSU-underpowered `⚡` chip, and the per-screenshot-thumbnail
`✕` remove button in `bugreport_panel.html` — none named by #1859 or by
`menu_page_test.go`'s denylist comment.

## Independent review (fresh-context Sonnet, isolated worktree)

Spawned per `complexity:easy` routing (Sonnet wrote it, reviewed by a
fresh-context Sonnet instance that never saw the dev reasoning). Verdict:
**SAFE TO MERGE AS-IS** — no blocker or should-fix findings.

Ran independently and confirmed green: `go build ./...`, `go vet ./...`,
`golangci-lint run ./...` (0 issues), `gofmt -l .`, `go test
./internal/httpx/... ./internal/pages/...`, and
`guard-emoji-font.sh`/`guard-i18n.sh`/`guard-help-topics.sh`/
`guard-help-drift.sh`/`guard-docs-shots.sh`.

Specifically verified (not just read):

- All 4 edited locale JSON files parse, key counts match, exactly one line
  changed per file (the emoji prefix), no duplicate keys introduced.
- The 3 new Lucide icon paths are **byte-for-byte identical** to upstream
  Lucide source, fetched directly during the review.
- The `.textContent`→`.innerHTML` switch in `base.html`'s self-update JS
  carries no XSS risk: every `.innerHTML =` assignment restores a value
  captured earlier from the button's own server-rendered (auto-escaped)
  markup, never concatenated with anything dynamic; the one spot that
  builds a string dynamically (`'Confirm update to v' + latest`) still
  uses `.textContent`, unchanged by this diff. All pre-existing restore
  call sites (locked/failed/uptodate/nobridge/visibilitychange-back) were
  converted consistently — none missed.
- The two Go test edits are honest fixes matching real, intentional
  behavior changes, not weakened/false-pass assertions.
- Accessible-name reasoning is correct per the accname algorithm
  (`aria-hidden` content excluded from computed name); no leftover
  reference to the old `"🛒 New Sale"` string anywhere in the repo.
- Every item on the "explicitly out of scope" list above is genuinely
  untouched (grepped/read directly).
- No secrets, credentials, or real client/shop names anywhere in the diff.

Flagged, not a blocker: the reviewer can fully judge the English help-doc
prose but not the de/fa/ar/tr wording quality itself (only checked for
encoding/RTL-mark corruption and that nothing beyond the emoji mention
changed) — a native speaker should still spot-check those 4 files' one
changed line each.

## Verified beyond automated tests

Real, Chromium-driven re-runs (not just the unit-test level) of every e2e
spec touching these surfaces, all passing:
`payment-overlay-duplicate-labels-1625`, `android-screenshot-bridge-1435`,
`bugreport-panel`, `camera-error-branching-ai-identify-1559` (dedicated
`ai-identify` project), `catalog-item-form-1956`,
`catalog-thumbnail-no-request`, `osk-decimal-sale-catalog-fields-1284`,
`new-sale-closes-payment-overlay-1386`,
`payment-overlay-focus-obscured-1629`/`1674`,
`payment-overlay-focus-sweep-1702`, `payment-overlay-footer-reachable-1542`,
`phone-width-layout-413`, `sale-screen-213`, `settings-osk` — 88 specs
total across the `default` and `ai-identify` Playwright projects.

## Safe-to-merge verdict

Yes. Independent review found nothing blocking; full gate (build/vet/lint/
test/every named CI guard) green; real browser-driven e2e re-run green.

## Explicitly deferred (new Backlog-worthy items, not filed as cards yet)

- Native-speaker spot-check of the one changed prose line in
  `web/help/{de,fa,ar,tr}/{quickstart,bug-reporting}.md` — flagged by
  the independent review, not a defect found, just unverified by either
  Sonnet instance that touched this change.
