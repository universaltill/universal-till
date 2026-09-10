# Code review: translations page server-side paging + infinite scroll (ut-docs#2014)

**Date:** 2026-09-10
**Card:** ut-docs#2014 — "Translations page renders all 2,258 keys in one
table — needs server-side paging with infinite scroll"

## What shipped

`internal/pages/translations_page.go`'s `renderTable` built a row for every
matching i18n entry (`web/locales/en.json` alone is ~2,261 keys) with no
limit/offset, and `web/ui/pages/translations.html` re-ran that same full
render on every debounced search keystroke.

- `GET /ui/translations-table` now takes `offset` and `limit` and returns
  one page (`translationsPageSize` = 100 rows by default, `?limit=` is
  accepted up to a `translationsMaxPageSize` cap of 500 — clamped, not
  silently ignored, if a caller asks for more).
- The filter (`filteredEntries`) still runs over the *whole* key set before
  slicing into a page — search is never limited to whatever happens to be
  loaded already.
- `web/ui/partials/translations_table.html` renders either the full table
  wrapper + first page + a `hx-trigger="revealed"` sentinel `<tr>`
  (`offset=0`), or just the next page's rows + a new sentinel
  (`offset>0`, an "append" fragment with no repeated wrapper).
- `POST /api/translations/set` and `/clear` now re-render only the one row
  that changed (`renderRow`), not the whole table — the old
  `hx-target="#translations-table" hx-swap="outerHTML"` behaviour would
  have discarded every page loaded beyond the first on every save, which
  paging makes an actual regression rather than a harmless no-op.
- New i18n keys `translations.loading` / `translations.end_of_list` in all
  four core locales (en/ar/fa/tr); manual topic updated in all five
  locales (en/de/ar/fa/tr) with a note on the new scroll/search behaviour;
  `make docs-shots` regenerated (112/112 screenshots).
- `internal/pages/translations_page_test.go`: bounded first page + sentinel,
  append-fragment has no outer wrapper, short/empty results, the true last
  page of the full unfiltered set, `?limit=` clamping, offset-beyond-total,
  an empty append page still showing the end marker, sentinel URL escaping
  (see below), and regression tests proving save/clear return only the
  touched row.

## Independent review (Opus subagent, read-only against the live checkout)

Model routing: `complexity:medium` → built at Sonnet (inline), reviewed at
Opus. The review ran without `isolation:"worktree"` — corrected mid-review
by instructing it not to mutate the shared checkout (no revert-in-place);
all TDD verification instead ran via mutation testing against a `tar`-copy
of the tree in scratch space, or by inspection. No writes reached
`/home/user/universal-till` during the review.

**Verdict: ship with nits, no blockers.** TDD claim independently verified
via a 13-mutation battery against the copied tree (whole-table-swap
regression, `end`/`hasMore`/`nextOffset` off-by-ones, `isAppend` forcing,
the `?limit=` clamp, filter-after-slice) — 11 of 13 mutations were caught by
the existing suite; 2 were not, both closed below with new tests. Full gate
(`gofmt`, `go build ./...`, `go test ./...`, `golangci-lint run ./...`,
`guard-i18n.sh`, `guard-htmx-loaded.sh`, `guard-docs-shots.sh`, and every
other CI-blocking guard except `guard-shellcheck-version.sh`, which fails
only because this sandbox has no `shellcheck` binary — an environment gap,
not a regression; the diff touches no shell scripts) independently
re-run and green.

### Findings and disposition

| # | Finding | Severity | Fixed? |
|---|---|---|---|
| 1 | Offset clamp (`offset > len(all)`) has no regression test; removing it panics on a slice bound | should-fix | **Fixed** — `TestTranslationsTable_OffsetBeyondTotalClampsInsteadOfPanicking` |
| 1b | An append page that happens to be empty (set shrank between sentinel render and follow-up) rendered nothing — silent truncation, not the AC's "real end state" | nit (real AC gap in a narrow window) | **Fixed** — the end-of-list condition is now `not hasMore AND (rows non-empty OR isAppend)`; `TestTranslationsTable_AppendWithNoRowsStillShowsEndOfList` |
| 2 | No test exercised the sentinel URL's escaping (the review's own headline risk) — escaping itself was verified correct by execution, but nothing in the suite would have caught a regression | should-fix | **Fixed** — `TestTranslationsTable_SentinelURLEscapesAdversarialQuery`: constructs a query containing `&`, `"`, `'`, `<script>`, follows the *actual* sentinel URL the server emitted, and asserts the round-tripped `q` matches exactly |
| 3 | `translations_row.html`/`translations_table.html` duplicated ~25 lines of identical row markup with nothing to catch drift | should-fix | **Fixed** — `translations_row.html` deleted; `renderRow` now calls `translations_table.html` itself with a new `rowOnly` flag that suppresses the wrapper and the sentinel/end-of-list row, so there is exactly one copy of the row markup |
| 4 | CSS comment claimed `.muted` centers the loading/end rows (it only sets color); `.translations-end` was on the `<tr>` while `.translations-loading` was on the `<td>` — `padding-block` on a table-row box is a no-op in every browser | should-fix | **Fixed** — `.translations-end` moved onto its `<td>` (matching `.translations-loading`'s placement) and the comment rewritten to state what the rule actually does |
| 5 | No `aria-live`/`role="status"` on the loading indicator; no "Load more" button fallback for a user who can't generate a scroll event | nit | **Partially fixed** — `aria-live="polite" role="status"` added to the loading cell; a manual "Load more" fallback control is a larger UX addition, deferred |
| 6 | The value `<input>` has no `id`, so htmx's focus/selection restoration after a swap (which requires one) never fires on save — pre-existing behaviour, but now cheap to fix since only the one row swaps | should-fix (small, directly serves an AC) | **Fixed** — `id="translations-value-{{ .Key }}"` added |
| 7 | Offset-based paging can skip/duplicate a row if the filtered set's length changes between a sentinel's render and the client following it (concurrent plugin install/uninstall, or a different manager's edit) | nit, low impact, real | **Deferred** — documented in a code comment at the paging logic, tracked as ut-docs#2019 (a keyset/cursor pager is a real design change, not a one-line fix) |
| 8 | `?limit=` was speculative: nothing in-tree set it, and the sentinel didn't propagate it, so a custom first-page limit silently reverted to the default from page 2 on (and a non-page-aligned custom limit could produce duplicate rows) | nit | **Fixed** — the sentinel's own `hx-get` now carries `&limit={{ $.limit }}` (the *effective* limit used to build the current page), so a custom `?limit=` stays consistent across every later page |
| 9 | `web/ui/pages/translations.html`'s own initial `hx-get` still interpolates `editLocale` unescaped (pre-existing, not exploitable — `editLocale` only ever comes from a locale `<select>`) | nit, out of scope | Not fixed — pre-existing, unrelated to this diff's own new code, noted for awareness only |

### Measured "measurably faster" (AC requirement, not just "feels better")

Same process, `newTranslationsTestDeps`, 20 warm iterations each, averaged:

| | avg render | response body |
|---|---|---|
| whole set (old behaviour, cap lifted) | 34.3 ms | 1,779,606 bytes |
| paged, default 100 rows (new) | 3.2 ms | 74,383 bytes |

**≈10.6× faster, ≈24× smaller** on first load. (Measured independently by
both the implementation pass and the review pass; the two runs agree to
within measurement noise.)

## Verified beyond automated tests

- `go test -race -run Translations ./internal/pages/...` — clean.
- `go test ./...` (full suite) and `golangci-lint run ./...` — clean.
- `make docs-shots` — real Playwright run against the built server,
  112/112 screenshots captured; `guard-docs-shots.sh` green.
- Manual read-through of the actual rendered sentinel `hx-get` HTML for an
  adversarial search query, confirming no attribute breakout and no
  parameter-injection into `edit_locale`/`offset`/`limit`.

## Deferred / explicitly out of scope

- ut-docs#2019 — offset-based paging's skip/duplicate edge case under a
  concurrent edit (finding #7 above).
- A manual "Load more" button as a screen-reader/no-scroll-event fallback
  (finding #5's second half).
- `web/ui/pages/translations.html`'s pre-existing unescaped `editLocale`
  interpolation (finding #9) — out of scope for this diff.

## Language pack follow-up

`translations.loading` / `translations.end_of_list` are brand-new
`web/locales/en.json` keys, so "land the pack PR first" isn't achievable
(a pack PR would orphan-fail its own key-drift guard against a `main` that
doesn't have the key yet). Per `universal-till/CLAUDE.md` and the
`reviewer` skill: this PR merges first, `lang-pack-drift` is expected red
on `main` until the follow-ups land, and the same lane that merges this
owns landing `ut-plugin-language-de` and `ut-plugin-language-es` follow-up
PRs in the same cycle.

## Safe-to-merge verdict

**Yes.** No blockers found; every should-fix finding above was fixed and
re-verified; the two nits deferred (a UX-fallback control, a pre-existing
unrelated line) are genuinely out of scope for this card.
