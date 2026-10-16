# Review: catsync row issues get codes the till translates (ut-docs#4485)

**Date:** 2026-10-16 · **Lane:** lane:cloud-41 · **Author model:** Sonnet (Dev subagent) · **Reviewer:** Opus 5.5 (independent subagent, fresh context)

## What shipped

- `internal/catsync/issue.go` (new): 21 exported issue codes (`row_not_object`,
  `name_invalid`, `barcode_taken`, `apply_failed`, …), one code → English
  format table, `IssueCodes()`, and an internal `rowIssue{Code, Args}`.
  `parseRow`, `normalizeBarcodes` and `plan.issue` carry a `rowIssue` instead
  of a finished sentence.
- `catsync.Sample` gains `issue_code` and `issue_args` (omitempty). `issue`
  keeps the English text, byte-identical to before, so JSON API callers
  keep working.
- `internal/pages/import_from_system.go`: the "From another till system"
  preview renders each sample's issue as `T("import.issue.<code>")` with its
  args. An empty or unknown code shows `import.status.unknown_issue` and
  logs. That mirrors `translateImportIssue` for the file import, which
  already used reason codes, so it needed no change.
- 21 `import.issue.*` keys in `en/ar/fa/tr.json`, with metadata in
  `web/i18n-meta/en.meta.json`. The de/es/pt language packs get follow-up
  PRs after this merges.

## Verification

- TDD: the Dev saw `undefined: IssueRowNotObject`, then
  `fa preview missing "import.issue.name_invalid"`. The reviewer re-ran the
  check independently: reverting the page to `view.Issue = s.Issue` fails
  `TestImportSystem_PreviewRowIssueIsTranslated` with
  `fa preview missing "نام وجود ندارد یا نامعتبر است."`, and restoring it
  makes the test pass.
- New tests:
  - `TestParseRow_IssueCodesAndEnglishTextUnchanged`
  - `TestRun_IssueSamplesCarryCodeArgsAndEnglish`
  - `TestRun_AlreadyLinkedAndApplyFailedCodes`
  - `TestIssueCodes_AllHaveEnglishFormat`
  - `TestImportSystem_PreviewRowIssueIsTranslated`
  - `TestImportSystemIssueText_UnknownCodeIsGeneric`
  - `TestCatsyncIssueCodes_HaveLocaleKeys`: every code has a key in each
    locale, with a matching `%s` count.
  - `TestAPIImport_SamplesCarryIssueCodeAndEnglish`
- Gate:
  - `gofmt` is clean, `go build ./...` passes, and `golangci-lint run ./...`
    reports 0 issues.
  - `go test ./...` passes.
  - Every guard in `ci.yml`'s build job passes except two, which fail the
    same way on `main` and are environment gaps in this container:
    - `guard-deadcode-baseline.sh`: the local deadcode is built with go1.26,
      but the module needs go1.27.
    - `guard-shellcheck-version.sh`: no shellcheck binary is installed.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low | The tr `too_many_barcodes` text glued a suffix to the number (`%s'den`). The real value, 10, needs `10'dan`. | Fixed: `Bir üründe en fazla %s barkod olabilir.` (no suffix on the number). |
| 2 | Low | The ar `category_too_deep` and `too_many_barcodes` texts used a noun form after `%s` that only fits 3–10. | Fixed: reworded so the number stands alone after "الحد الأقصى…:". |
| 3 | Nit | The fa text shows Western digits from the `%s` args. | Accepted: every `%d`/`%s` count in the till is formatted the same way. |
| 5 | CI | `guard-deadcode-baseline.sh` flagged `IssueCodes()` as unreachable, because only tests called it. This container's deadcode was built with go1.26 and could not run the guard. | Fixed: `IssueCodes` is now a package variable (`slices.Sorted(maps.Keys(issueText))`). Re-checked locally with a go1.27 deadcode: no catsync entry. |
| 4 | Nit | The fallback log says `unrecognised row issue code ""` when a sample has text but no code. The current engine never produces such a sample. | Accepted. |

The reviewer checked and cleared these points:
- Every old issue site maps to a code whose English text is exactly the old
  string. That includes the two `%d` sites and both `barcode_taken` sites.
- The args (barcode limited by `barcodeRe`, an external id that went
  through `cleanText`, constants) are Sprintf values, never the format.
  html/template escapes them, and plugins can't set codes or args.
- Placeholder counts match in all four locales.

**Verdict:** safe to merge. The manual (`web/help/en/catalog.md`, "From
another till system") is still accurate, so it is unchanged.
