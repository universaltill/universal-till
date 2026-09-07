# Code review: printed BY ARTICLE section cap/off setting (ut-docs#1650)

- **Card:** ut-docs#1650, "Day-close print: add a store setting to
  cap/disable the printed 'BY ARTICLE' section for high-SKU shops"
- **PR:** universaltill/universal-till (branch
  `feat/1650-eod-printed-article-cap`)
- **Complexity:** medium (Sonnet built, Opus reviewed)
- **Date:** 2026-09-06

## What shipped

Follow-up from ut-docs#1010's review (finding 2, deferred): the printed
Z-report's "BY ARTICLE" footer section printed every article sold that day
unconditionally — a high-SKU shop (200-400 distinct articles/day) got a
correspondingly long, un-turn-offable roll on every close. Research
(Square, Toast — see the card) confirmed this is a settings decision, not
a business call needing sign-off.

A new per-store EOD setting, `reports.eod_article_print_mode` /
`reports.eod_article_print_cap`, controls **only** the printed section —
the on-screen archived-report list is untouched (always shows every
article behind its own `<details>`):

- `all` — print every article (the pre-#1650 behavior).
- `capped` (**default**, top 30) — print only the top-N articles by
  revenue, followed by a fixed-vocabulary `+M more (see on-screen report)`
  line when truncated (`rep.Articles` already arrives `ORDER BY gross
  DESC`, so `articles[:articleCap]` genuinely is top-N by revenue — no
  extra sort needed).
- `off` — omit the section entirely.

`resolveEODArticlePrintSettings` (`internal/pages/eod_api.go`) normalizes
unset/invalid stored values to the shipped default, so `buildEODDoc`
itself never has to guess at malformed data. `buildEODDoc` gained two
new trailing parameters (`articleMode string, articleCap int`) consumed
only by the BY ARTICLE block — every one of buildEODDoc's ~23 pre-existing
test call sites was updated to pass `eodArticlePrintAll, 0`, reproducing
the old unconditional-print behavior exactly (proved by `go build`/full
test suite passing unchanged).

`POST /api/settings/eod` (existing schedule/business-day-start settings
endpoint) gained validation (`article_print_mode` one of the three known
values; `article_print_cap` a whole number 1-999) and persistence for the
two new fields, plus elevation-hidden-field replay so a manager's approval
dialog retry carries them. The EOD tab's settings form
(`web/ui/partials/reports_tab_eod.html`) gained a `<select>` + `<input
type=number>` reflecting the resolved (defaulted) state. 6 new i18n keys
across all 4 locales (en/ar/fa/tr). User manual (`web/help/*/reports.md`)
updated in the same branch, prose woven into the existing per-article
breakdown section in every locale (not a bolted-on trailing heading).

## Independent review (Opus, isolated worktree) — findings and disposition

Ran the full gate from scratch (`go build`/`go vet`/full `go test ./...`
— 50 packages green — `golangci-lint` 0 issues, `guard-i18n`,
`guard-data-access`, `guard-help-topics`, `guard-docs-shots`,
`guard-compliance-claims`, plus `guard-page-http-error`/
`guard-autofill-suppression`/`guard-htmx-loaded` since a new `<input>` was
added) and re-verified the capping logic with **behavioral mutations**
(not just revert-and-recompile): an off-by-one in the "+N more" count, a
removed truncation step, and a removed `off`-mode guard each independently
made the relevant new test fail with a real assertion error, all four
restored afterward (`git diff` empty). Also independently re-derived the
top-N-by-revenue claim from `internal/data/pos_repo.go`'s
`ArticleSalesForDay`/`ArticleSalesForInstantWindow` (`ORDER BY gross
DESC`, confirmed) and the cap upper-bound enforcement (999, enforced in
three independent places: HTTP validation, client `max` attribute, and
`resolveEODArticlePrintSettings` itself).

| # | Finding | Severity | Disposition |
|---|---|---|---|
| 1 | The elevation-dialog retry's hidden-field replay for the two new fields (`eod_api.go`'s `[]elevationHiddenField` list) had **zero test coverage** — a mutation deleting both new entries passed the entire `internal/pages` package with no failures. Real failure mode: a manager's approval-dialog retry that doesn't carry the fields persists them as `""`, silently resetting a store's chosen `all`/`off` back to the shipped `capped`/30 default, under an elevation approval. | **Fixed (should-fix)** | Added `TestPostSettingsEOD_NeedsElevation_HiddenFieldsReplayArticlePrintSettings`: posts without a PIN (forcing `needsElevation`) carrying non-default mode/cap, asserts the rendered dialog's hidden `<input>`s actually carry them. Re-verified by mutation: deleting the two `elevationHiddenField` entries makes this new test fail (`git diff` confirmed the mutation and restore were byte-identical to the fix afterward). |
| 2 | `POST /api/reports/eod/print/{period}` (reprint) resolves article-print settings **live/current**, not whatever was in effect when the archived report was originally closed — undocumented at the call site, unlike nearly every other non-obvious choice in this file. A manager reprinting an old day after switching to `off` gets a Z-report with no BY ARTICLE section and no marker anything is missing. | **Documented (should-fix)** | Judged the behavior itself correct/consistent (this handler already re-resolves `storeNameOrDefault`/`cfg.Charset` live) — added a comment at the call site explaining the choice and its `off`-mode caveat explicitly, rather than changing behavior. |
| 3 | `web/help/en/reports.md` described the `all` mode as "today's behavior" — true only until this ships, after which "today's behavior" is `capped`. ar/fa/tr all correctly said "the *previous* behavior" — en was the outlier. | **Fixed** | Changed en to "the previous behavior", matching the other three locales. |
| 4 | ar/fa/tr's new prose was appended as a standalone trailing `##` heading at the very end of each file (~200 lines below the section it actually modifies, after unrelated tips/cash-reconciliation topics) — unlike en, which wove it directly into the existing per-article breakdown section. An ar/fa/tr shop owner reading about BY ARTICLE gets no hint the printed version is configurable. | **Fixed** | Moved the paragraph into the existing breakdown section in all three locales (matching en's placement) and removed the trailing duplicate heading. Re-ran `make docs-shots`; only the `reports` topic's 4 locale hashes + the surface hash changed, no `.png` content changed (confirmed: the docs-shots harness captures `/reports` with no tab expanded, so the EOD settings form was never in frame either before or after). |
| 5 | Two test doc-comments overclaimed their own coverage: `TestResolveEODArticlePrintSettings_DefaultsUnsetOrInvalid`'s comment said "non-positive/oversized/non-numeric cap all fall back" but only exercised the negative case; `TestBuildEODDoc_ArticlePrintMode_DefaultCappedLeavesLowSKUUnaffected`'s comment implied it called `resolveEODArticlePrintSettings`, but it passes the shipped-default constants directly. | **Fixed (nit)** | Extended the first test to a table covering negative/zero/oversized/non-numeric/blank cap values (all correctly fall back); reworded the second comment to describe what it actually tests and point at the sibling test that pins the resolve-function contract separately. |
| 6 | Cap-boundary ties (`ORDER BY gross DESC` has no secondary sort key) print in arbitrary order when two articles share identical gross straddling the cap line. | Accepted, cosmetic | Not fixed — matches the existing `ArticleGroupsForDay`/`DepartmentsForDay` precedent of not adding a tie-break key; genuinely cosmetic (which of two identical-revenue articles appears on line 30 vs. 31 has no financial consequence). |
| 7 | The "Top N" number spinner stays visible/enabled regardless of mode (so it's shown even under `off`), carries only an `aria-label`, no visible label. | Accepted, follow-up | Deliberate (the template comment explains: the value must round-trip on submit even if a manager changes the number before changing the mode) — a visual polish item, not filed as its own card given its minor severity; noted here for visibility. |
| 8 | The elevation-dialog summary (`elevation.summary.eod_settings_enabled/disabled`) still names only the schedule change, not the article-print change also being approved in the same submission. | Accepted, matches precedent | Identical to how `business_day_start` already piggybacks on the same summary without its own callout — consistent with existing behavior, not a regression this diff introduces. Worth a follow-up card covering all piggybacked fields on this form at once; not this branch's scope. |

## Explicitly re-verified, not just trusted

- **Top-N-by-revenue ordering**: `ArticleSalesForDay`/
  `ArticleSalesForInstantWindow` (`internal/data/pos_repo.go`) both
  `ORDER BY gross DESC`; archived reports round-trip through
  `content_json` (JSON array order preserved), so `articles[:articleCap]`
  is genuinely top-N by revenue in every code path, not "first N in
  whatever order the DB returns."
- **Cap upper bound**: `article_print_cap=100000` is rejected with 400
  ("must be a whole number between 1 and 999") at the HTTP layer; the
  client `<input>` also carries `max="999"`; and
  `resolveEODArticlePrintSettings` independently rejects `> 999` even if a
  row were hand-edited directly in the DB (falls back to the default 30,
  not to 999).
- **A cap value submitted alongside `mode=all`/`off`** is stored anyway
  (not rejected, not silently dropped) — confirmed this is deliberate: it
  preserves the manager's chosen number so flipping back to `capped`
  later restores it rather than snapping to the default.
- **No money-type mixing**: mode is a plain string, cap a plain `int` —
  neither touches `money.Money` at all; the pre-existing
  `a.Gross.Minor()` at the print-formatting boundary is unchanged.
- **No new file writes**: no `os.Create`/`WriteFile`/`MkdirAll`, no
  cwd-relative path where `paths.Data(...)` would belong — this diff is
  settings + print-format only.
- **No real client/shop name**: test fixtures use "Test Shop"/"Task
  Runner"/synthetic "Article 01"… only. No secret-shaped literal anywhere
  in the diff.
- **Kiosk isolation / offline-first / plugin signing**: N/A — no
  `/self-order` route touched, no filesystem writes, no plugin code
  touched.

## Verified beyond automated tests

- `gofmt -l .` — empty
- `go build ./...`, `go vet ./...` — clean
- `go test ./...` — full suite green, all 50 packages (not just
  `internal/pages`)
- `golangci-lint run ./...` — 0 issues
- `scripts/ci/guard-data-access.sh`, `guard-i18n.sh`,
  `guard-help-topics.sh`, `guard-docs-shots.sh`,
  `guard-compliance-claims.sh` — all pass
- **Real running app, driven with a real browser** (not just
  rendered-HTML-string assertions): booted a throwaway auth-off till,
  drove Playwright to `/reports` → Day-end (EOD) tab in both `en` and
  `fa` (RTL) at the 1024×600 kiosk viewport, and actually looked at the
  screenshots — the new "Printed article list" control group renders
  cleanly, correctly mirrored and fully legible in fa/RTL (the longest
  translation, "چاپ N کالای برتر بر اساس درآمد", does not overflow or
  clip), consistent with the sibling `business_day_start` field's own
  wrap behavior at this viewport width (pre-existing, not introduced by
  this diff — confirmed by not having touched that field's markup).
  Then exercised the real endpoint end-to-end against the live server
  (not `httptest`): `POST /api/settings/eod` with
  `article_print_mode=off&article_print_cap=15` → 204, immediate refetch
  of the tab shows `off` selected and `15` in the number input; a bogus
  mode value → 400 with the exact validation message. Server killed and
  scratch files removed afterward.
- `make docs-shots` actually run (pre-installed Chromium at
  `/opt/pw-browsers`) — 100/100 screenshot tests passed both times (once
  after the initial implementation, once after the review-fix pass);
  confirmed via the manifest diff that only the surface hash and the
  `reports` topic's 4 locale hashes changed, no `.png` content changed
  (the harness captures `/reports` with no tab expanded, so the EOD
  settings form was never in frame). One unrelated `ar/sell.png`
  byte-only re-render occurred on both runs — confirmed as the
  established capture-noise pattern (documented in the ut-docs#1010
  review) and reverted both times, keeping the diff to only the
  `reports` topic's genuinely-changed content.

## Safe-to-merge verdict

Yes. No blocker-class findings. One demonstrated test-coverage gap fixed
and re-verified by mutation (elevation hidden-field replay); one
undocumented-but-correct behavioral choice now documented (reprint uses
live settings); one locale-inconsistency and one prose-accuracy issue
fixed across all 4 locales; two test-comment-accuracy nits fixed. Two
minor items accepted as out-of-scope/matching-precedent rather than
fixed here (the elevation summary not naming every piggybacked field;
the always-visible cap spinner), both noted above for visibility rather
than silently dropped.
