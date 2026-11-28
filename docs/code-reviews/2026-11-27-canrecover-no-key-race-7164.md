# Review: TestCanRecover/no_key no longer races the approval-key reporter (ut-docs#7164)

Date: 2026-11-27. Lane `lane:cloud-41`. Built by Opus 5.5 (one-file test fix: BA, Architect and Dev ran inline, `[no-subagent: one test-file edit, cause already traced on the card]`). Reviewed by a separate Sonnet 5.5 subagent, a different model from the author. Tested by a separate Sonnet 5.5 Tester agent.

Pre-flight: test-only change. Help topic, `reference/`, README, locales, `en.meta.json` and mirrors are all n/a because there is no behaviour change. Supersession check: `main` has no commit since the branch point that touches `internal/enroll/`. universal-till#2731 merged meanwhile; it touches neither `recover_test.go` nor the approval reporter, and the branch merges cleanly.

## What shipped

- `internal/enroll/recover_test.go`: the `TestCanRecover` `no key` case calls `resetApprovalReport()` before `paths.Init(t.TempDir())`. Since universal-till#2781 (ut-docs#7153), that helper cancels every approval-key report goroutine and waits for it to finish. A report started by `Init` can no longer run after the dir switch and create a fresh key in the empty dir. The assertion is unchanged.

## Cause

`recoverTill` → `initForTest` → `Init` kicks a background `reportApprovalKeyOnce` → `approvalkey.Shared().Report` → `LoadOrCreate`. The case's setup then switches `paths` to an empty dir. When the report ran after that switch, it created a key there. Then either `CanRecover` returned `true` (recover_test.go:340), or `Recover` healed (recover_test.go:345). The second form was the more common of the two.

## Verification

| Run | Result |
|---|---|
| `main`, `go test -race -count=150 -run '^TestCanRecover$' ./internal/enroll/` (author) | 17/150 failed, all `no_key` (14 × "Recover went ahead", 3 × "CanRecover = true") |
| same, Tester (independent) | 16/150 failed, all `no_key` |
| fix, `-race -count=300` (author and Tester) | 300/300 pass |
| fix, the card's `-race -count=50` (Tester) | pass |
| fix, whole package `-race -count=50` (Tester, 412 s) and `-race -count=3` | pass |
| fix, `-race -count=200` (Reviewer) | pass |
| no false pass (Tester): `CanRecover` key check disabled → `no_key` red 5/5; key created after the reset → red 3/3; both restored | test still guards |
| `gofmt -l`, `go vet ./internal/enroll/`, `scripts/ci/guard-fixed-dates.sh` | clean |

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| R1 | Info | `approvalkey.Shared()` holds only a path, and `Has`/`LoadOrCreate` read the disk each time. `CanRecover` only calls `Has`. `Recover` kicks a report only after a successful heal. Init's `run` goroutine is skipped because `keyDeviceRegistered` is set. So nothing can recreate the key after the reset. | Confirms the fix is complete. |
| R2 | Low | `restoreDir` (restored_test.go:30) switches the dir at a test's start. It could in theory meet an untracked nil-WaitGroup report from an earlier test that outlived its cleanup. No failing path was found. | **Accepted:** theoretical only. Not filed. |
| R3 | Low | `resetApprovalReport()` could move into `recoverTill` for all callers. | **Accepted:** out of scope for an XS card. Other subtests re-run `Init` on purpose. |

Verdict: **ship**.
