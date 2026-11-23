# Review — UK regulator named "Information Commission" in the check-in notice (ut-docs#6278)

Uncommitted on `fix/6278-uk-regulator-name`. Author model Sonnet 5.5; reviewer Opus 5.5 (independent subagent, fresh context); tester Sonnet 5.5. Card notes: `C:\unitill\notes-6278\01-05`.
Pre-flight: review record — this file · help/docs-shots n/a (no help topic names the regulator; string shorter, no layout change) · reference n/a · en.json: existing key edited, en.meta.json n/a (description at :923 has no regulator text), ar/fa/tr n/a (key absent; Weblate) · ADR n/a · legal record → ut-docs `legal-reviews/2026-11-19-uk-regulator-name-6278.md`.

## What shipped
`registration.notice.stop_hint` now ends "in the UK that is the Information Commission (ico.org.uk)" (DUAA 2025 ss.117-119, in force 30.9.2026). `notice_text_test.go` pins the new text; new `regulator_name_guard_test.go` fails when any en.json value names "Information Commissioner"/"Commissioner's Office" outside a "formerly" bridge. NoticeVersion stays `checkin-notice-v2`.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| F1 | Low | `regulator_name_guard_test.go:26`: any "formerly" anywhere in the string exempts it. Proven false pass: "This screen was formerly called Data. Complain to the Information Commissioner's Office." passes. Strip the bridge phrase first, then match. | Fixed 2026-11-19: only the exact bridge phrase is stripped (both apostrophes, any case), then any remaining old name fails; table test incl. the "formerly called Data" probe (red first, then green) |
| F3 | Medium | Owner-approved notice text changed with no committed legal record; 6559 record :83 still quotes the old text. | Fixed 2026-11-19: `legal-reviews/2026-11-19-uk-regulator-name-6278.md` written; pointer added to the 6559 record |
| F5 | Low | `notice_text_test.go:9-13` says NoticeVersion changes with any string change; contradicted by the no-bump decision. Reword to material vs recorded non-material correction. | Fixed 2026-11-19: comment now says material change bumps, non-material correction in a legal record keeps it (#6278) |
| F7 | Info | Guard reads en.json only; grep of web/help, web/ui, internal (non-test) finds no old name. | Accepted |

NoticeVersion: **no bump** recommended. `auto.go:60-63` bumps on a material change of what is sent; `auto.go:122` stops check-ins on every till until re-shown. The change renames the same complaint body only (s.119(2) already reads old references as the Commission). Record the correction in the legal record; owner confirms on #6278.

## Verification (reviewer, WSL Ubuntu-24.04, go1.27.1, throwaway copy)
`go vet ./internal/enroll/` clean; `go test ./internal/enroll/ -count=1` ok. Mutation: HEAD en.json put back → `TestShippedNoticeEqualsApprovedText` and `TestEnglishStringsDoNotNameOldUKRegulator` both fail with the right messages; restored → green. Not run: guard-i18n*.sh (Tester ran), full `go test ./...`, golangci-lint, a rendered look at the hint.

Open gates: owner OK on #6278 for the live notice text and no-bump; human browser check of ico.org.uk (agents get 403).
Verdict: APPROVE after F1/F3/F5 (fixed 2026-11-19, see Outcome; re-verified by the Dev, not re-reviewed).
