# Code review — page titles ignored the operator's locale (ut-docs#2297)

- **Date:** 2026-09-16
- **Ticket:** ut-docs#2297 (`complexity:easy`, split out of ut-docs#2296's
  larger German-UI bug report — see that card's close-out comment for the
  full split)
- **Branch:** `fix/2297-page-titles-locale`
- **Reviewer:** independent pass, fresh-context Sonnet subagent (per this
  card's `complexity:easy` routing — a clean instance that never saw the
  dev reasoning, isolated worktree, TDD-reverified for real).
- **Verdict: SAFE TO MERGE.** No blocking findings; two should-fix nits,
  both folded into this diff before merge; one tooling observation left
  as a documented, non-blocking follow-up.

## The bug

`internal/pages/*.go` set `"title": "<English literal>"` in ~48
`map[string]any` template-data maps that `web/ui/layouts/*.html` renders
verbatim as `<title>{{ .title }}</title>` — no `{{ T }}` involved. Every
page's `<title>` (browser tab, Android task switcher) stayed in English
regardless of the operator's selected locale, even on a fully-translated
page. Part of a real product-owner bug report: a German pilot shop saw
English text throughout Settings.

## What shipped

- Every static `"title"` literal replaced with
  `httpx.T(httpx.RequestLocale(r), "page.title.<name>")` — the existing,
  already-established handler-side translation pattern (same shape as
  `common.LocalizedError`'s keys and `fmt.Sprintf(httpx.T(locale, "elevation.summary.*"), ...)`
  in `backup_api.go`/`data_api.go`/`eod_api.go`), applied to a namespace
  it hadn't covered yet — no new i18n mechanism.
- The one dynamic title (`journal_page.go`'s per-receipt page,
  `"Receipt " + sale.ReceiptNo`) now uses
  `fmt.Sprintf(httpx.T(httpx.RequestLocale(r), "page.title.receipt_detail"), sale.ReceiptNo)`,
  with `page.title.receipt_detail` = `"Receipt %s"` (and translated
  equivalents, each with exactly one `%s`).
- `index_page.go`'s `"title": "Universal Till"` deliberately kept as a
  hardcoded literal (brand name, stays Latin in every locale — same
  convention as `EAN13`/SKU codes elsewhere in this codebase) and marked
  `// i18n:ignore` so it doesn't trip the new guard check.
- 48 new `page.title.*` keys added to all 4 core locale files
  (`web/locales/{en,ar,fa,tr}.json`) with real translations — reused
  existing, already-translated terminology wherever a precedent key
  existed for the same concept (`nav.settings`/`nav.help`/`nav.designer`/
  etc., `categories.title`/`users.title`/`refund.title`/etc.,
  `menu.group.administration`, `vouchers_import.title`) rather than
  inventing new wording, for terminology consistency with the rest of
  each page.
- `scripts/ci/guard-i18n.sh` extended with a new check 10: flags a
  hardcoded `"title": "<Capitalized>"` literal in `internal/pages/*.go`
  (excluding `// i18n:ignore`-marked lines and `_test.go` files, matching
  the existing exclusion convention checks 1–3 already use), so this
  can't silently regress. New regression test
  `scripts/ci/guard-i18n_title_test.sh` (TDD-verified), wired into
  `.github/workflows/ci.yml` alongside the other `guard-i18n_*_test.sh`
  steps.
- New `TestCategoriesPage_TitleFollowsLocale` in
  `internal/pages/categories_page_test.go`: asserts `/categories` renders
  `<title>Categories</title>` in English and `<title>الفئات</title>`
  (never the English string) under `?lang=ar`.

## What the independent review found

Ran the full gate itself, independently: `go build ./...`, `go vet ./...`,
`gofmt -l`, `guard-i18n.sh` (including the new check), the new
`guard-i18n_title_test.sh`, `guard-data-access.sh`, `shellcheck` (0.9.0,
matches the pinned baseline) on both touched shell scripts, and the full
`go test ./internal/pages/...` (all sub-packages) — all green from its own
run, not the dev's report.

**Both TDD claims independently reproduced live:**
1. Reverted `categories_page.go`'s fix back to the hardcoded literal, ran
   `TestCategoriesPage_TitleFollowsLocale` — failed with exactly the
   predicted symptom (`ar locale: title still rendered in English`).
   Restored, reran — passes.
2. Neutralized check 10's regex in `guard-i18n.sh`, ran
   `guard-i18n_title_test.sh` — its "expect a hardcoded title to be
   rejected" case failed exactly as predicted (a false pass). Restored
   the regex, reran — all 5 cases green.

**Diff-vs-summary verification**: confirmed (not just trusted) that all
48 sites compile with `r` in scope; `page.title.receipt_detail` carries
exactly one `%s` in all four locale files; `index_page.go`'s brand-name
line is the *only* remaining hardcoded English title literal outside
`_test.go` fixtures; all four locale files carry the identical 48-key
set (independent Python key-set diff, no orphans/missing); spot-checked
AR/FA/TR translations for script/meaning sanity; zero disk-I/O in the
diff (this pipeline's two recurring bug classes — missing
`os.MkdirAll`, a cwd-relative path where `paths.Data(...)` belongs — do
not apply); no real client/shop names, no secret-shaped literals.

**Findings, both folded in before merge:**

1. **should-fix** — `help_page.go`, `items_page.go`, `menu_page.go` each
   called `httpx.RequestLocale(r)` a second time for the title line, even
   though an identical locale value was already computed a few lines
   earlier in the same closure (`locale := httpx.RequestLocale(r)` in
   `items_page.go`/`menu_page.go`; `locale := httpx.ResolveLocale(w, r)`
   in `help_page.go` — same resolution algorithm, `RequestLocale` is
   documented as `ResolveLocale` minus the cookie side effect, so the
   value is identical). Not a correctness bug — same result either way —
   just a missed reuse of an existing local. **Fixed**: all three now
   read `httpx.T(locale, "page.title.<name>")`.
2. **nit, not fixed, documented instead** — check 10's regex
   (`^\s*"title":\s*"[A-Z]`) has narrow false-negative gaps: a future
   hardcoded title starting lowercase, or a single-line map literal, or a
   `"title": someVar + "Literal"` shape, would slip past it silently.
   Confirmed via full sweep that no *current* site is missed — this is a
   theoretical gap for future code, not a live bug today. Left as a
   documented limitation rather than widening the regex now (risk of new
   false positives on unrelated code outweighs closing a gap with no
   live instance); a tighter check is a reasonable future follow-up if
   the team wants one.
3. **inherited, not introduced by this diff** — `page.title.pay_at_counter`'s
   Arabic value ("pay at the table") is copied verbatim from the
   pre-existing `kiosk_counter_orders.title` key
   (`web/locales/ar.json`), which itself already reads this way. Not a
   regression this diff created; a pre-existing translation-quality
   question for `kiosk_counter_orders.title`, out of this card's scope.

After folding in finding 1: re-ran `go build`, `go vet`, `gofmt -l`,
`guard-i18n.sh`, and the targeted package tests
(`TestHelp*`/`TestItemsPage*`/`TestMenuPage*`/`TestCategoriesPage_TitleFollowsLocale`)
— all green again, no behavior change (both `RequestLocale` and
`ResolveLocale` resolve to the same value; this was a pure dedup).

## Verified beyond automated tests

- Grepped the full diff for `os.`/`filepath`/`ioutil`/`exec`/`MkdirAll`/
  `paths.Data`/`WriteFile`/`Create` — zero hits; this diff touches no
  disk I/O.
- Manually walked every one of the ~48 changed call sites to confirm `r`
  (the `*http.Request`) is a real parameter in scope at each — not just
  relying on `go build` succeeding.
- No UI/template files touched — this is a Go-side data-map change only,
  so `reference/ux-guidelines.md`'s checklist (design tokens, RTL layout,
  kiosk modal-blocker check) doesn't apply; no help-topic content
  describes page `<title>` text, so no manual update owed.

## Explicitly deferred (not this card, noted on ut-docs#2296's split)

- DE/ES pack translations for the new `page.title.*` keys — ut-docs#2301.
- `release.yml` lang-pack-drift release gate — ut-docs#2298.
- Plugin-update scheduler trigger-on-first-start — ut-docs#2299.
- `scripts/ci/audit-locale-render.sh` — ut-docs#2300.
- Check 10's narrow regex gaps (finding 2 above) — noted here, no
  separate card filed (theoretical, no live instance).
