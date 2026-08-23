# 2026-08-23 — docs-shots: #194 root-cause + Python/JS surface-hash cross-check

Closes universaltill/ut-docs#370.

## What shipped

1. **Root-cause investigation** of why PR #194's recorded `surface_sha256`
   (in `web/help/img/manifest.json`) disagreed with both canonical hash
   implementations on a clean checkout — the incident #195 patched around
   without explaining. Documented below; no code changes from this alone.
2. **A CI-enforced cross-check** (`scripts/ci/guard-docs-shots-cross-check_test.sh`)
   that runs the Python (`guard-docs-shots.sh`) and JS (`e2e/tests-docs/lib.js`)
   surface-hash implementations against the same live working tree and fails
   if they ever disagree. Wired into `.github/workflows/ci.yml`.
3. A small, additive change to `guard-docs-shots.sh`: a
   `GUARD_DOCS_SHOTS_PRINT_SURFACE_ONLY` env var that makes it print its
   computed hash and exit, instead of comparing against the manifest — this
   is how the cross-check test gets the Python side's value without a third
   copy of the hashing algorithm (which would itself be exactly the kind of
   drift-prone duplication this card exists to guard against).

## Root cause

**Confirmed mechanism, not fully byte-reproducible** — a real attempt, per
the card's own acceptance criteria for an acceptable "unreproducible"
outcome.

Checked out PR #194's exact merge commit (`3bfb2d5`) in a clean worktree and
ran the *contemporary* version of `guard-docs-shots.sh` (i.e. the algorithm
as it existed on 2026-08-06, before ut-docs#659's later dotfile-exclusion
fix — reconstructed from that commit's own copy of the script, not today's):

- It reproduces the exact reported failure: `guard-docs-shots: the app
  surface ... changed since the manual's screenshots were last taken`.
- The clean recompute is `8aa7059c...` — matching #195's corrected value
  exactly, confirming (as #195 already found) that the recorded `0bc542fd...`
  was the outlier, not a live algorithmic divergence.

Tested the leading theory named in #195/the review record — line-ending
handling — properly this time (not the "naive" attempt referenced there):
converted every surface file's line endings to CRLF and recomputed. Result:
`f724943...`. **Does not match** `0bc542fd...`. CRLF handling is ruled out.

**Root cause identified**: at the time PR #194 was authored, `surface_files()`
in both `guard-docs-shots.sh` and `lib.js` did **zero dotfile filtering** —
the dotfile/dot-directory exclusion (`.DS_Store` class) was only added later,
by ut-docs#659, which merged **2026-08-14, eight days after #194**. Verified
via `git log -S"DS_Store"` on both files: the fix first appears in the merge
for PR #355 (2026-08-14), strictly after #194's `3bfb2d5` (2026-08-06).

So if whoever ran `make docs-shots` for #194 was on a machine that had
dropped an untracked dotfile into `web/ui/`, `web/public/`, or
`internal/pages/` (canonically, macOS Finder's `.DS_Store`, which is
gitignored and therefore never part of the committed tree) — the
then-current algorithm would hash it into `surface_sha256` anyway. A later
clean checkout, with no such file on disk, computes a different hash for
the *identical git tree*, because the discrepancy isn't in tracked file
content at all — it's an extra untracked input feeding one side's computation
and not the other's.

**Demonstrated the mechanism directly**, on the same #194 worktree with the
contemporary (pre-#659) algorithm: planting two throwaway `.DS_Store`-named
files in `web/ui/` and `web/public/` and recomputing shifts the hash away
from the clean baseline (`8aa7059c...` → `e77fcc64...`) — proving that *any*
untracked dotfile in those directories changes the result under that
algorithm, exactly the class of drift #194 exhibited.

**Why the exact `0bc542fd...` value itself isn't reproducible**: it would
require the literal byte content of whatever untracked file(s) existed on
the original author's machine at that moment — ephemeral, machine-specific
Finder metadata that no longer exists and cannot be regenerated. The
mechanism is confirmed; the specific historical bytes are not recoverable,
which the card's acceptance criteria explicitly allows for.

**Timeline corroboration**: ut-docs#659 (filed and fixed independently,
2026-08-14) fixes precisely this defect class — a Mac-generated manifest
failing this same guard with no visible cause in the diff — just discovered
through a different path (someone hit it again, not connected back to #194
at the time). #194 was, in all likelihood, the same bug's first appearance.

## Why this is the right fix for the second half of the card

A cross-check against a *synthetic* fixture tree was considered and
rejected: both implementations already scan the same live working tree on
every real invocation (CI and `make docs-shots` alike), so the working tree
at HEAD already is a shared fixture — inventing a separate one is an extra
tree that could itself drift from what's actually checked, for no added
coverage.

Implemented as a new bash test script matching the repo's own
`scripts/ci/guard-*_test.sh` convention (see `guard-docs-shots_test.sh`,
`guard-data-access_test.sh`, etc.), rather than inline in `guard-docs-shots.sh`
itself, so a genuine divergence fails as its own clearly-named CI step, not
folded into (and easily missed inside) an unrelated guard's output.

## Verified beyond automated tests

- `bash scripts/ci/guard-docs-shots.sh` — still passes on the current, real
  `main` tree (surface `1b050631a135…`), unaffected by the refactor.
- `bash scripts/ci/guard-docs-shots_test.sh` — all 6 existing regression
  cases still pass (the reorder to compute `cur_surface` before loading
  `manifest.json` doesn't change observable behavior in normal mode).
- `bash scripts/ci/guard-docs-shots-cross-check_test.sh` — passes today
  (Python and JS agree: `1b050631a135…`).
- **Adversarial**: temporarily patched `e2e/tests-docs/lib.js`'s
  `surfaceHash()` to hash an extra byte, confirmed the cross-check fails
  with a clear message naming both diverging values, then restored the file
  (`git status` clean afterward) and re-ran to confirm it passes again.
- `go build ./...`, `gofmt -l .` — clean (this diff touches no other Go
  code).
- Confirmed no other caller of `guard-docs-shots.sh` depends on its exact
  invocation (`grep` across `Makefile`/`.github/workflows/*.yml` — only the
  existing CI step and the new cross-check test call it).

## Independent review

Fresh-context Sonnet subagent, `isolation: "worktree"` (`complexity:easy`
tier). **Verdict: safe to merge.** Independently re-ran everything above
(not just read it) in its own isolated worktree: `guard-docs-shots.sh` in
both normal and `GUARD_DOCS_SHOTS_PRINT_SURFACE_ONLY=1` modes,
`guard-docs-shots_test.sh` (all 6 cases), `guard-docs-shots-cross-check_test.sh`,
`go build ./...`, `gofmt -l .`; manually exercised the 4 guard failure paths
the existing suite doesn't cover (missing manifest, missing PNG, stale
topic, orphaned manifest entry) by editing and restoring real files,
confirming the `cur_surface`/`manifest` reorder is behavior-preserving for
all of them, not just the covered ones; re-ran the adversarial
lib.js-divergence test itself rather than trusting this doc's account of
it; and independently re-verified the root-cause write-up against the
actual historical worktree (`/tmp/rootcause_worktree`, PR #194's real merge
commit) — reproduced the same recorded/computed/CRLF/dotfile values and
confirmed the `ut-docs#659` timeline claim via its own `git log -S`.
Confirmed the cross-check delegates to the two real implementations rather
than re-implementing the algorithm a third time.

Two non-blocking findings, both fixed before merge:

1. **This section was a stub** ("See findings and resolution below" with
   nothing following) at review time — the review's own output hadn't been
   folded back into the record yet, breaking `CLAUDE.md`'s "every
   substantive change lands with a review record" rule in spirit even
   though the file existed. Fixed: this is that content.
2. **CI placement.** The cross-check step was originally wired into the
   `e2e` job (after its own `setup-node@v4`) rather than `build`, where
   every other `guard-*_test.sh` in this repo runs. It worked correctly
   there, but `e2e` carries `needs: build`, so the cheap check only ran
   after the full Go build/test suite finished — undercutting the "fail
   fast" goal stated in the test file's own header comment. Fixed: moved
   the step into `build`, adding a `setup-node@v4` there (immediately
   after `setup-go@v5`) since nothing in `build` needed Node before now.
   Re-verified after moving: `python3 -c "import yaml; yaml.safe_load(...)"`
   catches a real syntax bug the move surfaced (the step's `name:` value
   contained an unquoted `: `, which YAML parses as a nested mapping key —
   fixed by quoting the string) — confirms the workflow file is valid YAML
   post-move, and `guard-docs-shots-cross-check_test.sh` still passes from
   its new location.

## Deferred / follow-up

None — both parts of ut-docs#370's acceptance criteria are addressed.
