# Code review — refuse a till rename to a name another till uses (ut-docs#3308)

Date: 2026-10-03 · Lane: `lane:cloud-54` · Author: Opus 5.5 · Reviewer: Fable (independent subagent)
Companion: ut-cloud `docs/code-reviews/2026-10-03-refuse-duplicate-till-rename-3308.md`.

## What shipped

Pairing already refused a duplicate till name (`TillsRepo.NameTaken`, #1264); renaming did not.

- `TillsRepo.NameTakenExcept(name, exceptID)`: the same trimmed, Go-side `EqualFold` check, skipping one row. `NameTaken` delegates to it.
- `pages.tillNameTaken`: other tills are the enrolled tills on the main till. On a joined till they are the synced `tills` roster minus its own row (`sync.till_id`), plus the main till's name (`tillNameOrDefault` in the default locale, exactly as enrolment compares it). Local data only, so it works offline. A case change of the till's own current name is always allowed.
- `POST /api/settings/till-name` refuses with 422 + `sync.error.name_taken` (an existing key, in every locale; no new keys) **before** the elevation gate (#557 convention). The form shows the reason inline in `#till-name-msg` (`status:422 text-response:`), not the page-wide banner.
- `cloudRenameTill` (the `rename_till` hook) refuses too, so the directive fails with the reason. This is defence in depth: ut-cloud now refuses before queueing.
- Help `multitill.md` step 9 states the rule in en/de/tr/fa/ar.
- `web/help/img/manifest.json`: a hash-only refresh via `e2e/tests-docs/write-manifest.js`, no PNG changed. The template edit is an attribute and the help edit is prose, so no rendered pixel changes. The local Chromium (141) doesn't match the pin (149), so a full `make docs-shots` would have churned every screenshot for nothing. `Docs-Shots-Unchanged: true`.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | (ut-cloud) `deviceNameTaken` compared only a device's owner label, not the name it still reports while a rename is pending | **Fixed** in ut-cloud, test updated |
| 2 | minor | (ut-cloud) the portal shows 400/404/409/429 for the rename form as a bare English text page | Pre-existing pattern; **deferred** to ut-docs#3580 |
| 3 | nit | The name becomes taken between the elevation prompt and the PIN retry → the retry's 422 closes the dialog silently | Accepted (race; the next save shows the reason) |
| 4 | nit | `#till-name-msg` is unstyled, so the refusal reads like a status line | Accepted; mirrors the retention form's 409 |
| 5 | nit | A till that left a shop keeps its stale `tills` roster, which can refuse a former sibling's name | Pre-existing hygiene gap (#3030 area); noted |
| 6 | nit | `errTillNameTaken` is English; it travels to the cloud as the directive failure reason | Accepted; same as `validateTillName` |

The reviewer checked and confirmed: a joined till's roster row id == its `sync.till_id`; roster names stay current via `applyReportedTillName`; there are no other rename writers in scope.

## Verification

- TDD: the Go tests (`till_name_taken_test.go`, `TestTillsRepo_NameTakenExcept`) failed first with the real symptom (204 instead of 422; the rename was applied), then passed.
- e2e `settings-till-name-taken-3308.spec.ts`: passes. With the template change reverted, it fails (`#till-name-msg` stays empty).
- Full gate: `gofmt`, `go build`, `go test ./...`, `golangci-lint` (0 issues), and every `ci.yml` build-job guard pass. The exception is `guard-shellcheck-version.sh`: shellcheck isn't installed in this container, and no shell script changed.
- Not looked at: the visual styling of the inline message in dark theme / RTL. It is plain text in an existing span, and no CSS changed.

## Verdict

Safe to merge.
