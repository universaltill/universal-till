# Review: help-breadcrumb guard (ut-docs#4696)

**Date:** 2026-10-23 · **Lane:** lane:cloud-41 · **Built by:** Sonnet subagent · **Reviewed by:** Opus (independent subagent, separate worktree)

## What shipped
- `scripts/ci/checkhelpbreadcrumbs/` (Go, reads from disk under `-root`) + `guard-help-breadcrumbs.sh`,
  wired into `ci.yml`'s `build` job after the help-drift guard, with its sandboxed `_test.sh`.
- For each `web/help/<loc>/*.md`, every `Settings → <Section>` in an English numbered item is mapped to its
  `en.json` `settings.*.title|.label` key (longest match, word boundary); the same-ordinal translated item must
  contain that key's value in the locale (English value when the locale lacks the key). Locale strings come from
  `web/locales/<loc>.json`, else a pack checkout (`UT_<LOC>_PACK_DIR` / sibling `ut-plugin-language-<loc>`),
  else the locale is skipped with a notice.
- Shrink-only baseline `scripts/ci/i18n-baseline/help-breadcrumb-baseline.txt` (stale, duplicate, malformed or
  reason-less entries fail).
- Help prose now names the till's own labels: ar `claim.md` (تسجيل الصندوق), ar `display.md` (السمة),
  fa `backups.md` (پشتیبان‌گیری), tr `updates.md` (Yazılım güncellemesi ×5), tr `display.md` item 12 (missing
  final sentence translated — reviewer checked it against English).
- CLAUDE.md: one entry under "The user manual ships with the feature".

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium-High | Baseline entries for a locale the run can't load (de pack absent in CI) were reported stale, so de mismatches could be neither baselined nor tolerated: green in CI, red for a developer with the pack. | **Fixed**: entries for a skipped locale are never stale; unit test added. The 4 de mismatches are baselined against ut-docs#4891. Verified green both with and without `UT_DE_PACK_DIR`. |
| 2 | Low-Medium | Topics whose numbered items don't line up were skipped silently yet counted as checked; unmapped English section names ignored silently. | **Fixed** (first half): per-topic notice, excluded from the count, summary prints the skipped count; unit test added. Unmapped names ("Data", "Printer", "Tills", …) → ut-docs#4891. |
| 3 | Low | Match is "label anywhere in the item", so a short label (e.g. tr "Dil") can pass from elsewhere in the item. | Accepted: card's spec is per item; tightening needs locale-specific breadcrumb parsing. |
| 4 | Low | Breadcrumb forms with NBSP, a line wrap or quotes aren't recognised. | Accepted; none in today's English help. |
| 5 | Nit | `_test.sh` comment wording; GNU-only `sed -i '0,/re/'` (as in other guard tests); `UT_<LOC>_PACK_DIR` invalid for hyphenated locales; duplicate failure line when one item names a key twice. | Accepted. |

## Verified beyond automated tests
- Reviewer reverted `web/help/tr/updates.md` to `origin/main`: guard failed with 4 lines (`:15`, `:23`, `:25`, `:47`, expected "Yazılım güncellemesi"); restored → green.
- `_test.sh` asserts non-zero exits, so an always-0 guard fails it.
- Gate after the last edit: `gofmt -l .` empty, `go build ./...`, `go test ./scripts/ci/... ./internal/manual/... ./web/...`,
  `golangci-lint run ./scripts/ci/...` 0 issues, shellcheck clean on the new scripts, help-topics/help-drift/
  ci-tests-sandboxed/compliance/competitor/mail/i18n/rtl guards pass.
- No UI surface touched (CI guard + help prose only); no screenshots needed.

## Verdict
Safe to merge after fix 1/2. Deferred: ut-docs#4891.
