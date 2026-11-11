# Code review — a release never ships an ad-hoc / un-notarized .dmg (ut-docs#6025)

- Card: universaltill/ut-docs#6025 (follow-up from #6024's review finding 1).
- Branch: `fix/6025-macos-release-refuse-unsigned`.
- Pre-flight: `release.yml` `macos-app` `updated`; `docs/arch/desktop-app.md`
  Signing & Gatekeeper + CI section `updated`; `packaging/macos/*.sh` comments
  (right-click → Open dropped, #3803) `updated`; release notes footer `n/a`
  (#6024 already promises signed + notarized; this change makes it true);
  `README.md` `n/a` (no signing claim beyond #6024's); `web/help/` `n/a` (no
  help topic carries Mac install/signing); `en.json` `n/a`; ADR `n/a` (no
  course change: ADR-less enforcement of an existing promise).
- Process: Dev on Sonnet, Tester on Sonnet (separate subagent), Reviewer on
  Opus 5.5 (separate subagent, different model, in a detached worktree at the
  WIP commit).

## What changed

- New `scripts/ci/check-macos-release-secrets.sh`: env-only preflight. In
  `universaltill/universal-till` (exact match on `RELEASE_REPO`) it fails
  unless the signing trio (`MACOS_CERT_P12`, `MACOS_CERT_PASSWORD`,
  `MACOS_SIGN_IDENTITY`) is set and one notary trio (API key, or Apple ID) is
  complete. Prints secret names only, never values; whitespace-only counts as
  unset. Forks pass with a `::notice::`. Production shape (API trio complete,
  Apple-ID trio partial — only `MACOS_NOTARY_TEAM_ID`) passes.
- `release.yml` `macos-app`: new unconditional first step after checkout
  ("Refuse an unsigned or un-notarized release .dmg"), so tag pushes and
  `workflow_dispatch` are both covered; it fails in seconds, before the release
  wait and the ~15 min build. The build step sets
  `MACOS_REQUIRE_NOTARIZED` to `1` upstream only (literal-only expression, no
  injection surface).
- `packaging/macos/make-dmg.sh`: with `MACOS_REQUIRE_NOTARIZED=1` it refuses
  before `hdiutil` when there is no identity, and refuses after signing when
  `notary-args.sh` found no complete credentials — a backstop if the preflight
  and `notary-args.sh` ever disagree. Unchanged without the flag (forks/local).
- Comments in `build-app.sh` / `make-dmg.sh` / `release.yml`: right-click →
  Open replaced by Open Anyway / the bundled `.command` (#3803).
- Tests, all wired into `ci.yml`'s `build` job:
  `check-macos-release-secrets_test.sh` (17 cases + no-`set -x` pin, sentinel
  values must never appear in output), `release-macos-signing-required_test.sh`
  (wiring + 6 mutant families self-test), `make-dmg-notarized_test.sh` (stubbed
  `hdiutil`/`codesign`/`xcrun`/`spctl` in a mktemp copy, plus a mutation
  self-test deleting each refusal branch).

## Findings (Opus review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | medium | (Tester D1) the wiring test's make-dmg pin was `grep -c MACOS_REQUIRE_NOTARIZED >= 2`; deleting either refusal branch stayed green. | Fixed before review: `make-dmg-notarized_test.sh` drives make-dmg.sh and its self-test proves each branch deletion goes red (re-verified below). |
| 2 | nit | make-dmg.sh comment read "opens the app, for / so a quarantined…". | Fixed by reviewer. |
| 3 | nit | `set -x` pin regex misses `set -o xtrace`. | Accepted: secrets are also masked by Actions; the sentinel-leak cases catch any value echo. |
| 4 | nit | Preflight is stricter than `notary-args.sh` (whitespace-only = unset; `MACOS_NOTARY_PROFILE` not accepted). | Accepted: stricter is the safe direction; a keychain profile cannot exist on a fresh CI runner. |
| 5 | nit | A refused build after signing leaves a signed, un-notarized `.dmg` in `dist/`. | Accepted: the job fails, so `upload-artifact` (no `if:`) and `macos-dmg-attach` (needs `macos-app` success) never see it. |
| 6 | nit | A job-level `if:` on `macos-app` gating on event name would evade the step-level pin. | Accepted: the job `if:` is pinned by other release tests; low value. |

Checked and clean: no secret value printed (no `set -x`, names only);
`github.repository` reaches the script via `env:`, never inlined in `run:`;
repo gate is an exact string compare (near-miss `universal-till-fork` is a
fork); trio semantics match `notary-args.sh` (API key first; partial API +
complete Apple ID passes in both).

## Verification

- Ran: the three new tests, every `release-*_test.sh` and
  `guard-release-*_test.sh` — all pass. `shellcheck scripts/ci/*.sh
  packaging/macos/*.sh` 0 issues. `guard-ci-tests-sandboxed.sh` passes. All 60
  guards in `ci.yml` pass except `guard-catimport-version.sh` (no catimport
  tags fetched in this worktree) and `guard-deadcode-baseline.sh` (local Go
  1.26 < go.mod 1.27) — both environmental, no Go/catimport change here.
- TDD re-verified by the reviewer (revert → red → restore → green):
  - `release.yml` at `origin/main` → wiring test 26 FAIL (no preflight step,
    ordering, each secret).
  - `make-dmg.sh` at `origin/main` → behaviour test red on all three
    REQUIRE=1 refusal cases and "left a .dmg".
  - check script neutered to `exit 0` → 4a/4b/4c… red; partial-trio-as-complete
    mutant → cases 6, 7, 15 red; value-echo mutant → leak caught; prefix
    repo-gate mutant → case 13 red.
  - `macos-app` added to `publish-release.needs` →
    `release-macos-nonblocking_test.sh` red, so a refusal still never holds the
    other platforms' release (ut-docs#2917).
- Tester mutation table (mktemp copies): A–G, I, J caught; H/H2 (single
  make-dmg branch deletion) were the D1 gap, now caught by
  `make-dmg-notarized_test.sh`.

## Verdict

Approved. No blockers.

## Deferred

None.
