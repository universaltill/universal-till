# Review: joined till's own name reaches the main till's Tills page (ut-docs#3294)

- **Date:** 2026-10-01
- **Lane:** lane:cloud-54 (author: Opus 5.5; reviewer: Fable, independent subagent)
- **Branch:** `fix/3294-joined-till-name-on-main`

## What shipped
- `fleetlink.Report` gains an optional `name`. The joined till fills it with `enroll.DeviceName` (its `sync.till_name`). A rename goes out at the client's next on-change check, every `RecheckEvery` (30 s).
- `decodeReport` drops a name longer than 256 bytes instead of cutting it. The 64-byte `maxReportField` clip would have cut valid 60-rune fa/ar names.
- `HubOptions.OnReport` is called on the reader goroutine for reports from the till's live link only, before the report is stored.
- The main till (`applyReportedTillName`, `internal/pages/sync_link.go`) runs the name through `validateTillName`, then calls the new `TillsRepo.UpdateName`. That is `UPDATE … WHERE id = ? AND name IS NOT ?`, so a repeated report never bumps `sync_admin_version`. An unusable name is logged once per till and name.
- Duplicate names are applied on purpose: the row shows the till's real name. Refusing duplicates at rename time is ut-docs#3308.
- Help `web/help/{en,de,tr,fa,ar}/multitill.md` step 9 gets one sentence in each locale.

## Findings
| Severity | Finding | Outcome |
|---|---|---|
| should-fix | Help said "within a few minutes"; the code delivers in about 30 s | Fixed: "about half a minute", in all 5 locales |
| nit | German "Abgleich" vs the page's "Synchronisierung" | Fixed |
| nit | A control-char `sync.till_name` would log a warning on every 5-min report | Fixed: warn once per till and name (`sync.Map`) |
| accepted | `isLive` → `OnReport` → store is not atomic. A rename during a same-till link replacement could briefly write the older name | Accepted: same authenticated till only, `tillID` never comes from the payload, and the next forced report (5 min) self-heals it |
| accepted | A bearer holder can flip its name once per 30 s, and each flip bumps the admin generation | Accepted: bounded, and that bearer can already do more |

## Verification
- TDD: `TestReplicaLink_JoinedTillsNameReachesTheMainTillsRow` fails without the `OnReport` wiring (`row = "Till 2"`) and passes with it. Checked by the author and again by the reviewer. Replacing `validateTillName` with identity makes its invalid-name branch fail.
- Real end to end: that test runs a real main-till `GET /api/sync/link` server and the real replica link client against two migrated SQLite DBs. It covers the first name, a later rename, and a control-character name, which is proven to arrive and proven not to be applied.
- `go test ./...`, `golangci-lint` (0 issues) and gofmt pass. Reviewer: `-race` on fleetlink/data/pages, no races. Guards pass: data-access, i18n, help-topics, help-drift, core-neutral, kiosk-engine, no-showmodal, compliance, competitor-naming. `guard-deadcode-baseline` fails identically on `main` in this container: it needs the desktop job's GTK headers. CI runs it there.
- No UI template change, so no screenshots. The Tills and quarantine pages already render `tills.name`.

## Verdict
Safe to merge.
