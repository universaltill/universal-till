# Code review: CI guard against unlocked reads of `Pm.Installed`/`Pm.MenuPlugins`/`Menu` (ut-docs#489)

**Date:** 2026-08-08
**Card:** [ut-docs#489](https://github.com/universaltill/ut-docs/issues/489)
**Complexity:** easy
**Author (Dev):** inline (scrum-master session, Sonnet)
**Reviewer:** independent fresh-context Sonnet subagent (round 1)

## What shipped

`ut-docs#478` added three locked read accessors on `common.Deps` —
`MenuSnapshot()`, `InstalledPlugin(id)`, `MenuPluginByKey(key)` — and swept
every read site under `internal/pages/**` onto them, because
`Manager.Reload` reassigns `Deps.Menu`, `Pm.Installed` and `Pm.MenuPlugins`
inside one critical section under `Deps.PluginMu`, and — since the
background sync-pull goroutine (`ut-docs#460`) calls `Reload` every 30s —
an unlocked concurrent read of any of the three is a fatal Go "concurrent
map read and write" crash, not just stale data. That invariant was enforced
only by a doc comment on `PluginMu`, mechanically unguarded.

Adds `scripts/ci/guard-plugin-menu-read.sh`: a grep-based CI guard, modeled
on `guard-kiosk-engine.sh`/`guard-data-access.sh`, that fails if any file
under `internal/pages/**` (excluding `internal/pages/common/deps.go` itself
and `_test.go` files) contains an unlocked reference to `.Pm.Installed[`,
`.Pm.MenuPlugins[`, or `.Menu` (word-boundaried). Receiver-name-agnostic
from the start (`[A-Za-z_][A-Za-z0-9_]*\.` prefix) — this package is
inconsistent about the `*common.Deps` receiver name (`d`/`dp`/`deps`), a
lesson pulled forward directly from `guard-kiosk-engine.sh`'s own round-1
review finding rather than repeating that bypass here. Full-line comments
are stripped before matching, so prose that merely mentions these fields
(the style `deps.go`'s own doc comments already use) doesn't false-positive.
Wired into `.github/workflows/ci.yml` next to the kiosk-engine guard pair,
with a regression test (`guard-plugin-menu-read_test.sh`) following the same
plant/expect_fail/expect_pass/trap-cleanup fixture pattern as the sibling
guards' tests. Documented in `universal-till/CLAUDE.md`'s "Before
committing" list.

## Independent review

A fresh-context Sonnet subagent, given no prior reasoning, read
`internal/pages/common/deps.go` for ground truth and the two sibling guards
for convention, then actually executed (not just read) both new scripts and
cross-checked with its own greps:

- Ran `guard-plugin-menu-read.sh` directly — clean pass (exit 0) on the
  current codebase; independently grepped `internal/pages` for `.Menu\b` and
  `.Pm.(Installed|MenuPlugins)` and confirmed every hit outside `deps.go` is
  in a `_test.go` file or a comment.
- Ran `guard-plugin-menu-read_test.sh` — all 8 cases pass (unlocked
  `Pm.Installed[`, unlocked `Pm.MenuPlugins[`, unlocked `Menu`, alt-receiver
  `dp.Pm.Installed[`, comment-only false-positive, locked-accessor
  false-positive, `_test.go` exemption, clean-codebase baseline); confirmed
  the fixtures are real planted files against the real script, not mocked.
- Confirmed CI wiring is correctly placed (same naming/step-order pattern as
  the kiosk-engine guard pair) and `CLAUDE.md`'s "Before committing" list is
  correctly updated.
- Confirmed the script's own header comment satisfies acceptance criterion 4
  (tells future authors where to extend the pattern list if `Manager` grows
  another critical-section field).
- Flagged two accepted, non-blocking tradeoffs, both pre-existing in the
  sibling `guard-kiosk-engine.sh` and verified absent from the current
  codebase rather than just asserted: (a) one level of variable aliasing
  (`pm := d.Pm; pm.Installed[id]`) would evade the regex — checked, no such
  aliasing exists anywhere in `internal/pages` today; (b) the bare `.Menu`
  pattern would flag any unrelated struct field literally named `Menu`
  under `internal/pages` — checked, none exists today, and `MenuItem`/
  `MenuSnapshot`/`BaseMenu`/`MenuPluginByKey`/`MenuPlugins` all correctly
  fail to match thanks to the `\b` word-boundary placement.

**Verdict: APPROVE — no blockers found.** One review round only, per this
pipeline's default: nothing blocker-class (money/tax, data loss, security)
surfaced, so no second round was earned.

## Verified beyond the guard's own regression test

- `go build ./...` and `go test ./...` (full suite, run once after the
  script/CI/doc changes — see this pipeline's gate-once rule): clean.
- `bash scripts/ci/guard-data-access.sh` and `bash
  scripts/ci/guard-kiosk-engine.sh` (the sibling guards, to confirm no
  interference) still pass.
- No help-manual or i18n surface touched — CI-only, developer-facing change
  with no shop-owner-visible behavior, so no `web/help/` topic update
  applies.
- Working tree confirmed clean of fixture artifacts (the test script's own
  `trap cleanup` plus a manual `git status --short` check before commit).

## Files changed

- `scripts/ci/guard-plugin-menu-read.sh` (new)
- `scripts/ci/guard-plugin-menu-read_test.sh` (new)
- `.github/workflows/ci.yml` — two new steps, next to the kiosk-engine guard pair
- `CLAUDE.md` — "Before committing" list updated
