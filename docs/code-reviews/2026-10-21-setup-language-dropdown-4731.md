# Setup wizard: language as a drop-down (ut-docs#4731)

**Change.** Step 1 of the first-run wizard shows one `<select id="setup-language">`
(native names, no flags) instead of a row of buttons. The till's current locale
is preselected (the system language on a first run); an unbundled locale falls
back to English instead of the browser picking the first option. Bundled
locales reload `/setup?lang=X` on change. Catalog languages sit in an
optgroup; picking one reveals an Apply button that submits the matching
`#lang-install-<locale>` form.

**Review** (Opus, fresh context, one round): no correctness bugs. Findings,
all fixed:
1. A keyboard arrow on a closed select fired `change` and would have started a
   catalog download unasked (WCAG 3.2.2). Catalog languages now need the Apply
   press; bundled ones still reload (cheap, reversible).
2. Stale `form=` comments on the install forms — rewritten; the in-flight
   disable script now targets the Apply button.
3. Test gap — a test now pins `data-install-locale="de"` ↔ `id="lang-install-de"`.

**Verified.** `go test ./internal/pages` (Setup tests updated: `<option value="tr"`
instead of the button hrefs; new drop-down + English-fallback test, written
first and seen failing). Driven run: fresh till in a temp data dir, Chromium
with a de-DE locale: the drop-down renders, choosing Türkçe navigates to
`/setup?lang=tr` with `tr` selected. The setup wizard has no manual screenshot,
so the docs-shots surface hash was refreshed, not the images.
