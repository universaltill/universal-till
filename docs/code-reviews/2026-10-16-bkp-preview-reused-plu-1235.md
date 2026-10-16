# Review: Preview flags a reused .bkp item number and names the new one (ut-docs#1235)

Date: 2026-10-16 · Branch: `feat/1235-preview-reused-plu-warning` · Card: ut-docs#1235

## What shipped

- **Preview** (`internal/pages/import_page.go`): a row that will import but whose
  reused source PLU was renumbered by the `.bkp` parser (`SKUIssue =
  duplicate_in_file`) now renders as a warned row (`row-warn`, ⚠) instead of a
  plain "ok". Warned rows move to the front, so they also survive the 200-row
  display cap. Commit summary counts are unchanged.
- **Message names the new number** (AC2): new key
  `import.status.sku_reused_renumbered` (`item number "%s" reused in this file —
  new number "%s"`). It replaces `import.status.sku_reused_in_file`, because a
  placeholder change needs a new key (ADR-0160 §5). The commit path uses it too.
  `translateSKUIssue` now takes the assigned SKU.
- i18n: en/ar/fa/tr translated. fa/ar wrap each placeholder in LTR isolates
  (U+2066…U+2069, as `httpx` does for money), because without them the browser
  rendered `30006-2` as `2-30006`. Metadata is in `web/i18n-meta/en.meta.json`.
  The old key's line is removed from the shrink-only `key-meta-baseline.txt`.
- Manual: `web/help/{en,de,ar,fa,tr}/catalog.md` now says the Preview marks
  these rows ⚠ with the new number before anything is imported.
- Language packs: `ut-plugin-language-{de,es,pt}` follow-up PRs rename the key
  after this merges. Each pack's `check-key-drift.sh` was run against this
  branch's `en.json` and passes.

## Tests

- New `TestImport_BkpPreviewWarnsReusedProductNumberBeforeCommit` (AC3). It
  checks three things: the preview writes no items, Affogato's row is
  `<tr class="row-warn">` and names `30006-2`, and only the renumbered row
  carries the warning.
- `TestImport_BkpCommitDedupesReusedProductNumberInsteadOfDropping` is
  tightened to the new wording, which names `30006-3`.
- TDD: both tests were written first and failed against the old code. The
  reviewer re-checked this independently by reverting `import_page.go` in a
  worktree (both failed with the claimed messages) and then restoring it (both
  pass).

## Independent review

Fable was unavailable (out of usage credits), so Sonnet reviewed this, per
MODEL-ROUTING. The author was Opus 5.5. The reviewer ran build, vet, the
`internal/pages` Import tests, `internal/catimport`, `guard-i18n`,
`guard-i18n-meta`, `guard-help-drift` and `guard-help-topics`; all passed.
Findings:

1. **Nit, fixed:** this review record was missing.
2. **Nit, fixed:** a failure message in `import_problem_grid_test.go` still
   named the removed key.

The reviewer confirmed these behaviours are correct:
- A deduped row that is also skipped (SKU, barcode or name already in the
  catalog) keeps its skip status.
- Forced-correction dedup runs only on commit, so it causes no double or false
  warning on re-preview.
- CSV imports are unaffected.
- There is no interaction with the GeneratedSKU input.
- No other code or spec references the old key.

There are no SQL, money, `os.MkdirAll` or `paths.Data` concerns.

## Verified beyond automated tests

A real till binary ran on a throwaway data dir. Playwright uploaded a three-row
`.bkp` that reuses PLU 30006 and pressed Preview. Screenshots were taken and
looked at:
- en, 1024×600, light theme: two ⚠ rows naming `30006-2` and `30006-3`.
- fa and ar, 1024×600, RTL, light and dark: the numbers read correctly only
  after the isolates were added.
- tr and de, 360px: in the card layout the warned tint shows, and tapping
  "More" (touch emulated) shows the ⚠ status.

Not checked: real touch hardware, and the Pi kiosk.

## Deferred

The Preview still shows a plain "ok" for the other non-blocking parse-time
warnings (dropped barcode, unparsed tax rate, invalid net quantity), which are
surfaced only after commit. This is a pre-existing gap and is filed as its own
Backlog card.

## Verdict

Safe to merge.
