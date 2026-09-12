# Code review: guard-docs-shots comment-only escape hatch (ut-docs#2102)

**Change:** `scripts/ci/guard-docs-shots.sh` hashes whole files under
`web/ui/**`, `web/public/**` and `internal/pages/**.go` to detect when the
shipped user manual's screenshots have gone stale. Because it hashes whole
files, a comment-only edit trips it for zero pixel change and demands a
full 104-screenshot regeneration — which conflicts with every other open
PR by construction. New `scripts/ci/update-docs-shots-surface-hash.sh` is
a companion tool a developer runs locally, after manually confirming a
surface edit changes no rendered pixel, to refresh only the manifest's
`surface_sha256` field (via the guard's own
`GUARD_DOCS_SHOTS_PRINT_SURFACE_ONLY` mode, so the hash algorithm is never
duplicated) instead of paying for a real screenshot capture. The author
commits the resulting one-line manifest diff with a
`Docs-Shots-Unchanged: true` trailer — a documentation convention for
reviewers, not something anything reads or enforces.

Reviewer: independent fresh-context Opus subagent (isolated from the
implementation), per this card's `complexity:medium` routing. Two rounds
run — the second earned because the first found blocker-class (CI-breaking)
issues, per the standing "second round must be earned" rule.

## Round 1 — first draft (guard reads a git trailer live in CI)

The first draft had `guard-docs-shots.sh` itself read a
`Docs-Shots-Unchanged: true` git trailer at CI check time and bypass the
surface-hash check for that one head commit. Findings:

1. **CRITICAL — the bypass would silently never fire in real CI.**
   `actions/checkout`'s default `fetch-depth: 1` grafts the checked-out
   commit as parentless, so `HEAD^2` never resolves — verified by the
   reviewer against a real depth-1 clone (`git log -1 --format=%P` on HEAD
   returned an empty parent list). The escape hatch would have shipped
   completely inert.
2. **HIGH — the bypass was sticky.** It never updated `manifest.json`
   itself, so the same trailer had to be re-asserted forever, and the
   recorded `surface_sha256` stayed wrong for every unrelated PR that
   touched a surface file after the trailered one merged — turning a
   one-PR false positive into a repo-wide one.
3. **MEDIUM — trailer parsing was looser than documented.** `tr -d
   '[:space:]'` deleted all whitespace and concatenated multiple matching
   trailers, and git's own trailer-key matching is case-insensitive, so
   e.g. two trailers `Docs-Shots-Unchanged: tr` + `: ue` or a
   lowercase key both fired despite the "exact match only" claim.
4. **MEDIUM — the new test could not run on a stock GitHub runner.** It
   made real `git commit`s without a configured identity
   (`actions/checkout` sets no `user.name`/`user.email`).

Confirmed correct in round 1 and preserved into the final design: the "no
change to the hash algorithm, so `e2e/tests-docs/lib.js` needs no update"
claim, and that the escape hatch only ever bypasses the surface check, never
the per-topic markdown/missing-screenshot/orphan checks.

## Fix: redesigned around a local, self-healing tool

Rather than patch the CI-time trailer read, the escape hatch moved out of
CI entirely: `update-docs-shots-surface-hash.sh` runs locally, rewrites
`manifest.json`'s `surface_sha256` for real, and the guard's own pass/fail
logic is otherwise byte-for-byte unchanged from `main` (confirmed via
`git diff <base> HEAD -- scripts/ci/guard-docs-shots.sh`: only comments and
one printed message differ). This eliminates findings 1-4 structurally —
there is no git-trailer-reading code left anywhere, and the manifest is
genuinely corrected rather than bypassed.

## Round 2 — verifying the redesign

The reviewer confirmed all four round-1 findings resolved (finding 3 and 4
resolved by removal of the code they applied to) and found two new issues
in the redesign itself, both fixed before merge:

1. **MEDIUM — `sort_keys=True` on the manifest rewrite.** Reordered every
   topic's locale keys from the generator's own `en/fa/ar/tr` insertion
   order into alphabetical order, turning the "one-line diff" tool into a
   60+-line diff — defeating its own purpose (still conflicts with other
   PRs; the next real `make docs-shots` run would flip the order back
   anyway). **Fixed:** dropped `sort_keys=True`; a new test assertion
   (`diff -u` line count, not just a key-order-blind dict comparison) now
   catches a regression of this class directly.
2. **MEDIUM — the test's cleanup trap didn't cover every mutation.** A
   failure between planting the topic-markdown edit and its restore left
   the real `web/help/en/alerts.md` modified and a stray `.zz_bak` file
   behind, contradicting the test's own "never leaves the tree dirty"
   claim and risking cascading into whichever guard ran next in the same
   CI job. **Fixed:** every mutated path (manifest, topic markdown, planted
   fixture, scratch files) is now tracked from the top of the script and
   restored/removed from a single `cleanup()` trap, unconditionally.

Also fixed as cleanups from round 2: predictable `/tmp/...$$` scratch paths
replaced with `mktemp`, and a leaked `.pre_update` backup path added to the
same trap.

## Verification beyond automated tests

- `gofmt -l .`, `go build ./...`, `go vet ./...` — clean (no Go source
  changed by this fix; confirms nothing else in the tree regressed).
- `shellcheck` 0.9.0 (the version this repo's CI pins) — clean on both new
  `.sh` files and the modified guard.
- `bash scripts/ci/guard-docs-shots.sh`,
  `guard-docs-shots_test.sh`, `guard-docs-shots-cross-check_test.sh`,
  `update-docs-shots-surface-hash_test.sh` — all green, run repeatedly
  across both review rounds and after every fix.
- `git status --short` verified clean (and `web/help/img/manifest.json`
  byte-identical to its pre-test state) after every test run — the new
  test's hermeticity was independently re-verified by the round-2 reviewer,
  not just asserted.
- New `.github/workflows/ci.yml` step wires
  `update-docs-shots-surface-hash_test.sh` into the same job as the
  existing docs-shots guard tests.

Closes universaltill/ut-docs#2102.
