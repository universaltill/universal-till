# Review: no CI script pipes into grep -q under pipefail (ut-docs#2946)

## What shipped

- New `scripts/ci/guard-pipefail-grep-q.sh` (+ `_test.sh`, wired into `ci.yml`). It fails any `scripts/ci/*.sh` that sets pipefail and pipes (a single `|`) into `grep`/`egrep`/`fgrep` with a q flag in any flag position, or `--quiet`, also behind `command`. Whole-line comments, `||` and non-pipefail scripts are ignored.
- 24 sites in 12 scripts rewritten to grep a here-string (`grep -q … <<<"$x"`): 22 mechanical, plus `check-brand-assets.sh` (capture the awk block first) and `windows-signing_test.sh` (capture, a `[ -n ]` guard for the empty case, then `grep -vq`).
- Why: `producer | grep -q` lets grep exit at the first match, the producer dies of SIGPIPE, pipefail makes the status 141, and a present match reads as absent (#2941 turned `main` red), or with `!`, a false pass. The review of #2941 found 5 such scripts; the guard found 13.

## Review (independent, Fable)

- **All 24 rewrites semantically equal:** every pattern checked against the empty-here-string case (one empty line); no `-v`/`-c`/`-x`/`^$` hazard except the `-v` site, where the `[ -n ]` guard keeps it equal. Compared on 5 inputs under bash 3.2 and 5.
- **13 changed scripts run pre- and post-patch** under bash 3.2 and 5.3: identical exit codes and output (commit-attribution exercised with a real `git log` range). Mutations reintroducing a pipe in two guards → the new guard fails.
- **should-fix, fixed:** the regex missed `-E -q`, `-i -q`, `egrep -q` and `command grep -q`, and false-positived on `||`. The regex was widened and anchored on a single `|`; the test now covers 7 spellings × with/without a space, plus a `||` negative.
- The test is real: 4 deliberate breakages of the guard each fail it.
- Accepted / follow-up: the guard only scans `scripts/ci` (workflow `run:` steps with `shell: bash` have the same shape: `android-ci.yml:85`, `release.yml:455`), and `grep -l`, `-m1` and `head -1` after a pipe carry the same hazard. Filed as ut-docs#2983. An unreadable `app.css` now exits 2 instead of 1 in `check-brand-assets.sh` (both fail).

**Verdict:** safe to merge.
