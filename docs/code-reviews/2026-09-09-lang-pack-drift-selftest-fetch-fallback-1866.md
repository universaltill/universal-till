# 2026-09-09 — lang-pack-drift self-test: tolerate an unreachable fixture fetch

Card: [ut-docs#1866](https://github.com/universaltill/ut-docs/issues/1866)
Branch: `fix/1866-lang-pack-drift-selftest-fetch-fallback`

## What shipped

`scripts/ci/check-lang-pack-drift.test.sh`'s `pack_script_source` fetches
each pack's real `check-key-drift.sh` live from `raw.githubusercontent.com`
when no local sibling checkout exists — always true on a bare CI runner.
That fetch ran, at load time, on every `lang-pack-drift.yml` trigger
including `push: branches: [main]` (no `paths:` filter, deliberately
blocking), with no fallback: a transient `raw.githubusercontent.com` outage
or shared-runner-IP rate limit would red-X the whole job on a push to
`main`, even though the real drift check was fine. That directly
contradicts the workflow's own stated intent ("an external pack repo
lagging for reasons that have nothing to do with this PR must never block
this PR's merge") on the one trigger meant to be immune to it.

Picked the issue's second proposed fix — a distinct "fixture unavailable"
exit code the calling step tolerates — over `continue-on-error: true` on
the whole step, because a blanket `continue-on-error` would also swallow a
genuine self-test assertion failure (the guard itself being broken), not
just a network hiccup. Vendoring a minimal fixture (the third option) was
rejected in the issue itself: it loses the "tests against the real,
current pack script" property the test file's own header says was a
deliberate design choice.

1. `scripts/ci/check-lang-pack-drift.test.sh`: `pack_script_source`'s
   final failure branch (live fetch failed, no local checkout to fall back
   to) now `return 2` instead of `return 1`. Every other failure path —
   `fixture_pack`'s unknown-mode guard, `start_server`'s port-detection
   failure, the final `$FAILS -ne 0` check, and (crucially) Case 4's
   `assert_fail_repos "es unreachable"` which exercises the *production*
   script's own unreachable-pack detection during a real test case, not
   this fixture-setup path — are untouched and still exit 1.
2. `.github/workflows/lang-pack-drift.yml`: the self-test step now runs
   under `shell: bash` and captures the test script's exit status
   explicitly (`|| status=$?`). Exit 2 emits a `::warning::` annotation and
   the step exits 0 (non-blocking); any other non-zero status still
   propagates and fails the step/job exactly as before.

## Verified beyond the automated suite

- Ran `check-lang-pack-drift.test.sh` unmodified against live network: all
  4 cases pass, exit 0 — confirms the fix doesn't touch the normal path.
- Simulated the actual bug: a copy of the test file with the raw-fetch
  host pointed at a nonexistent domain reproduces the original failure
  (`curl` errors, script exits) and confirms it now exits **2** specifically
  (not 1), with the pre-existing per-repo error message intact.
- Simulated the new workflow step's shell logic standalone
  (`status=0; bash test.sh || status=$?; if [ "$status" -eq 2 ]; then ...
  exit 0; fi; exit "$status"` under `shell: bash`) against that same
  simulated-failure script: captured status is 2, final step exit is 0, as
  designed.
- `python3 -c "import yaml; yaml.safe_load(...)"` on the workflow file:
  parses clean. `bash -n` on the test script: clean. No shellcheck/actionlint
  binary available in this sandbox to run additionally.
- No Go, template, or locale files touched — `gofmt`/`go build`/`go
  test`/`golangci-lint`/the `guard-*.sh` CI gates in `CLAUDE.md`'s "Before
  committing" list are all unaffected by this diff (CI-workflow and
  bash-test-harness only); not run here as they're out of scope for this
  change, not skipped despite being in scope.
- No UI/visible surface touched — the tester skill's screenshot/driven-run
  requirement doesn't apply; genuinely out of scope for this card, not
  skipped.

## Independent review (fresh-context Sonnet, per this card's `complexity:easy`
routing) — no findings

The reviewer independently re-derived and re-verified, rather than taking
this record's claims on faith:
- Live-reproduced that `return 2` from `pack_script_source`, called at
  top level under `set -euo pipefail`, actually propagates as the whole
  script's exit status — confirmed bash's real behavior rather than
  assuming it.
- Grepped every `return`/`exit` in the test file and confirmed exactly one
  `return 2`, with every other failure path unchanged at exit 1 — so a
  real assertion failure can never be silently downgraded to the
  non-blocking code. Also noted `pack_script_source` is fetch-cached and
  pre-fetched once at load time before any case runs, so a mid-run cache
  hit can never re-trigger the fetch-failure path once the load-time
  pre-check has already passed.
- Live-verified the workflow step's shell logic for all three exit codes
  (0/1/2) produces outer exit (0/1/0) respectively, under `shell: bash`'s
  actual `-eo pipefail` semantics — not just read as plausible.
- Confirmed no route exists for a genuine drift/regression finding to be
  reported as exit 2 (the guard's real detection logic, exercised by Case
  4, is on a different code path entirely, untouched by this change).
- Style/consistency: comments cross-reference the issue and the file's
  existing tone; no conflicting prior use of exit code 2 anywhere in the
  repo.

## Safe-to-merge verdict

Yes. Small, targeted, `complexity:easy` fix; independent review found
nothing to change.
