# Code review: widen guard-plugin-settings-bump.sh scan beyond internal/

**Date:** 2026-09-09
**Card:** ut-docs#1942 — "guard-plugin-settings-bump.sh: extend writer-call
scan beyond internal/ (cmd/, scripts/, e2e/)" — follow-up from the
independent review of ut-docs#1357 (universal-till#996)
**Branch:** `fix/1942-guard-plugin-settings-bump-widen-scope`
**Design:** no ADR — mechanical scope-widening of an existing CI guard,
not a new architectural decision (complexity:easy)
**Diff:** `scripts/ci/guard-plugin-settings-bump.sh`,
`scripts/ci/guard-plugin-settings-bump_test.sh`
**Build model:** Sonnet (card is `complexity:easy`)
**Reviewer:** independent (fresh-context Sonnet subagent, isolated
worktree, did not write the code and did not see its reasoning)

## What shipped

`guard-plugin-settings-bump.sh` (ut-docs#1357) enforces that any
production `.go` file calling `UpsertPluginSetting`/
`UpsertPluginSettingScoped`/`MergeAdditiveJSONMapSetting` also references
`plugins.SharedBus(db).BumpGeneration()` in the same file, or carries an
inline `plugin-settings-bump:allow` escape hatch on that exact line. It
previously scanned only `internal/*.go`. This change widens the scan to
also cover `cmd/`, `scripts/`, and `e2e/` (same exclusions throughout:
`*_test.go`, `*/testdata/*`), guarded by a per-directory existence check
that fails loudly (mirroring the pre-existing `internal/`-missing check)
rather than silently skipping a directory that's been renamed or moved.

Zero real call sites of the three writer methods exist outside
`internal/` today — confirmed by a repo-wide grep at both design and
review time — so this is a pure safety-net widening: no behavior change
for any current caller, only for a future seed/smoke tool that doesn't
exist yet.

Three new regression cases were added to
`guard-plugin-settings-bump_test.sh`, one per newly-covered directory
(planted in real `package main` locations: `cmd/unitill-uninstall`,
`scripts/e2e_seed`, `e2e/seed_demo`), each proving a writer call with no
same-file `BumpGeneration()` reference is now caught the same way the
existing `internal/pages` case already was.

## TDD

Written test-first: the three new cases were added and run against the
*unwidened* guard first, confirmed to fail with the exact symptom
(`expected guard to reject ... but it passed`), then the guard was
widened and the same three cases confirmed to pass. The independent
review re-verified this itself (see below) rather than taking the claim
on trust.

## What the independent review found

**Verdict: SAFE TO MERGE. No blockers.**

The reviewer:
- Ran the widened guard against the real codebase (clean pass) and the
  full regression suite (10/10 cases pass, including the 3 new ones).
- Confirmed no fixture-file leakage after the test run.
- Independently re-verified the TDD claim: swapped the pre-fix guard
  script back in against the *current* test script and reproduced the
  claimed failure exactly — only the 3 new cases failed, with the exact
  claimed message; the other 7 pre-existing cases still passed against
  the old guard (confirming the old guard wasn't broadly broken, just
  missing the newly-covered trees). Restored the new guard and
  reconfirmed all 10 pass; worktree ended clean (`git status --porcelain`
  empty).
- Verified `cmd/unitill-uninstall`, `scripts/e2e_seed`, `e2e/seed_demo`
  are real `package main` directories in the checkout, not invented
  paths.
- Grepped the whole repo for real (non-test/non-fixture) writer-method
  call sites under the three newly-covered trees: none exist — the
  "zero behavior change today" claim is verified, not just asserted.
- Checked for symlinks/vendored/generated files under the four scanned
  trees that could trip the widened scan unexpectedly: none found.
- Confirmed diff scope is exactly the two intended files
  (`git diff --stat`), no secrets/credentials/real client names present.
- Checked the two recurring bug classes this pipeline has been bitten by
  before (`os.MkdirAll` before a file write, cwd-relative path instead of
  `paths.Data(...)`): neither applies — this script does no writes to
  product data paths, only reads (`grep`/`find`) plus test-fixture
  writes, and already `cd`s to `ROOT_DIR` before any relative path use,
  unchanged by this diff.
- Confirmed none of `universal-till/CLAUDE.md`'s enforced rules
  (repository pattern, money, i18n, offline-first, kiosk isolation) apply
  — pure CI shell tooling, no Go production code, no SQL, no UI strings,
  no money.

No findings, blocking or otherwise, beyond the above.

## What was verified beyond automated tests

- `gofmt -l .` clean, `go build ./...` clean, `go vet ./...` clean.
- Full `go test ./...` across every package: all green, no regressions.
- `golangci-lint run ./...`: 0 issues.
- `bash scripts/ci/guard-data-access.sh` and `bash scripts/ci/guard-i18n.sh`:
  both pass (unaffected by this change, run as part of the standard gate).
- No UI surface touched — this is backend/CI tooling only, so the visual-
  check attestation and UX-guidelines checklist don't apply.
- No user manual topic applies — nothing a shop owner sees or does
  changed.

## Safe-to-merge verdict

Safe to merge. No deferred items — the change is fully scoped to its
stated acceptance criteria with no follow-up gaps identified.
