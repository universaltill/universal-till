# Import problem grid — ut-docs#601

**Date:** 2026-08-25
**Card:** [ut-docs#601](https://github.com/universaltill/ut-docs/issues/601) (rescoped by Architect this cycle — split off [ut-docs#990](https://github.com/universaltill/ut-docs/issues/990))
**Complexity:** hard — Dev at Fable, Review at Opus (independent, different model)

## What shipped

The catalog importer's preview (`GET /import`, `POST /api/import`) previously
showed a problem row (missing name, bad price, duplicate barcode/SKU, various
write failures) as a passive status pill with no way to act on it — the only
fix path was re-uploading a corrected file. This change makes the two most
common, genuinely fixable issue types interactive:

- Preview now stages the uploaded file server-side (`internal/pages/import_stage.go`)
  instead of discarding it, so a follow-up commit can re-read the
  byte-identical copy and apply the operator's row-level decisions.
- `missing_name` and `bad_price` rows get an inline correction field plus an
  "Import anyway" checkbox. Every other issue type keeps today's passive
  status text with no controls — there is genuinely nothing an operator can
  do about a duplicate-in-catalog or a write failure from this UI.
- An explicit **allow-list** (`forceableImportIssue`) is the only path to
  forcing a row through — never a deny-list — so any future issue type
  `catimport` grows defaults to skip-only.
- Commit with no `staged_id` (a client that never previewed) behaves exactly
  as before this change — a pinned regression test guards this.
- Rescoped out of this card, filed separately: a generic plugin-driven import
  surface for `POST /api/data/import` (ut-docs#990) — that backend already
  exists (ut-docs#599) but has zero consuming plugins today, unlike export's
  equivalent (already live in Settings, ut-docs#600), so it's lower-urgency
  speculative UI. **No ADR was needed**: this stays a plain server-rendered
  form (a bigger form with per-row inputs, one submit), confirmed against
  ADR-0008 at design time, no SPA-shaped client state.

## What the independent review found

Review ran at Opus (different model from Fable, which implemented this) in
an isolated git worktree, against a WIP snapshot commit, per this pipeline's
standing process — including reverting each claimed fix locally to confirm
its regression test actually fails without it.

**No blockers.** No data corruption, no injection, no race, no base-path
regression. Two real bugs (medium severity — bounded, not corruption) and one
UX/test-quality gap, all fixed same-cycle (no second independent-review round
needed — none were money/tax/data-loss/security class):

1. **F1 — an in-file duplicate PLU could be forced through on the `.bkp`
   path.** `internal/catimport/bkp.go`'s issue-detection switch checked
   `missing_name` before the in-file duplicate-SKU check, so a row that was
   both missing a name AND duplicated another row's PLU in the same file
   reported as `missing_name` — an allow-listed forceable issue — letting a
   corrected-name override smuggle the duplicate past the guard that exists
   specifically to stop that. Impact was bounded (the DB's `items.sku
   UNIQUE` constraint rejected the second insert, surfacing as an unrelated
   `item_failed`), but it contradicted the feature's own stated invariant.
   **Fixed** two ways: reordered the parser's switch so the duplicate check
   runs first (the issue is now correctly reported, and the existing
   allow-list already refuses to force it), *and* added a defense-in-depth
   in-file SKU check in the page handler's override-application loop itself,
   covering the case of two *forced* rows racing to claim the same SKU
   (which the parser-level fix alone doesn't catch, since neither row's
   `Issue` was ever `duplicate_sku_in_file` before the operator's own
   corrections were applied).
2. **F2 — the original "defense-in-depth" test didn't test the allow-list.**
   `TestImport_CommitStagedNeverForcesNonForceableRows` still passed with
   `forceableImportIssue` inverted into a deny-list, because its fixture
   rows were skipped by DB-level checks independent of the allow-list.
   **Fixed** by adding `TestImport_BkpStagedCommitNeverForcesInFileDuplicatePLU`,
   a genuine `.bkp`-path integration test exercising six rows across every
   ordering of the missing-name/duplicate-PLU overlap (clean-then-dup,
   dup-then-clean, two forced rows sharing a PLU) — written first against
   the pre-fix code, confirmed failing, then confirmed passing after F1.
3. **F3 — the "Import anyway" checkbox was inert on non-forceable rows.**
   Every skipped row rendered the checkbox, but ticking it did nothing for
   an issue type outside the allow-list — no override applied, no feedback,
   and it contradicted the manual's own wording ("all other skipped rows
   always stay skipped"). **Fixed**: only forceable rows (`FixField != ""`)
   render any interactive control now; every other skipped row keeps its
   passive status text only.

Also verified and found sound: no double-consumption race on the staged-file
registry (`takeStagedCatalogUpload` is read+delete under one mutex); row
overrides are keyed by a stable index that can never land on the wrong row
across a currency-triggered re-parse; `htmlEscape` covers both name and
value in the currency-confirm prompt's field re-emission, and the reflected
field names are regex allow-listed; `os.CreateTemp("")` resolving to the OS
temp dir (not `paths.Data(...)`) is correct here — the staged copy is
transient, TTL-pruned, and consumed on commit, not something that needs to
survive a self-update.

**F5 — found in an earlier review pass this same cycle, before the formal
independent review, and generalized during it:** the currency-confirm gate's
first early return (no `confirm_currency` submitted yet) was made to
preserve the staged copy instead of destroying it (so the operator's
corrections survive the ut-docs#970 confirm round-trip) — but the
independent review found the *other* early returns inside that same gated
block (an invalid currency code; a settings-write failure) still destroyed
it. Generalized: `preserveStaged` is now set for the whole gated section and
only cleared once the currency is actually confirmed and the commit proceeds
to write. New test: `TestImport_StagedCommitSurvivesInvalidCurrencyCode`
(TDD: written and confirmed failing before the fix).

Accepted, not fixed (bounded, low risk, noted rather than silently skipped):
a staged file from a preview that's never committed and never hits the
currency-confirm detour is only cleaned up by the registry's TTL prune
(opportunistic, on the next new preview) — one abandoned temp file per
un-committed preview, in the OS temp dir, until the next preview runs or the
till reboots.

## Verified beyond automated tests

- Real driven browser run (Playwright, headless Chromium) against a fresh
  till: uploaded a 3-row CSV (missing name / bad price / clean), previewed,
  screenshotted and visually checked the problem grid (controls correctly
  associated with their rows, nothing overlapping), ticked corrections,
  drove the currency-confirm detour end to end (this till's currency was
  unconfirmed by default), confirmed, and cross-checked the result against
  `GET /catalog` — all three items landed correctly, not just the transient
  result HTML.
- RTL (`fa`) screenshot: `dir="rtl"` confirmed, table/controls mirror
  correctly, correction placeholders render in Persian, nothing overlaps —
  and incidentally exercised a live non-forceable row (checkbox-correctly-
  absent) in RTL.
- Theme: this app has no literal dark theme (curated color-swap plugins
  only, per ADR precedent noted in `reference/ux-guidelines.md`); checked
  the `slate` theme, layout unaffected. **Not checked**: `amber`/`monarch`
  themes, `ar`/`tr` locales visually (only `fa` was screenshotted) — noted
  rather than implied.
- i18n: 7 new keys (`import.problem_grid.*` ×6, `import.error.stage_expired`)
  present and matching across all four locales (`guard-i18n.sh` — 1205 keys
  total, all resolve). ar/fa/tr translations were done by the Fable dev
  subagent (no NAS Ollama pipeline reachable from this sandbox this cycle) —
  **not yet human/NAS-re-verified**; tracked separately at
  [ut-docs#991](https://github.com/universaltill/ut-docs/issues/991), same
  pattern as ut-docs#982.
- Manual: `web/help/{en,ar,fa,tr}/catalog.md` updated in the same branch,
  read for accuracy (not just structurally guard-clean) — correctly
  describes ticking, typing the correction, and that all other rows stay
  skipped.

## Gate (final, after all fixes)

`go build ./...` · `go vet ./...` · `gofmt -l .` (clean) ·
`go test ./... -count=1` (every package `ok`) ·
`go test ./internal/pages/ -run TestImport -race -count=1` (clean, no race) ·
`guard-data-access.sh` · `guard-i18n.sh` · `guard-help-topics.sh` ·
`guard-docs-shots.sh` (screenshots regenerated via `make docs-shots`) — all
green.

## Verdict

**Safe to merge.** Independent review found no blockers; the two real bugs
it found (F1, F5-generalization) and the one UX gap (F3) were fixed same-cycle
with TDD regression coverage, re-verified by re-running the full gate
afterward. No second independent-review round needed — none of the findings
were money/tax/data-loss/security class.

## Deferred (new Backlog cards)

- [ut-docs#990](https://github.com/universaltill/ut-docs/issues/990) — generic
  plugin-driven import surface for `POST /api/data/import` (no consuming
  plugin exists yet).
- [ut-docs#991](https://github.com/universaltill/ut-docs/issues/991) — NAS
  Ollama re-verification of this change's ar/fa/tr translations.
