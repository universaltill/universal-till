# Review: the till applies `rename_till` (ut-docs#3272)

**Card:** universaltill/ut-docs#3272. A rename of a till from the cloud (my. or the portal) now reaches that till. This is slice B of #2351.
**Design:** ADR-0095 Notes, section "Till name: a cloud rename reaches that till". Built with Opus 5.5 and reviewed independently by Fable in one round. The review found no blockers.

## What shipped
- A `Hooks.RenameTill` hook plus the `rename_till` dispatch. A missing or non-string name fails with `missing name`.
- Tick skips a `rename_till` whose `device_id` is blank, belongs to another till, or arrives before the till knows its own id. A skipped directive gets no apply and no result post, so it stays pending for its real target. It works on satellites too: `rename_till` is not main-till only.
- `internal/pages/till_name.go` holds the 60-rune limit in one place. `validateTillName` refuses a name that is empty, over 60 runes, or contains a control character (it does not truncate). The Settings form keeps its existing trim-and-truncate behaviour through `truncateTillName`.
- `cloudRenameTill` writes `till.name` on the main till and `sync.till_name` on a joined till, so `enroll.DeviceName` and the next heartbeat report the new name. It writes an audit row `till_name_changed` with `{name, via: cloud, key}`. An unchanged name writes nothing.
- `satelliteSkipLog` was factored into a reusable `skipOnceLog`; behaviour is unchanged.
- Help: `web/help/{en,de,tr,ar,fa}/multitill.md` step 9 now says where a rename shows up. `manifest.json` hashes were regenerated with `make docs-shots`; no PNG changed.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The main till's own list of joined tills (`tills.name`) still shows the name from pairing after a cloud rename. | This is a follow-up, not part of this card's contract: it needs a new name report from the joined till to the main till. Filed as ut-docs#3294 and noted in ADR-0095. |
| 2 | nit | When the till's own id is unknown, the log said "addressed to another till" and used up the once-per-id log. | Fixed. The log gives a distinct reason and doesn't use up the dedupe. |
| 3 | nit | The invalid-UTF-8 branch can't be reached from JSON, and the name is trimmed twice. | Harmless; kept. |
| 4 | nit (pre-existing) | A joined till's own Settings rename writes the shop-wide `till.name`. | Filed as ut-docs#3292. |

## Verified
- Run by the dev: `gofmt -l .` empty; `go build ./...`; `go test ./...` passes (`-race` on cloudsync and enroll); `golangci-lint` 0 issues; every guard in `ci.yml`'s build job passes except `guard-shellcheck-version`. That one can't pass locally because the local shellcheck is 0.11, not the 0.9.0 baseline, and no scripts were touched.
- Run by the reviewer: build, vet, the cloudsync/enroll/pages tests, lint, and the data-access, i18n, help-topics, help-drift and docs-shots guards all pass.
- TDD re-verified by the reviewer. Disabling the device-id skip made `TestTickRenameTillOnlyForOwnDevice` and `TestTickRenameTillSkippedWhenOwnIDUnknown` fail. Replacing the refusal with truncation made the `too_long` case fail. Both were restored.
- Name parity with the cloud: both sides trim, cap at 60 runes and refuse control characters, and neither normalises. So the till reports exactly the name the cloud sent, and the owner label clears.
- Not driven in a running app: there is no visible UI change beyond the help text, which was re-rendered by docs-shots. The end-to-end round trip is covered by each side's handler and Tick tests against the shared payload shape, not by a live cloud-and-till run.

**Verdict:** safe to merge.
