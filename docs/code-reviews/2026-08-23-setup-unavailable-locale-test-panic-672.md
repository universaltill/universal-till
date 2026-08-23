# CI locale-branch coverage gap + test-harness panic (ut-docs#672)

**Reviewer**: independent Sonnet subagent, fresh context, isolated worktree
(`complexity:easy` — Sonnet built it, a fresh-context Sonnet instance
reviewed it, per the scrum-master skill's model routing exception for easy
cards). One round — no blocking findings, nothing to fix.
**Branch**: `fix/672-setup-unavailable-locale-test-panic` (base: `main`).
**Date**: 2026-08-23.

## What shipped

Follow-up from the independent review of ut-docs#662/#775-class findings.
`GET /setup`'s language detection has two outcomes: language available
(redirect) and language detected-but-unavailable (renders in English,
writes `setup.detected_lang_unavailable` via `d.Settings.Set(...)`,
`internal/pages/setup_page.go:223`). ut-docs#662's CI locale step
(`LANG=en_GB.UTF-8`) only ever exercises the first branch — `en` is always
shipped. The second branch is untested against a real OS-locale env var in
CI, and the one fixture cheap enough to reach it accidentally
(`newAuthTestMux`, used by `auth_page_test.go`) leaves `common.Deps.Settings`
`nil`, so any request reaching that branch through it panics the whole
`internal/pages` test binary rather than failing one assertion.

- **`internal/pages/auth_page_test.go`** — `newAuthTestMux` now wires a
  real `internal/settings.Store` into `common.Deps` (schema-identical
  `settings` table to `newFullAuthDeps` in `setup_page_test.go`, the
  existing precedent this mirrors). Added
  `TestFirstBootSetupUnavailableLanguageDoesNotPanic`: mocks OS locale to
  `de_DE.UTF-8` (German isn't shipped — `web/locales/` has only
  `ar/en/fa/tr`), hits `GET /setup` through this fixture, asserts `200`
  and that `setup.detected_lang_unavailable` was persisted as `"de"`.
- **`.github/workflows/ci.yml`** — a third `build`-job step, same shape
  and scope as the existing `en_GB.UTF-8` step (`internal/pages/...` only,
  not the whole tree — the workflow's own comment on ut-docs#674's
  double-run incident is respected, not repeated), running with
  `LANG=de_DE.UTF-8`/`LC_ALL=de_DE.UTF-8` so the unavailable-language
  branch actually runs against a real OS-locale env var in CI, not just a
  mocked seam.

No SQL outside `internal/data`/`internal/db` (none added — test fixture
only), no new user-facing strings (no template/JS/locale file touched),
no UI surface at all: the diff is exactly two files, a Go test file and a
CI workflow file.

## Independent review — TDD re-verification (done by the reviewer subagent, not taken on trust)

The claim: `TestFirstBootSetupUnavailableLanguageDoesNotPanic` panics
against the pre-fix `newAuthTestMux` (nil `Settings`) and passes clean
after wiring the real store. The reviewer, in an isolated worktree,
reverted only the `newAuthTestMux` hardening (kept the new test), reran
the test, and got the exact panic the issue predicted:

```
panic: runtime error: invalid memory address or nil pointer dereference
github.com/universaltill/universal-till/internal/settings.(*Store).Set(...)
    internal/settings/settings.go:25
github.com/universaltill/universal-till/internal/pages.registerSetup.func2(...)
    internal/pages/setup_page.go:223
```

Restored the fix, reran, passed clean. **The TDD claim is genuine** — this
was also independently confirmed by the implementer before handoff (same
panic trace, same line numbers, reproduced against the unfixed code before
writing the fix).

## Findings

None blocking. Two non-blocking notes from the reviewer, neither acted on
(out of scope for this card, noted here rather than silently dropped):

- The new CI step is a third ~51s run of `internal/pages/...` in the same
  `build` job (now three full runs of the same package across the default,
  `en_GB.UTF-8`, and `de_DE.UTF-8` steps). Not a defect — correctly scoped,
  avoids the ut-docs#674 double-run failure mode — just worth watching if
  this per-locale-step pattern keeps growing.
- `newAuthTestMux`'s `settings` table DDL is now a second hand-rolled copy
  of the same schema `newFullAuthDeps` already has (byte-identical today).
  Same drift risk this package's other fixture comments already flag for
  `country_settings`/`role_permissions` — not this card's job to
  deduplicate.

## Verified beyond automated tests

- `gofmt -l .` clean, `go build ./...` clean, `go vet ./...` clean.
- Full `go test ./...` (excluding `internal/plugins`, run separately per
  CI's own split) green, plus `go test -timeout 20m ./internal/plugins`
  green.
- `go test ./internal/pages/...` run three ways — default env,
  `LANG=en_GB.UTF-8`, and `LANG=de_DE.UTF-8` (the new CI step's exact
  env) — all green, independently re-run by the reviewer, not just
  trusted from the implementer's run.
- `bash scripts/ci/guard-data-access.sh` and `bash scripts/ci/guard-i18n.sh`
  both ✓, independently re-run.
- Confirmed `de` is genuinely unshipped by reading `web/locales/` directly
  (`ar.json`, `en.json`, `fa.json`, `tr.json` — no `de.json`), not just
  trusting the comment.
- Checked `newAuthTestMux`'s other two callers
  (`TestFirstBootSetupThenLogin`, `TestUsersPagePermissions`) for any
  behavior that implicitly depended on `Settings` being `nil` or the
  `settings` table being absent — neither does; wiring the real store is
  purely additive.
- No secret-shaped literal, no real client/shop name anywhere in the diff.
- No UI surface, no i18n/UX/manual-topic implications — confirmed by
  file list (test file + CI workflow file only), not assumed.

## Safe-to-merge verdict

**Yes.** Small, well-scoped, TDD-proven fix with an independently
re-verified panic-to-pass transition and a clean full gate on both
sides of the review.
