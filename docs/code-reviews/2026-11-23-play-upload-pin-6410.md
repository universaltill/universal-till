# Review: Play upload-cert pin script + shared pin pre-check (ut-docs#6410)

Pre-flight: scoped diff (scripts/ci, play-publish.yml, ci.yml); shellcheck clean; focused tests only.

Shipped: `set-play-upload-pin.sh` (owner tool), `check-play-pins.sh` (build job warn-only on dry run, sign job hard), exact owner-command fail messages, workflow wiring greps, mutation test extended, both new tests in ci.yml.

Reviewer: independent Opus 5.5 subagent (author Sonnet 5.5). Verdict: no blocking or security findings; ran every test under bash 3.2 and current bash, all mutants killed.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | wiring test matched version step's DRY_RUN line, branch swap mutant survived | fixed: assertions scoped to the pre-check step and its then/else branches |
| 2 | nit | `read -rsp` strips spaces from password | fixed: `IFS= read` |
| 3 | nit | no tmp cleanup trap | fixed |
| 4 | nit | keytool failure reported only as wrong password | fixed: wording covers corrupt/unreadable |
| 5 | nit | `UT_PIN_DIR` honoured in sign job | accepted: nothing sets it earlier in either job |
| 6 | pre-existing | a comment-only release list passes `-s` | superseded by round 2 below: fixed, no card |

## Round 2 (second Reviewer, Fable)

APPROVE. Fixed: (1) comment-only release list fails closed in check-play-pins.sh (needs a 64-hex line, message names the file; TDD case written first, saw it fail, then pass); (3) pin compare is now `grep -qxF "$pin" "$rel_file"` (no pipe, annotation removed). Accepted: (4) `bash -x` on set-play-upload-pin.sh would print the password variable. Re-ran check-play-pins, set-play-upload-pin, play-publish-workflow and mutation tests (all pass), shellcheck clean.
