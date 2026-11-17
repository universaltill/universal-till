# Review — cash adjustment confirmation without the internal id (ut-docs#5814)

**Date:** 2026-11-07 · **Branch:** `fix/5814-adjustment-confirmation`
**Author model:** Sonnet (dev subagent) · **Reviewer model:** Opus 5.5 (independent subagent)
**Precedent:** #5810 (`fix/5810-shift-open-confirmation`, PR #2430, the same fix for "Shift opened: <uuid>")

## What shipped

- After a cash adjustment or payout on `/shifts`, the HTML confirmation
  showed "Adjustment recorded: <uuid>". The Pfandrückgabe dialog showed the
  same text. Both now show "Adjustment recorded." with no id.
  - `respondAdjustmentSuccess` (`internal/pages/shifts_api.go`) renders the
    new placeholder-free key `shifts.adjustment_confirmed`, still through
    `html.EscapeString`. `fmt.Sprintf` and the id are gone from the HTML
    branch.
  - The JSON branch is unchanged. It still returns `adjustment_id` and
    `success` for API clients.
- i18n:
  - `shifts.adjustment_confirmed` = "Adjustment recorded." added to
    `en.json`, with its `en.meta.json` entry (description, `review:
    standard`, no placeholders).
  - The old key `shifts.adjustment_success` (`%s`) was removed from en, ar,
    fa and tr, and from the shrink-only `key-meta-baseline.txt`. Changing
    the placeholders needs a new key, so the old one is not reused.
- Tests (`internal/pages/shifts_api_test.go`):
  - `TestRecordCashAdjustment_HTMLSummaryIsTranslated` now asserts the plain
    text and the absence of the real adjustment id (read from `audit_log`)
    and of `%s` or `%!`. Its fa half now asserts the English fallback,
    because fa has no translation yet (ADR-0160 §2).
  - New: `TestPfandRueckgabe_HTMLConfirmationHasNoInternalID`.
  - New: `TestRecordCashAdjustment_JSONStillReturnsAdjustmentID`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | The `en.meta.json` entry was appended as the file's last entry. That edits the previous last line (adds a comma), and the end of the file is where most open PRs append, so it is a likely merge-conflict spot. | **Fixed (reviewer, uncommitted):** the entry now sits mid-file after `settings.display.awake_never`. That position is clear of #5810's insertion point near `shifts.close_no_sale_opens`. The file is unordered, so there is no order rule to follow. JSON is valid and `guard-i18n-meta.sh` passes. |
| 2 | minor | The fa half of the HTML test now only asserts the English fallback. The #1504 regression proof, that the HTML branch honours the request locale, is therefore weaker until Weblate delivers fa. | **Accepted:** ADR-0160 makes code PRs English-only, and the test comment says to restore the fa assertion once it is translated. `httpx.T(locale, …)` still receives the resolved locale. The #5810 sibling made the same trade. |
| 3 | nit | `docs/code-reviews/2026-09-03-shifts-adjustment-success-i18n-1504.md` still names `shifts.adjustment_success`. | **Accepted:** it is a historical record and must not be rewritten. |

Checked and found clean:
- **Leftover references.** `git grep` finds no `shifts.adjustment_success` in
  `web/` (templates, public JS, help), `e2e/`, `plugins/`, `internal/` or
  any locale. Only the 1504 record has it. No e2e spec asserts the
  "Adjustment recorded" text.
- **JSON validity and duplicates.** `en.json`, `ar.json`, `fa.json`,
  `tr.json` and `en.meta.json` parse, and none has a duplicate key (checked
  with `object_pairs_hook`).
- **Escaping.** The message is still passed through `html.EscapeString`.
  There are no placeholders, so there is no format-verb mismatch.
- **Test strength.** The id assertion uses the real id: `InsertAudit` is
  given `adjustmentID` as the row id (`internal/pos/shifts.go`). The
  `audit_log` lookup with `LIMIT 1` runs while only one adjustment row
  exists.
- **Not applicable.** No SQL changed (the repository pattern holds), no
  money is touched, there are no file writes or cwd-relative paths, and no
  real shop or person names appear.

## Extra verification

- **TDD re-run (reviewer, separate detached worktree, removed with `git
  worktree remove`).**
  - With `origin/main`'s `shifts_api.go`, both HTML tests fail:
    `TestRecordCashAdjustment_HTMLSummaryIsTranslated` (`:826`) and
    `TestPfandRueckgabe_HTMLConfirmationHasNoInternalID` (`:1174`).
    Restored, the tests pass.
  - Pre-fix, the old key no longer resolves, so that run alone does not
    prove the id check. A second mutation appends `data.AdjustmentID` to
    the new text. Both tests then fail with
    `Adjustment recorded. <uuid>` in the body, so the id assertion is
    load-bearing.
- **Gates in the main checkout:**
  - `gofmt -l internal`: clean.
  - `go vet ./internal/pages/`: ok.
  - `go test ./internal/pages/ -count=1`: ok (300s).
  - `guard-i18n.sh`, `guard-i18n-meta.sh` and `guard-fixed-dates.sh`: pass
    (the latter two re-run after finding 1).
- **Docs-shots.** `GUARD_DOCS_SHOTS_PRINT_SURFACE_ONLY` gives `3f287f24…`
  on both the branch and `origin/main`, the same value as in
  `web/help/img/manifest.json`. `shifts_api.go` registers API routes only,
  which are outside the hashed surface (ut-docs#620), so no refresh is
  needed.
- **Tester driven run** (UT_AUTH=off build):
  - Opened a shift and recorded an adjustment of 2.00. `#shift-result` read
    "Adjustment recorded." in en and in fa (English fallback) at 1024 and
    360, with no UUID and no clipping.
  - Mutation-checked both HTML tests.
  - Full `go test ./...`: ok except an unrelated `internal/plugins` timeout.
    `TestHostTCPCloseAllOnPluginUnload` passes alone on main and on the
    branch.
- **UX §7 gate: PASS.** This is a copy-only change, consistent with #5810's
  "Shift opened.".
  - "Adjustment recorded." also fits a payout, because the form is titled
    "Cash adjustment / payout".
  - In fa, the English fallback shows its full stop at the left edge (a
    bidi artifact). This resolves when Weblate delivers fa.
- **Not checked:**
  - Playwright specs were not run locally (no `e2e/node_modules`). They are
    left to CI e2e.
  - The Pfand modal was covered only by the Go test and not driven in a
    browser. It closes on 2xx, so the text is barely seen there.
  - Not checked: German, dark theme, `aria-live` on `#shift-result`.

## Verdict

**Approve.** The change is small and correct and matches the #5810
precedent. The tests prove both the HTML fix and that the JSON contract is
unchanged.

## Deferred

- Restore the fa-translation assertion in the HTML test once Weblate
  delivers `shifts.adjustment_confirmed` for fa (noted in the test comment).
- `lang-pack-drift` is advisory on this PR, because `en.json` changed by
  one key added and one removed. Translations arrive through Weblate
  (ADR-0160). No pack PR is needed.
- UX optional polish (not carded): echo the amount and type in the
  confirmation ("Payout of 5.00 recorded"). Also, the Pfand modal gives no
  visible confirmation, because it closes on success. That behaviour is
  pre-existing and out of scope.
- Merge order with #5810: both edit `shifts_api.go`, `shifts_api_test.go`,
  the four locales and the baseline in separate hunks. Re-check for
  conflicts when the second one is rebased.

Pre-flight: help topic n/a (`web/help/en/reports.md` "Cash adjustments & payouts" does not quote the confirmation text, and the flow is unchanged); `en.json` key → `web/i18n-meta/en.meta.json` updated (old key's baseline line removed); reference n/a (the JSON API shape is unchanged); README n/a; docs-shots n/a (surface hash unchanged, API-only file outside the hashed set; no screenshot shows the result fragment); review record: this file.
