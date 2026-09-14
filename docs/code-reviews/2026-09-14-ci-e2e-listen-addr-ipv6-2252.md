# 2026-09-14 — CI: e2e job's "Start app" bound an ambiguous host, unreachable over IPv4 loopback (ut-docs#2252)

## What shipped

Found while driving universaltill/universal-till#1164 through CI — the
`e2e` job in `.github/workflows/ci.yml` was red on `main` itself
(https://github.com/universaltill/universal-till/actions/runs/34802792344,
the immediately preceding merge) and reproduced identically on #1164,
including after one manual re-run, so it was not a one-off flake.

- **`.github/workflows/ci.yml`**: the `e2e` job's "Start app" step now
  sets `UT_LISTEN_ADDR=127.0.0.1:8080` instead of the ambiguous
  `UT_LISTEN_ADDR=:8080`. Root cause (`internal/server/server.go`'s
  `listenWithFallback`, left untouched — this is a CI-config-only fix):
  an unspecified/wildcard host is supposed to bind dual-stack, but on the
  current runner image (`ubuntu-24.04`, `20260907.300.1`) resolved
  IPv6-only (`port :8080 was busy — listening on [::]:8080 instead`), so
  the job's own `curl -sf http://127.0.0.1:8080/` health-check loop timed
  out before Playwright ever installed or ran a single spec. Two `main`
  runs the day before had passed the identical step in ~13s, so this is a
  recent runner-image behavior shift, not something either PR's own diff
  caused.
- **`tests/e2e/README.md`**: same one-line fix, so a contributor
  following the documented local-dev flow doesn't hit the same thing.
- Every other e2e server-start script in this repo
  (`e2e/run-till{,-auth,-ai,-layout,-diagnostics}.sh`) already binds an
  explicit `127.0.0.1:<port>` — `ci.yml`'s own inline step and its README
  were the only two places still using the ambiguous form.

## Independent review

Sonnet, fresh context.

**What it did:** confirmed the diff is exactly these two lines (no
application code touched); validated `ci.yml`'s YAML with
`python3 -c "import yaml; yaml.safe_load(...)"`; reproduced the fix
locally (seeded a throwaway e2e DB, started the app with
`UT_LISTEN_ADDR=127.0.0.1:8080`, confirmed `listening on 127.0.0.1:8080`
with no "was busy" fallback and an immediate, first-attempt `curl`
success); read the full `e2e` job and `tests/e2e/playwright.config.ts` to
confirm nothing in that job needs a non-loopback address (Playwright runs
in the same job/runner, `BASE_URL=http://127.0.0.1:8080`); grepped the
repo for every other `:8080` occurrence to rule out a missed third spot.

**Verdict: PASS, safe to merge.**

**Finding (low severity, not fixed, filed as a fast-follow):**
`Makefile`'s own `e2e:` target has the identical unfixed pattern
(`UT_LISTEN_ADDR=:8080` + a `127.0.0.1`-only health-check/`BASE_URL`).
Not in CI's path (`ci.yml` duplicates the "Start app" logic inline rather
than calling `make e2e`), so it doesn't block anything — but anyone
running `make e2e` locally on an IPv6-preferring host hits the same
flake, and it's now inconsistent with `ci.yml` and every
`e2e/run-till*.sh` script. Filed as `universaltill/ut-docs#2253` rather
than bundled into this fix, per this pipeline's own minimal-fix
discipline (this PR fixes what's actually failing CI, nothing more).

**Confirmed correct and untouched, checked adversarially:** every other
`:8080` occurrence in the repo (`Dockerfile`, `pos.env*`, root
`README.md`, `scripts/dev.sh`,
`specs/000-pos-core-mvp/quickstart.md`) is the *intentional* production
wildcard-bind default (`docs/code-reviews/
2026-08-27-no-wildcard-fallback-bind-1169.md`) and was correctly left
alone — this fix is scoped to the CI harness only, never to production
listen-address behavior.

## Verified beyond automated tests

- Reproduced the original failure (byte-for-byte identical symptom) on
  `main`'s own most recent run and on universal-till#1164's CI, including
  after one manual `rerun_failed_jobs` retry — ruled out "one-off flake"
  before writing this fix.
- Reproduced the fix locally: with `UT_LISTEN_ADDR=127.0.0.1:8080`, the
  app binds cleanly with no fallback message and the health-check curl
  succeeds on its very first attempt.
- YAML re-validated after the edit.

## Deferred

- `universaltill/ut-docs#2253` — same one-line fix for `Makefile`'s
  `e2e:` target (local-dev only, not urgent).

## Safe to merge

Yes.
