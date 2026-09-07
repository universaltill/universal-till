# Code review: cash-adjustment HTML fragment i18n (ut-docs#1504)

**Date:** 2026-09-03
**Author:** autonomous pipeline, Dev (WIP commit `e02bd3b` on
`fix/1504-adjustment-success-i18n`)
**Reviewer:** independent fresh-context Sonnet subagent, worktree-isolated
**Repo / PR:** `universal-till`, branch `fix/1504-adjustment-success-i18n`

## What shipped

`internal/pages/shifts_api.go`'s `respondAdjustmentSuccess` — the
HTML-fragment path of `POST /api/shifts/adjustment` — hardcoded
untranslated, unescaped English prose:

```go
writeHTML(w, http.StatusOK, fmt.Sprintf("<div class='success'>Adjustment recorded: %s</div>", data.AdjustmentID))
```

Same defect class already fixed twice on sibling functions in this file:
`respondShiftSuccess` (ut-docs#1406) and `respondCloseSuccess`
(ut-docs#1289). This card (#1504) was itself filed as a deferred finding
during the #1406 review.

Fix follows the `respondShiftSuccess` precedent exactly, line for line:
`httpx.ResolveLocale` → `httpx.T(locale, "shifts.adjustment_success")` →
`fmt.Sprintf` → `html.EscapeString` → `writeHTML`. New key
`shifts.adjustment_success` (`"Adjustment recorded: %s"` in en) added to
all four `web/locales/*.json` files (en, ar, fa, tr), alphabetically
placed between `shifts.adjustment` and `shifts.amount`. New test
`TestRecordCashAdjustment_HTMLSummaryIsTranslated` in
`internal/pages/shifts_api_test.go` asserts an English-locale render
(sanity check only, not discriminating pre/post fix) and a `fa`-locale
render containing `اصلاح ثبت شد` (the actual regression guard).

`data.AdjustmentID` is `pos.RecordCashAdjustment`'s return value —
`uuid.NewString()`, never user input — not currently a security issue,
but the escape keeps this function consistent with its siblings.

## What the independent review found

### TDD re-verification: PASS

Cherry-picked the WIP commit into an isolated worktree, then reverted
*only* the fix in `internal/pages/shifts_api.go` (test file and locale
JSON files left untouched) and reran the new test:

```
=== RUN   TestRecordCashAdjustment_HTMLSummaryIsTranslated
    shifts_api_test.go:826: expected the fa translation of shifts.adjustment_success, got:
        <div class='success'>Adjustment recorded: 8ece2e60-ac78-4d58-857b-8643d97d7f49</div>
--- FAIL: TestRecordCashAdjustment_HTMLSummaryIsTranslated (0.03s)
FAIL
```

Fails exactly where expected — the `fa`-locale assertion, with the
untranslated English literal leaking through — not a compile error or an
unrelated panic. Restored the fix and reran:

```
=== RUN   TestRecordCashAdjustment_HTMLSummaryIsTranslated
--- PASS: TestRecordCashAdjustment_HTMLSummaryIsTranslated (0.02s)
PASS
```

### Pattern match against sibling

`respondAdjustmentSuccess` now matches `respondShiftSuccess` structurally
(`ResolveLocale` → `T` → `Sprintf` → `EscapeString` → `writeHTML`), same
imports already present in the file (`html`, `httpx`) — no new import
needed, no deviation from the established shape.

### Locale files

- All four (`en`, `ar`, `fa`, `tr`) parse as valid JSON.
- Key sets are identical across all four (`en.json` minus each locale's
  keys, and vice versa, both empty).
- `shifts.adjustment_success` sits alphabetically between
  `shifts.adjustment` and `shifts.amount` in every file.
- Each locale's string has exactly one `%s` verb, matching the single
  `AdjustmentID` argument — no format-string mismatch.
- Translations use vocabulary consistent with the sibling
  `shifts.adjust`/`shifts.adjustment` keys in the same files (ar: تعديل,
  fa: اصلاح, tr: Düzeltme) — not English pasted in.

### Security

`data.AdjustmentID` traced to `pos.RecordCashAdjustment` in
`internal/pos/shifts.go:305` (`adjustmentID := uuid.NewString()`) —
server-generated, never user input. Same reasoning as the #1289/#1406
siblings: `html.EscapeString` here is defense-in-depth consistency, not a
fix for an actual injection.

### Gate

- `gofmt -l .` — clean (no output).
- `go vet ./...` — clean.
- `go build ./...` — clean.
- `go test ./...` — full suite green, all packages `ok`, no `FAIL`/panic.
- `bash scripts/ci/guard-i18n.sh` — green (1346 template keys resolve,
  all locales match `en.json`, no hardcoded Go-side response strings).
- `bash scripts/ci/guard-data-access.sh` — green.
- `bash scripts/ci/guard-page-http-error.sh` — green.
- `bash scripts/ci/guard-compliance-claims.sh` — green.

### Recurring bug classes checked (not applicable here)

- No file-write handler in this diff — checked; it's pure in-memory
  string formatting, no `os.*` calls, so a missing `os.MkdirAll` doesn't
  apply.
- No cwd-relative path anywhere in the diff — checked; nothing here reads
  or writes a path at all, so `paths.Data(...)` doesn't apply.

### Scope / applicability checks

- No real client/shop name anywhere in the diff (test fixtures are the
  file's existing generic `reg1`/`Front Till`/`user1`); no secret-shaped
  literal.
- Diff touches only `internal/pages/shifts_api.go`,
  `internal/pages/shifts_api_test.go`, and the four `web/locales/*.json`
  files — no `web/ui/` or `web/help/` files touched. Confirmed the
  UX-guidelines checklist and the manual-update requirement genuinely do
  not apply: there's no markup/template change (same
  `<div class='success'>...</div>` shape) and no user-visible screen
  changed shape, only the text now resolves through `T()`.

### Findings

None. No deviations from the sibling pattern, no blocking or non-blocking
issues found.

## Verified beyond automated tests

- Independent revert/restore TDD re-verification (above), run in an
  isolated git worktree so it never touched the orchestrator's shared
  checkout.
- Manually confirmed `data.AdjustmentID`'s provenance
  (`pos.RecordCashAdjustment` → `uuid.NewString()`) to rule out an
  escaping-relevant injection path.
- Manually confirmed alphabetical key placement and exact key-set parity
  across all four locale files (not just trusting `guard-i18n.sh`).
- Manually confirmed the diff's file list to rule out any `web/ui/` or
  `web/help/` touch before concluding those requirements don't apply.

## Verdict

**Safe to merge.** Build/vet/gofmt/tests/guards all green, TDD claim
independently re-verified with a real pre-fix failure and a real
post-fix pass, fix follows the established sibling pattern exactly, no
findings.
