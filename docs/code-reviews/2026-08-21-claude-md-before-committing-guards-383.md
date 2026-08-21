# Code review: `CLAUDE.md`'s "Before committing" list omits several CI guards

- **Issue:** universaltill/ut-docs#383
- **Complexity:** easy
- **Change:** documentation only — `universal-till/CLAUDE.md`

## What shipped

`CLAUDE.md`'s "Before committing" bullet previously named only three of the
16 `scripts/ci/guard-*.sh` / `check-*.sh` scripts that `.github/workflows/
ci.yml`'s `build` job actually runs and blocks merge on (data-access,
kiosk-engine, plugin-menu-read). Missing: `guard-i18n.sh`,
`guard-compliance-claims.sh`, `guard-docs-shots.sh` (the one whose omission
originally caused a wasted CI round-trip on #413, per ut-docs#417),
`guard-help-topics.sh`, `guard-webkit-version.sh`,
`guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
`guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
`guard-autofill-suppression.sh`, `check-brand-assets.sh`,
`guard-makefile-version.sh`. Also added `gofmt -l .`, the build job's own
first step, which wasn't mentioned either.

The bullet now names all 16, in the order `ci.yml`'s `build` job runs them,
plus a note to treat `ci.yml` as the authoritative source if this list ever
drifts again (it already had, once, by the time this card was picked up —
the ticket's own snapshot from 2026-08-07 was itself stale against the
current workflow).

## Verification

- Diff is `CLAUDE.md` only (`git status --porcelain`) — no behavior change,
  matches the issue's "documentation only" acceptance criterion.
- Independently re-derived the guard list from `.github/workflows/ci.yml`'s
  `build` job (ignoring `e2e`/`contract` jobs and each guard's own
  `*_test.sh` regression self-test, which isn't a "before committing" check
  a developer runs) and confirmed every script named exists under
  `scripts/ci/` (`ls scripts/ci/`).
- Confirmed `check-lang-pack-drift.sh` is correctly excluded — it runs only
  in the separate `lang-pack-drift.yml` workflow, already documented
  elsewhere in `CLAUDE.md`'s i18n section as advisory/differently-scoped.

## Independent review

Fresh-context Sonnet subagent (per the `complexity:easy` review tier — a
different instance that never saw the edit being made) independently
re-enumerated the `build` job's guard steps against `ci.yml` and cross-
checked script existence and the exclusion list. **Verdict: PASS**, no
findings — exact name-for-name, order-for-order match; no test-script
leakage; no scope creep; no contradiction with other `CLAUDE.md` sections.
Full transcript available in the pipeline session that ran this cycle.
