# Review — inactive variant barcode attach says why (ut-docs#4343)

**Date:** 2026-10-14 · **Branch:** `fix/4343-variant-barcode-inactive` · **Lane:** `lane:cloud-41`
**Built by:** Sonnet (complexity:easy) · **Reviewed by:** Opus 5.5, fresh context (different model from the author)

## What shipped
- `data.ErrVariantInactive` sentinel; `addBarcodeInTx` wraps it (`%w: <id>`). Refusing a barcode on an
  inactive variant is unchanged — the scan path never resolves one, so the code would be unreachable.
- `common.FriendlyBarcodeConflict` maps it to `catalog.barcode.variant_inactive` ("This variant is
  switched off. Tick Active for it and save, then add the barcode.") instead of the generic
  "barcode attach failed". Covers the item form, import and cloud directive callers alike.
- Variants tab: an inactive row no longer offers the ＋ attach form; a short hint
  (`catalog.barcode.variant_inactive_hint`, full sentence in its `title`) sits in its place,
  kept to the attach field's width (`.vg-row .vg-barcode-off`, logical CSS) so the sticky Save
  column (#1967) never covers it. Existing chips and their ✕ stay.
- Help `catalog.md` step 2 (en/de/ar/fa/tr): one sentence.
- Keys in en/ar/fa/tr; ut-plugin-language-{de,es,pt} follow-up PRs land after this merges.

## Tests
- `TestAddBarcode_InactiveVariantReturnsErrVariantInactive` (data), `TestFriendlyBarcodeConflict_VariantInactive`
  (common), `TestBarcodeAttach_InactiveVariantShowsSpecificMessage` + `TestVariantsPanel_InactiveVariantHasNoBarcodeAttachForm`
  (catalog handler/template, real migrated DB).
- e2e `variant-barcode-inactive-4343.spec.ts`: real Variants tab, switch a variant off, hint visible,
  no attach form on it, the active row keeps its form; geometric check that the hint overlaps neither
  the sticky Save nor the Active checkbox — 1024×600 en, 360×800 en, 1024×600 fa (RTL). 3/3 pass.

## Review findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major (process) | New keys need the de/es/pt pack PRs | Done in the same cycle, after core merge (brand-new keys) |
| 2 | minor (process) | Review record missing | This file |
| 3 | minor | Help says a switched-off variant can't take a barcode; my./cloud `SaveVariant` can still set one (`replaceVariantBarcodesTx` skips the active check deliberately) | Accepted: the help is the till manual and the till surface does refuse |
| 4 | minor | Cloud directive error text changes from "variant X inactive" to "variant inactive: X" | Accepted: nothing in till or ut-cloud matches the text |
| 5 | nit | ar used "متوقف"; "غير نشط" ties to the «نشط» checkbox | Fixed |
| 6 | nit | Go tests assert English only | Accepted; e2e covers fa |

**First driven run** (Tester) caught a real layout defect: the long sentence wrapped under the sticky Save
column at 1024×600 (en and fa). Fixed by the short hint + width cap; the e2e now asserts it.

## Verified
- TDD re-verified by the reviewer in an isolated worktree: production files reverted → all four tests fail
  (two compile on the missing sentinel; with only the sentinel stubbed they fail on `errors.Is=false` /
  "barcode attach failed"); handler: "got: barcode attach failed"; panel: "inactive variant must not render
  a barcode attach form". Restored → pass.
- gofmt clean, `go build ./...`, `go vet`, golangci-lint 0 issues, `go test ./...`, every `ci.yml` build-job
  guard (`guard-deadcode-baseline.sh` and `guard-shellcheck-version.sh` could not run locally: deadcode tool
  built with go1.26 vs module go1.27, no shellcheck binary — no shell or Go-reachability change here; CI runs both).
- Screenshots looked at: Variants tab 1024×600 en, 360×800 en, 1024×600 fa (RTL), light theme. Not looked at:
  dark theme, physical touch hardware.

## Deferred
- ut-docs#4348 — reactivating an item leaves its variants off (point 3 of the report). Filed this cycle; built and merged by another lane (#1935) while this PR waited on CI.

**Verdict:** safe to merge.
