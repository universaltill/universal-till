# Review: release macOS wiring test races on SIGPIPE (ut-docs#2941)

**Change:** `scripts/ci/release-macos-nonblocking_test.sh` only.

## What shipped

- `main` CI failed on 5babd67ee with `FAIL - macos-dmg-attach does not need publish-release`, although release.yml lists `publish-release`. Cause: `needs_of` piped `job_block` (awk, 4,318 bytes for that job) into an awk that `exit`ed after the `needs:` line. When the writer got SIGPIPE on its second buffered write, `set -o pipefail` turned the pipeline into status 141 and the `if` read false. This is timing-dependent.
- Fix: the `needs_of` reader consumes all of its input (a `done` flag, no `exit`). A new `needs_has` captures the output, then greps a here-string. The other `job_block … | grep -q` check now greps a captured block too.
- A deterministic self-test uses a fake workflow whose job block is over 64 KiB. It checks `needs_of`'s own exit status, not a captured copy.

## Review (independent, Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The self-test went through `needs_has`, whose capture discards the pipeline status, so it passed even with the old `{exit}` body. The TDD claim didn't hold. | **Fixed:** `if selftest_out="$(needs_of big-job)" && grep -qx … <<<"$selftest_out"`. Re-verified: with the old body restored, the self-test FAILS (`needs_of lost publish-release …`); with the fix it passes. |
| 2 | nit | Temp file had no trap. | Fixed: `trap 'rm -f …' EXIT`. |
| 3 | nit | The comment said "no `grep -q`" while here-string `grep -q` remains. | Fixed: "no `grep -q` on a pipe". |
| — | follow-up | The same shape (`printf big \| grep -q` under pipefail) is in 5 other guards. | Filed ut-docs#2946. |

Mawk (Ubuntu's default awk) swallows EPIPE, so on stock Ubuntu the self-test can't reproduce the race. On BSD awk and gawk it fails deterministically. The comment says so.

## Verified beyond the script's own run

- Mutations: dropping `publish-release` from macos-dmg-attach's needs → FAIL. Adding `- macos-app` to publish-release's block-form needs → FAIL.
- 100 local runs pass, 300 in ubuntu:24.04 pass, and bash 3.2 and 5.x both pass (reviewer).

**Verdict:** safe to merge.
