# Makefile stamps the wrong ldflags symbol (ut-docs#369)

## What shipped

`Makefile`'s `LDFLAGS` stamped `-X main.version=$(VERSION)` — a symbol that
doesn't exist anywhere in this codebase (the app actually reads
`internal/buildinfo.Version`). `go build` does not error on an `-X` target
that isn't a real symbol, so this was a silent no-op: every `make build`
binary reported `Version = "dev"`. `internal/updates.Newer` treats `"dev"`
as older than every release, so with auto-update on, a `make build` binary
(the documented developer/field-hotfix build path — README, quickstart
docs, `scripts/dev.sh` all use it) would silently replace itself with the
latest GitHub release minutes after being deployed. Observed exactly that
on two field devices while hotfixing ut-docs#344/#362.

Fix:

- `Makefile`: `LDFLAGS` now stamps
  `github.com/universaltill/universal-till/internal/buildinfo.Version`, the
  same symbol goreleaser's release build (`.github/workflows/release.yml`)
  already correctly stamps.
- `scripts/ci/guard-makefile-version.sh`: new regression guard, mirroring
  the existing `guard-*.sh` convention and `release.yml`'s own "every
  binary must report the real version, never dev" check. Builds via the
  real `make build VERSION=<distinctive>` and fails if that version isn't
  actually embedded in the resulting binary, or if it can't be
  distinguished from the bare `"dev"` fallback. Wired into `ci.yml`
  alongside the other guards, so this class of silent ldflags breakage is
  now CI-caught, not just release-time-caught.
- `internal/pages/update_api.go`: `autoUpdateTick` (the unattended
  scheduler) now declines to act when `buildinfo.Version == "dev"` — a
  developer/hotfix build, whatever the reason it ended up unstamped.
  Unattended self-replacement of a developer's own build is never the
  right default. The manual "Update now" button (`POST /api/update/apply`,
  a separate handler) is intentionally unaffected — an explicit user
  action stays available regardless.

## Decision recorded (acceptance criterion 3)

The card asked for an explicit decision on whether unattended auto-update
may replace a `dev` build. Judged as an ordinary engineering/safety default
(developer-build-replacement safety), not a business/legal call requiring
product-owner sign-off — same class of judgment the existing
`autoUpdateTick` guards already make (basket-in-progress, staleness
re-check). Decision: **no**, auto-update declines on a `dev` build; the
manual button is unaffected. Implemented and tested above.

## Verified beyond automated tests

- Confirmed the bug for real before fixing: built with the unfixed
  `Makefile` (`make build VERSION=9.9.9-broken-check`), inspected the
  binary with `strings -a`, confirmed the version string was genuinely
  absent.
- Confirmed the fix for real: same build with the fixed `Makefile`,
  confirmed the version string is now embedded.
- Mutation-tested `guard-makefile-version.sh` itself: ran it against the
  pre-fix `Makefile` (via `git show origin/main:Makefile`), confirmed it
  fails with the real error; restored the fix, confirmed it passes.
- TDD on the auto-update guard: `TestAutoUpdateTick_SkipsWhenBuildVersionIsDev`
  written first, confirmed it fails (`undefined: autoUpdateBuildVersion`)
  before the seam existed, passes after. The three pre-existing
  "auto-update fires" tests needed updating (the test binary's own
  `buildinfo.Version` is `"dev"`, so they'd otherwise now exercise the new
  guard instead of what each meant to test) — fixed at the shared
  `stubAutoUpdateSeams` helper so every test defaults to a real version,
  keeping the change in one place instead of six call sites.

## Explicitly deferred

None — the card's three acceptance criteria (fix the stamp, make it
detectable, record the auto-update decision) are all addressed.

## Safe to merge

Yes. Full gate green: `go build ./...`, `go vet ./...`, `gofmt`,
`go test ./... -race` (one pre-existing, unrelated failure in
`internal/issuereport`, same as ut-docs#159's record — confirmed via
`git stash` to reproduce identically on `main` HEAD), `guard-data-access.sh`,
`guard-i18n.sh`, `guard-docs-shots.sh` (regenerated all 44 manual
screenshots — `internal/pages/update_api.go` is part of the guard's
tracked app-surface hash even though this specific change has no template/
UI effect).
