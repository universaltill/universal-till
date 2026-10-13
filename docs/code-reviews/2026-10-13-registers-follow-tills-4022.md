# Code review — registers follow their till's name (ut-docs#4022)

**Date:** 2026-10-13 · **Lane:** `lane:cloud-41` · **Branch:** `feat/4022-registers-follow-tills`
**Author model:** Opus 5.5 · **Reviewer model:** Fable (independent subagent, separate worktree)

## What shipped

The owner found Settings → Registers confusing. Register names drifted from
their tills, a stock "Default Register" showed up, and inactive leftovers
were mixed in with the active registers. Under the owner's 2026-10-09
decision, registers get restructured together with satellite mode (#1154,
ADR-0086: satellites have no register). So this change builds only what
stays true under that model:

1. **Seeded name.** `POSRepo.EnsureRegister` names the first register
   (`reg-default`) after `till.name`. If the till has no name, it uses
   `store.name` (never 001's "My Store" placeholder). It falls back to
   "Default Register" only when neither is set. A name that is already
   taken gets a " (2)"… suffix.
2. **Migration 072.** Renames an installed till's `reg-default` that is
   still called exactly "Default Register" the same way, if the name is free.
   It is data-only, NULL-safe and idempotent, and its checksum is pinned.
3. **Rename sync, main till only.** `registers` is primary-wins synced.
   - Renaming the main till (Settings `till.name`, or the cloud
     `rename_till`) renames its own register. That is the persisted
     `sync.till_register_id` if still active, else the single active
     register, else nothing.
   - A joined till's reported rename (`applyReportedTillName`, #3294)
     renames the register enrolment created for it. That register's id is
     read from the till's `till_enrolled` audit row.
   - A name another register holds is skipped (logged). It never fails the
     till rename. Each rename is audited as `register_renamed_with_till`.
4. **Registers page.** Active registers come first. Inactive ones follow
   in their own `<tbody>` under an "inactive (N)" row. This reuses the
   existing `registers.inactive` key, so there are no new locale keys and
   no language-pack follow-ups.
5. **Help.** One new `multitill` bullet in en/de/fa/tr/ar. The
   help-drift baseline for ar/fa/tr is re-recorded (+1 top bullet on both
   sides).

Deferred to #1154 (comment posted there): a till column on the Registers
page (the main till has no stored till→register link), and folding the
page into Settings → Tills.

## Findings (Fable review) and triage

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | blocker | help-drift baseline not updated for ar/fa/tr `multitill` | **Fixed**: the guard passes. |
| 2 | major | the joined-till rename found the register by the till's *old name*. Enrolment suffixes on collision ("Bar (2)"), so a manager's own "Bar" register would be renamed instead | **Fixed**: lookup by id from the `till_enrolled` audit row (`POSRepo.EnrolledRegisterID`). The test now seeds exactly that scenario. An enrolment without a recorded register renames nothing (new test). |
| 3 | minor | help example "inactive … from a till that was unpaired" implied automatic deactivation, which doesn't happen | **Fixed** in all five locales ("one you deactivated after unpairing its till"). |
| 4 | minor | the search filter leaves the "inactive (N)" row visible when no inactive row matches | **Accepted**: cosmetic, and the rows and the no-results row still behave. Not worth new JS on a page #1154 will restructure. |
| 5 | minor | `list-group-row` class had no CSS rule | **Fixed**: class dropped (the `muted` cell carries the style). |
| 6 | nit | doc said "logged" but nothing was logged | **Fixed**: `Infof` on a skipped rename. |
| 7 | nit | `EnsureRegister` errors if an *inactive* row already holds the id `reg-default` | **Accepted**: pre-existing on `main`, unchanged by this diff. |

The reviewer checked these and found them sound: migration SQL (NULL
handling, idempotency, replica behaviour), the single-statement collision
check (no TOCTOU), resolving the own register without guessing, no
remaining "Default Register" dependents in `web/`, `e2e/` or `internal/`,
template scoping, translation terminology, and no file writes or paths in
the diff.

## Verified beyond automated tests

- **TDD:** each new data test was run against `main`'s `pos_repo.go` and
  failed with the claimed error ("Default Register" ≠ "Counter",
  undefined methods). The pages tests failed with the hooks disabled and
  pass with them.
- **Driven run of the real binary** (`UT_AUTH=off`, throwaway data dir):
  1. Set `till.name` = "Front Counter", then `GET /shifts` seeded
     `reg-default` named "Front Counter".
  2. `POST /api/settings/till-name` "Window Counter" returned 204. The
     register was renamed and both audit rows were written.
  3. `/registers` was screenshotted at 1024×600, 360px and in fa (RTL),
     and the screenshots were looked at. Active rows come first, then the
     "inactive (2)" group. The row layout is unchanged and RTL mirrors
     correctly.
- **Gate:** `gofmt`, `go build`, `golangci-lint` (0 issues), every
  `build`-job guard, and `go test ./...`. Two guards can't run in this
  container, and neither failure comes from this diff (CI runs both):
  - `guard-shellcheck-version`: no shellcheck binary here.
  - `guard-deadcode-baseline`: x/tools v0.48 rejects this container's
    Go 1.27.

## Verdict

Safe to merge once CI is green. No money or tax paths are touched, the
migration only renames a seeded name, and every rename is best-effort and
audited.
