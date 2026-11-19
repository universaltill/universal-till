# Review: catalogue import reorder-level follow-ups (ut-docs#5959)

Date: 2026-11-09 · Lane: `lane:cloud-41` · Follows: ut-docs#2084
(`2026-11-09-import-reorder-level-2084.md`, its accepted Low findings L2, L4,
L5 and the Tester's wording note).

## What shipped

- **L2 — "Reorder point" wins over other reorder columns.** `catimport`
  `headerIndex` gains an unexported `preferredSynonym` table
  (`reorder_level` → `reorder point`). In each of its two passes (exact /
  `[Location]`, then paren-stripped) the preferred synonym is tried first, so
  a file with both `Reorder Level` and `Reorder Point` reads the reorder point
  in either column order, CSV and XLSX alike. The ut-docs#587 B1 rule is kept:
  a paren-stripped `Reorder point (x)` still loses to a genuine exact
  `Reorder level` (deliberate refinement of the BA's "always wins"; the
  Architect's call). No other field changes. `catimport/VERSION` 0.6.1 → 0.6.2.
- **L5 — no reorder warning on a gift voucher.** `parseTimeImportWarnings`
  skips the reorder issue for `ItemTypeGiftVoucher`, mirroring the commit
  path, which never stores a voucher's reorder level.
- **T1 — dedicated message.** New key `import.issue.reorder_level_invalid`:
  "Imported without a reorder level: “%s” could not be read as a whole number
  from %s to %s (write it without thousands separators)." Bounds come from
  `catalogrules.MaxReorderLevel`, in the locale's digits but **ungrouped**
  (review finding 1). `en.meta.json` entry with three placeholders, not in the
  baseline.
- **L4 — pages-level failure test.** A `BEFORE UPDATE OF reorder_level`
  `RAISE(ABORT)` trigger fails one row's `SetItemReorderLevelTx`; the test
  asserts that row is failed, no item exists for its SKU, and the sibling row
  imports with level 7.
- Help: `web/help/en/catalog.md` step 3 (reorder-point precedence) and
  "Fixing problem rows" (new wording, voucher exception).

## Phases

BA → Architect → Dev (Sonnet) → Tester + UX (parallel) → review (Opus 5.5,
different model from the author, fresh context).

- **UX (R-UX1, fixed):** "“1,000” is not a whole number from 0 to 1,000,000"
  was self-contradictory because the parser rejects separators; reworded to
  "could not be read … (write it without thousands separators)".
- **Tester:** PASS. Six deliberate breaks of the implementation, each caught
  by a test, tree restored (diff checksum equal before/after). Driven run on a
  real till (throwaway data dir, Playwright): CSV with `Reorder Level` 20 and
  `Reorder Point` 5 imported Flour with level 5; a voucher row with a bad cell
  showed no ⚠; a bad row showed the new message; summary "Imported: 3 —
  Warnings: 1". fa at 360px and de at 1024×600: no overflow.
  **Visual attestation:** Tester screenshots `tester-preview.png`,
  `tester-commit.png`, `tester-preview-fa-360.png`,
  `tester-preview-de-1024.png` (session scratchpad) looked at by the Tester.
  `internal/plugins` timed out under full-suite load in the Tester's run
  (`TestHostTCPEgressPolicy`, untouched; passes alone).

## Review findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Low | The message says "without thousands separators" but printed the ceiling with them (`1,000,000`, de `1.000.000`); the hard-coded `0` stayed Latin in Arabic-Indic locales. | **Fixed:** both bounds are placeholders rendered with `httpx.LocalizeDigits`, ungrouped; `TestTranslateReorderLevelIssue_BoundsUngrouped` (red with `FormatDecimal`: got `abc\|0\|1,000,000`, green after). |
| 2 | — | Other `headerIndex` callers (mapped import, smart_products alias fill, smart_people, `NeutralHeader`) checked: unchanged behaviour; "reorder point" is only a `reorder_level` synonym. | No change. |
| 3 | — | B1 rule holds (paren-stripped point never beats exact level), pinned by a test case. | No change. |
| 4 | — | Voucher filter hides nothing else; preview and commit share `parseTimeImportWarnings`. | No change. |
| 5 | Nit | L4 test's `class="muted"` check is weak (matches any muted element); the DB assertions carry the test. | Accepted. |

TDD re-verified by the reviewer outside the Dev's checkout: origin/main's
`catimport.go` makes `TestHeaderIndex_ReorderPointOutranksOtherSynonyms`
fail on 4 cases and both CSV/XLSX "both orders" tests read 20 not 5; all pass
with the fix.

Pre-flight: help topic `web/help/en/catalog.md` updated; docs-shots n/a (no
layout change, text-only row warning); `reference/` n/a (no reference page
covers import column synonyms); README n/a (not stale); `en.meta.json`
updated for the new key; language packs n/a (Weblate, ADR-0160); ut-cloud
catimport pin → deferred to a follow-up after CI cuts `catimport/v0.6.2`
(another lane's ut-cloud#953 is pinning v0.6.1 now); German-thousands misread
(`1.000` read as 1, found by UX) → deferred → ut-docs#5962.

## Gate

gofmt clean; `go build ./...`; `go test ./...` (root) and `catimport`
`go test ./...`; `golangci-lint run ./...` 0 issues; every guard in
`ci.yml`'s build job passes except `guard-shellcheck-version.sh`, which needs
a `shellcheck` binary this container lacks (no shell changes).

## Verdict

Approve. Merge once CI is green.
