# Code review — nav rail Lock button reachability regression test

- **Date:** 2026-09-01
- **Branch:** `fix/1346-nav-rail-lock-reachable-regression-test`
- **Reviewer:** independent review, fresh-context Sonnet subagent
  (`complexity:easy` per the model-routing table — test-coverage-only
  change, no Go source touched)
- **Verdict:** Safe to merge. No blockers found.

## Context

ut-docs#1346: independent review of universal-till PR #670 (ut-docs#1332's
nav-rail change) simulated the real `session_chip.html` markup (3 manager
admin links + operator + Lock) on `/settings` at 1024x600 and measured
`.nav` scrollHeight 614 vs clientHeight 600 (overflows by 14px) — Lock's
own bottom edge ~4px below the rail's clientHeight. Not broken today (the
rail is `overflow-y: auto`, and Lock stays hit-testable at its centre via
scroll), but with zero headroom left: one more rail item (a plugin nav
entry, or a sync-chip/fiscal-chip wrapping to two lines) could push Lock
further off-screen with nothing to catch it. Grooming's own read: "this is
a test-coverage gap, not a behavior change" — no product decision needed,
just the regression test the original review recommended.

## What changed

- `e2e/tests/nav-rail-lock-reachable-1346.spec.ts` (new): mirrors
  `tender-panel-reachable.spec.ts`'s scrollable-ancestor pattern — real
  hit-test (`scrollIntoViewIfNeeded` + `elementFromPoint`, not a bounding-
  box/isVisible check) plus a real, completing click (Lock → logout →
  `/login`), not a forced one.
- `e2e/playwright.config.ts`: added the new spec to `AUTH_ONLY_SPECS`. The
  `#session-chip` fragment this test measures only renders once
  `auth.FromContext` resolves a real session — on the default
  (`UT_AUTH=off`) project `auth.Middleware` is never installed at all
  (`internal/pages/init.go`'s `authDisabled` branch returns
  `recoverMiddleware(mux)` directly, skipping `auth.Middleware` entirely),
  so nothing ever populates that context and the chip renders empty
  regardless of `canPerform()`'s bypass. Confirmed live: 0
  `.session-admin-link` elements on the default project's `/settings`, not
  3.
- `scripts/ci/guard-e2e-fixtures-import.sh` + `_test.sh`: extended the
  single-file `login.spec.ts` exemption to an `EXEMPT_FILES` list including
  the new spec, for the same underlying reason — `fixtures.ts`'s
  `resetPosOncePerFile` auto-fixture posts through a bare `request` context
  with no session cookie, which would 401 against the auth-gated `auth`
  project.

## Verification

| Check | Result |
|---|---|
| `bash scripts/ci/guard-e2e-fixtures-import_test.sh` | all 9 cases pass, incl. 2 new exemption cases |
| `bash scripts/ci/guard-e2e-fixtures-import.sh` (real tree) | 62 specs checked, both exemptions recognized |
| `gofmt -l .` / `go build ./...` | clean (no Go files touched) |
| `npx playwright test --project=auth --list` | confirms file-sort order: `login.spec.ts`'s 15 tests before the new spec's 1 |
| `npx playwright test --project=auth` (full run) | 16/16 passed, ~24s |
| `npx playwright test --project=default --list` | new spec correctly excluded (0 matches) |

**TDD-style proof the test has real teeth** (no production fix exists here
to revert, so the equivalent proof is: does the test actually detect the
regression class it claims to?): temporarily changed `.nav`'s
`overflow-y: auto` to `visible` in `web/public/app.css` (removing the
rail's scroll escape entirely — the failure mode where Lock becomes
genuinely unreachable, not just scrolled-past) and reran the spec — it
failed with `Lock must be the real hit-test target, not clipped by the
rail running out of headroom`, `Received: false`. Reverted the CSS
(`git checkout -- web/public/app.css`, confirmed clean diff) and reran —
green again, along with the rest of the `auth` project suite (16/16,
including `login.spec.ts` unaffected).

## Independent review findings

Full pass by a fresh-context Sonnet subagent, re-deriving every claim from
the actual source rather than trusting the diff's own comments:

1. **Core claim verified correct.** Traced `auth_page.go`'s
   `GET /ui/session-chip` → `init.go`'s `authDisabled` branch →
   `middleware.go`'s `Middleware` (the only place that ever populates
   `auth.FromContext`) independently and confirmed the chip is genuinely
   empty on the default project, not just asserted so in a comment.
2. **Locators and hit-test logic correct**, faithfully mirroring
   `tender-panel-reachable.spec.ts`'s scrollable-ancestor case (not its
   always-visible bottom-edge-probe variant — correctly, since `.nav` is
   `overflow-y: auto`, not a fixed footer). `IsManager()` recognizes the
   wizard-created `admin` operator, so the `toHaveCount(3)` sanity check is
   sound.
3. **Ordering safety is real today but rests on alphabetical file-sort**,
   not an explicit dependency — flagged as a pre-existing fragility
   `login.spec.ts` itself already carries (same "I run alone in this
   project" assumption), not something this diff makes worse. No blocker;
   noted for future awareness (e.g. a future `AUTH_ONLY_SPECS` addition
   sorting before `login` would silently reintroduce the same race).
4. **Test hygiene confirmed clean** — fresh cookie-less context via
   `ensureOperator`, never `login.spec.ts`'s shared serial-block page;
   separate server/port entirely.
5. **Guard script refactor is correct bash** — `EXEMPT_FILES` array +
   `is_exempt()` inside a `set -e` loop behaves correctly (confirmed
   empirically against all 62 real spec files); the new test case
   meaningfully exercises the new behavior rather than copy-pasting the
   old one.
6. **Scope of protection, not a defect**: the test cannot fail purely from
   "more rail items push Lock further down" as long as the rail stays
   scrollable — only when Lock becomes genuinely unreachable (non-
   scrollable overflow, collapse, occlusion). This is the deliberate,
   correctly-mirrored design the issue's own acceptance criterion asks for
   ("hit-testable," not "never needs scroll").

No blockers. No changes required as a result of this review.

## Scope note

No production behavior changed — `web/ui/partials/session_chip.html` and
`web/public/app.css`'s `.nav` rail rules are untouched. This is
test-coverage only, matching grooming's own read of the card.
