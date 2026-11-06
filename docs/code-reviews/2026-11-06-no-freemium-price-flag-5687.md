# Review: no "freemium" price flag in marketplace docs (ut-docs#5687)

Date: 2026-11-06 · Author model: Sonnet (BA+Architect+Dev) · Reviewer: Opus (independent, different model)
Parent: ut-cloud#870 (enum -> `[free, paid]`; listing data model is a `paid_listing` bool).

## What shipped
- `docs/market_place_openapi.yaml` L252 (query param) and L639 (Plugin schema): `enum: [free, paid, freemium]` -> `[free, paid]`. Nothing else in the file touched.
- `docs/plugin_guidelines.md`: removed the "**Freemium:**" monetisation block (3 bullets). The other pricing claims (20% commission etc.) belong to ut-docs#5704; links region belongs to PR #2392. Neither touched.
- Regression guard: `internal/plugins/docs_no_freemium_test.go` fails on any `freemium` (case-insensitive) in those two docs files.
- Grep of `docs/`, `web/`, `internal/`, README, scripts found no other "freemium" mention (code-reviews excluded).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Low | `plugin_guidelines.md` "Example Pricing Models" -> "Accounting Integration: Free (basic export) / $9/month (advanced features)" still reads like a free-basic/paid-premium split, i.e. freemium in spirit without the word. The marketplace can express it only as two separate listings. | Accepted, out of scope: belongs with the other invented pricing claims on ut-docs#5704 (same section, alongside the 20% commission). |
| 2 | Info | Test is a Go test in `internal/plugins` reading `../../docs/...`; no precedent for a docs test there, but `go test` runs in the package dir so the path is sound, and the plugins package is the closest owner of marketplace/listing concepts. A `scripts/ci` guard would be the alternative. | Accepted as is. |
| 3 | Info | Regex `(?i)freemium` over only the two named files: no legitimate use of the word exists in them, so no over-match; scope is narrow by design (repo-wide grep found no other mention outside code-reviews and the test itself). | No change needed. |

## Verification
- TDD: new test run before the fix failed with 3 hits (yaml:252, yaml:639, plugin_guidelines.md:940); after the fix it passes.
- gofmt -l internal/plugins: clean. go vet ./internal/plugins: clean.
- guard-mail-addresses, guard-compliance-claims, guard-competitor-naming, guard-fixed-dates: exit 0.

Pre-flight: gofmt -l, go vet + go test ./internal/plugins (targeted test fail->pass), guard-mail-addresses/compliance-claims/competitor-naming/fixed-dates. User manual: n/a (no till behaviour change; docs/ only). en.json keys: n/a. ADR: n/a (aligns to existing ut-cloud spec). README: n/a. Review record: this file.

## Review
Reviewer: Opus. Read the diff, both edited docs and the new test.
- Repo-wide `git grep -i freemium` (excluding docs/code-reviews): only the new test file. Nothing in `web/help/`, `web/locales/`, `web/ui/` templates, README or scripts.
- Guidelines read coherently after the removal: "Paid Plugins" now ends at the Subscription block and runs straight into "Example Pricing Models"; no orphaned reference to a freemium model.
- Test: plain line scan with clear `file:line` messages; cwd-relative path is correct for a Go test (package dir is the cwd under `go test`). No fixed dates, no file writes.
- Re-verified the TDD claim myself in the review worktree: restored both docs from HEAD~1 -> `go test ./internal/plugins -run TestDocsDoNotDescribeFreemium -count=1` FAILs with exactly yaml:252, yaml:639, plugin_guidelines.md:940; restored HEAD -> PASS.
- `gofmt -l` on the new file: clean. `go vet ./internal/plugins`: clean. Full package suite not re-run (Tester: ok, 868s).

## Verdict
Approve. No blocking findings; finding 1 deferred to ut-docs#5704.
