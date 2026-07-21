# 2026-07-22 — Keyword search over content bundle entries

## Context
Spec-audit gap (FR-005/SC-003, `ut-plugin-faq` spec 001-multilingual-faq-page):
"Users MUST be able to browse FAQs by category and search by keyword" —
each entry's JSON already carries a `keywords` array, parsed nowhere;
`plugin_content.html` had only category accordions, no search input.

## Changes
- `contentBundle`'s entry struct gained `Keywords []string`.
- `bundleView`'s `entryView` gained `Search string`: a lowercased
  `question + answer + keywords...` haystack, computed once per entry.
- `plugin_content.html`: a `#plugin-content-search` input (shown only when
  the bundle has categories); each `.plugin-content-entry` carries
  `data-search="{{ .Search }}"`; an inline IIFE `<script>` filters on
  `input`, hiding entries whose haystack doesn't contain the (lowercased)
  query substring and hiding a whole `.plugin-content-category` section if
  every entry inside it is hidden. Mirrors the existing, already-shipped
  `#catalog-search` pattern in `catalog.html` (same hide-via-`.hidden`
  approach), applied to the plugin-content classes instead.
- New i18n key `plugin.content.search_placeholder` in all 4 core locales,
  inserted as a single line each (learned from the previous fix in this
  same session not to round-trip a locale file through a generic
  `map`+re-marshal — verified independently by the reviewer this time,
  each diff is a clean `+1` line).
- Test: `TestPluginPage_KeywordsAreSearchable` — seeds an entry whose
  question/answer do NOT contain "barcode", only its `keywords` array
  does, and asserts the rendered `data-search` attribute carries it,
  proving keywords are actually indexed, not just parsed and dropped.

## Independent review
One review pass (line-by-line JS/logic trace of the new script and
`Search` construction; cross-repo CSS/class reuse check; direct
re-verification of the locale-file diff hygiene). Findings and
disposition:

**Fixed:**
- Reviewer confirmed the search input had zero matching CSS (unlike
  `#catalog-search`, which gets `min-width`/flex placement from
  `.page-head`) — would have rendered at the browser's tiny default
  search-input width, stacked alone above the card. Added a
  `.plugin-content-search` rule in `app.css`.

**Considered, deliberately not changed:**
- *Search input isn't inside the `{{ if .bundle.RTL }}dir="rtl"{{ end }}`
  div, so it doesn't inherit that attribute directly.* Reviewer's own
  trace confirmed this is very likely fine in practice: the page's
  `<html dir="...">` is already set from the resolved UI locale
  (`internal/httpx`'s `dir()` template func), which normally agrees with
  `bundle.RTL` since the bundle is chosen based on that same locale. Only
  a genuine locale/content-language mismatch (bundle fell back
  cross-language) could disagree, and even then the search placeholder
  text itself is UI-locale text, not content-locale text — correct as
  designed, not a bug.
- *Turkish dotted/dotless I casing*: neither Go's `strings.ToLower` nor
  JS's `String.prototype.toLowerCase()` implement Turkish-specific
  casing rules, so a dotless-ı-based query could in theory miss a
  keyword. Real but niche (would need a specific Turkish keyword +
  specific query casing to trigger); not fixed — would need a
  locale-aware casing library on both the Go and JS sides for a fix that
  actually holds together, disproportionate to the gap.
- *No live-browser/headless test of the actual JS filtering behavior* —
  correct gap (Go tests can only prove the markup/attributes are right,
  not that the script executes correctly in a real DOM). Checked: no
  Playwright/Puppeteer harness exists elsewhere in this repo to hook into
  cheaply. Mitigated instead by `node --check` confirming the extracted
  script is syntactically valid, and the logic being a near-line-for-line
  copy of `catalog.html`'s already-shipped, working `#catalog-search`
  pattern (same hide-via-`.hidden`, same "re-show on empty query"
  short-circuit). Building new e2e infra for one small feature is out of
  proportion here; flagging as a known verification gap rather than
  rushing browser automation together under time pressure.
- *No regression test pinning `data-search`'s HTML-attribute escaping for
  question/answer/keyword text containing quotes* — reviewer traced this
  as already safe (`html/template`, not `text/template`, is in use
  throughout this renderer, confirmed earlier in this same session's
  checksum-verification review), just untested. Not added under time
  pressure; low risk since the escaping guarantee is structural (which
  template package is imported), not per-call-site.

## Verification
`go build ./...`, `go test ./...`, `scripts/ci/guard-i18n.sh`,
`scripts/ci/guard-data-access.sh` — all green, including the 8
`TestPluginPage_*` cases.
