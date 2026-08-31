# 2026-08-31 — e2e cross-spec state leakage: systemic fix (ut-docs#1315)

## What shipped

`e2e/tests/*.spec.ts` (57 files) all share ONE live till server process
(`playwright.config.ts` pins `workers: 1`), so a spec file that leaves
state behind (a stray basket item, discount, table assignment) could make
an unrelated **later** file's exact-total/exact-copy assertions fail —
purely from alphabetical run order. ut-docs#1310 was one hand-fixed
instance (`settings-osk.spec.ts`'s cancelled hold-sale dialog leaking a
basket item into `split-tender-i18n-925.spec.ts`'s fa/RTL test). This
card is the systemic backstop:

1. **`e2e/tests/fixtures.ts`** — wraps Playwright's `test` via
   `base.extend<{ resetPosOncePerFile: void }>` with an auto fixture that
   calls the existing `POST /api/pos/reset` endpoint exactly once per
   spec *file* (tracked via a module-level `Set<string>` keyed by
   `testInfo.file`, valid because of `workers: 1`), before that file's
   first test body. A file's own tests still see each other's basket
   state exactly as before (e.g. `tender-panel-reachable.spec.ts` holding
   several sales in a row within one test) — only what a *different*
   file left behind gets cleared.
2. **56 of 57 spec files** migrated from
   `import { test, expect } from '@playwright/test'` to
   `import { test, expect } from './fixtures'` (mechanical, one line per
   file; 3 files that also imported `Page`/`type Page` got a second,
   type-only import line). `login.spec.ts` is the deliberate exception —
   it drives the separate `auth` project against a genuinely fresh,
   never-set-up till, where a basket reset pre-wizard is meaningless, and
   the two projects run on separate server processes so it can't leak
   into or from `default`-project specs anyway.
3. **`scripts/ci/guard-e2e-fixtures-import.sh`** (+ regression test) —
   new CI guard, wired into `.github/workflows/ci.yml`'s `build` job and
   `CLAUDE.md`'s guard list, failing the build if a non-exempt spec
   imports `test` directly from `'@playwright/test'` instead of
   `./fixtures` — so a new spec can't silently opt back out the way
   #1310 happened one file at a time.
4. **`e2e/README.md`** documents the new import convention, the
   `login.spec.ts` exemption, and the `beforeAll`-ordering caveat (below).

## Independent review

Spawned via `Agent` (`general-purpose`, `model: opus`, `isolation:
"worktree"` — this card is `complexity:medium`, reviewed one tier up per
the scrum-master skill's model-routing table). The review ran, not just
read: the full `--project=default` e2e suite, both new guard scripts, an
empirical Playwright fixture-lifecycle probe, `go build`/`go vet`/
`gofmt`, a full diff scan for the two recurring bug classes (missing
`os.MkdirAll`, cwd-relative paths instead of `paths.Data(...)`) and for
secrets/real-shop-names, and — going beyond what was asked — wrote two
throwaway specs to *prove* the leak and the fix empirically (a spec that
deliberately leaves a stray basket item, run against a victim spec
importing from `'@playwright/test'` directly vs. from `./fixtures`; only
the latter stayed clean). Verdict: **safe to merge**, 3 non-blocking
findings, all fixed before commit:

1. **Ordering caveat**: the auto fixture runs *after* a file's
   `test.beforeAll` (Playwright fixture lifecycle), not before it as the
   original comment implied — a future spec seeding basket state in
   `beforeAll` would have it silently wiped. No live instance today
   (verified: only the exempt `login.spec.ts` uses `beforeAll`).
   **Fixed**: clarified the wording in `fixtures.ts`,
   `guard-e2e-fixtures-import.sh`, and `e2e/README.md`.
2. **Guard bypass**: the original regex was single-line, single-quote
   only — `import { test } from "@playwright/test"` (double quotes) or a
   multi-line import both silently passed. The repo has no
   prettier/eslint/editorconfig pinning quote style, so nothing else
   would have caught either. **Fixed**: rewrote the match as a slurped
   (`-0777`) Perl regex spanning both quote styles and multi-line
   imports; added both cases to the regression test (now 8 cases, all
   green). Hit and fixed a real bug while doing so — an unescaped `@` in
   the Perl regex triggers array-variable interpolation (`@playwright`
   read as an array, not a literal), silently emptying that part of the
   pattern; escaped to `\@`.
3. **Doesn't typecheck**: `base.extend({...})` without an explicit type
   parameter leaves Playwright's mapped `Fixtures<T, ...>` type
   uninferred, so `request`/`use`/`testInfo` were all implicit `any` (4
   `tsc --noEmit` errors) — no runtime/CI effect (Playwright transpiles
   via esbuild, no `tsc` step exists), but every IDE would red-underline
   the file. **Fixed**: `base.extend<{ resetPosOncePerFile: void }>(...)`
   — confirmed 0 errors via a standalone `tsc --noEmit --strict` pass
   afterward.

Also verified and accepted as genuinely out of scope: held sales are
DB-persisted state `Reset()` doesn't touch (`tender-panel-reachable.spec.ts`
demonstrably leaves 3+ behind), but no spec today asserts held-strip
emptiness/count, so nothing is exercised — a future card if that ever
bites. The review also flagged that `.github/workflows/ci.yml`'s `e2e`
job points at a *different*, smaller, legacy suite under `tests/e2e/`
(unrelated to this card) and read that as "the `e2e/` suite this card
touches may not run in CI at all" — checked independently after the
review and that reading was wrong: a separate `.github/workflows/e2e.yml`
runs `npx playwright test` from `e2e/` on every push/PR to `main`. No
action needed; noting here so the (incorrect) alarm doesn't get
re-raised.

## Verified beyond automated tests

- Full `npx playwright test --project=default` run **twice**,
  back-to-back, after the fixes: both times **238 passed, 1 failed**,
  identical failure both runs
  (`catalog-image-to-till.spec.ts:35`, a thumbnail-load timeout).
  Confirmed pre-existing and unrelated: reproduced the identical failure
  in isolation on `main` *before* this branch's changes existed. Filed
  separately as ut-docs#1362 rather than silently folding an unrelated
  fix into this diff.
- Both originally-namechecked specs (`split-tender-i18n-925.spec.ts`'s
  fa/RTL test, `shifts-tips-osk-1272.spec.ts`) pass in every run.
- All CI-blocking guards from `CLAUDE.md`'s "Before committing" list run
  green locally, including the two new ones and their regression tests.
- `go build ./...`, `go vet ./...`, `gofmt -l .` clean — diff touches
  zero `.go` files (confirmed via `git show --stat`).
- Reviewer's own empirical leak-vs-fix proof (see above) — the strongest
  evidence available that this actually closes the bug class, not just
  that the suite happens to stay green.

## Safe to merge

Yes. All three review findings fixed, guard + suite re-verified green
after the fixes, no blockers.

## Explicitly deferred

- Held-sales cleanup (`tender-panel-reachable.spec.ts` leaves 3+ behind
  with no drain) — real, but no current spec depends on a clean
  held-sales strip. Not fixed here; flag if it ever causes a failure.
- ut-docs#1362 — the pre-existing, environment-specific
  `catalog-image-to-till.spec.ts` failure, filed separately.
