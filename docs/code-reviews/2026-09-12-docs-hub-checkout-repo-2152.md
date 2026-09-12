# Review: docs-hub checkout targets the wrong repo (ut-docs#2152)

**PR:** universaltill/universal-till#1115
**Card:** universaltill/ut-docs#2152
**Complexity:** easy — Dev at Sonnet (inline), Review at fresh-context Sonnet subagent.

## What shipped

`.github/workflows/ci.yml`'s `e2e` job's "Checkout docs repo for docs-hub
tests" step used `repository: ${{ github.repository_owner }}/docs`, which
resolves to `universaltill/docs` — not the real docs repo,
`universaltill/ut-docs`. Fixed to hardcode `universaltill/ut-docs`,
matching the sibling `adr-taxonomy-guard` job's own existing convention in
the same file. Added `scripts/ci/guard-docs-hub-checkout-repo.sh` (+
`_test.sh`) so this can't silently regress.

## Investigation (the card's other two acceptance criteria)

- **`DOCS_READ_TOKEN` set?** Yes — live CI run `34677542275`'s `e2e` job
  (on the *unfixed* `ci.yml`) shows the checkout step succeeding and the
  "will skip" warning step skipped.
- **Do the docs-hub specs run against real content once pointed right?**
  Yes, verified twice: (1) that same live run's job log shows
  `docs_hub.spec.ts`'s 2 tests passing even against the buggy
  `universaltill/docs` target — most likely explained by a GitHub
  rename-redirect from before `ut-docs` was renamed (inferred from the
  evidence, not confirmed with certainty); (2) real driven verification
  in this session: ran the actual binary + Playwright/Chromium against a
  genuine `universaltill/ut-docs` checkout, both `docs_hub.spec.ts` tests
  passed for real.

## Independent review

Fresh-context Sonnet subagent, isolated worktree. Verdict: not a blocker
for the underlying fix, one real should-fix finding in the new guard
script.

**Finding (should-fix, fixed before merge):** the guard's original
step-isolation logic bounded the step's body by matching its `path:
./.docs-hub` value, not by a structural step boundary. The reviewer
constructed a concrete adversarial case — the docs-hub step's
`repository:` line still broken, *and* its `path:` line renamed to
something else — and reproduced a false pass: the scan ran past the end
of the step, matched the *sibling* `adr-taxonomy-guard` job's own,
unrelated, correctly-hardcoded `repository: universaltill/ut-docs` line,
and reported success even though the actual bug was still present.

**Fix:** rewrote the isolation to stop at the next step at the same
indentation (a structural boundary — `^      - ` after the step's first
line), independent of any specific line's content inside the step.
Independently re-verified: reproduced the reviewer's exact adversarial
ci.yml (broken `repository:`, renamed `path:`, sibling job's line left
alone) and confirmed the guard now fails on it. Added that exact case as
a third scenario in `guard-docs-hub-checkout-repo_test.sh`, alongside the
original two (passes on the fix, fails on the literal historical bug).

Other findings from the review, no fix needed: guard/test pair's size is
proportionate to this repo's existing convention (every other one-off CI
misconfiguration risk gets the same guard+test-pair treatment); no
duplicate of any existing guard; no secret-shaped literals or demo
client/shop names in the diff; YAML still parses; new steps correctly
wired into the `build` job with matching indentation/ordering.

## Verified beyond automated tests

- `go build ./...`, `gofmt -l .`, `go vet ./...` — clean.
- `shellcheck` 0.9.0 (matches this repo's pinned baseline) on both new
  scripts — clean, both before and after the post-review fix.
- `python3 -c "import yaml; yaml.safe_load(...)"` — `ci.yml` parses.
- TDD red→green re-verified independently (not just trusting the
  script's own internal claim): reverted the real `ci.yml` line, ran the
  guard directly, confirmed a clear failure message, restored, confirmed
  pass again. Repeated for the post-review adversarial case.

## Deferred / out of scope

None — the two `blocked:env`-style AC items (secret existence, real-content
verification) were fully investigated and resolved above rather than
deferred, since this session had the means to check both.

## Verdict

Safe to merge.
