# Review — `make build` stamps `0.1.0-dev`, not `0.1.0` (ut-docs#2700)

**Date:** 2026-10-08 · **Card:** universaltill/ut-docs#2700
**Author:** Sonnet (dev subagent) · **Reviewer:** Opus 5.5 (independent, fresh context)

## What shipped
- `Makefile`: `VERSION?=0.1.0-dev` (was `0.1.0`). The comment block now says why it is neither `dev` (keeps #369's stamping) nor a bare `0.1.0`.
- `internal/plugins/marketplace/dev_build_version_test.go` reads the Makefile's default and pins three things:
  - it is a `-dev` prerelease that `releaseVersion` rejects;
  - with that host version, `ListPlugins` sends no `host_version` and keeps a listing with `min_host_version` `99.0.0`;
  - `updates.Newer` still ranks `0.1.0` and `0.31.0` above it.

Why: since #2673 the till sends `releaseVersion(buildinfo.Version)` as `host_version` and also filters by it locally. A `make build` till reported `0.1.0`, so once any listing set `min_host_version` above 0.1.0, the plugin would silently vanish from a developer's store. The only trace was a `[DEBUG]` line.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | nit | The test failure message cited #369 the wrong way round: #369 was about a build ranking *below* releases. The Makefile comment also implied `-dev` protects against auto-update. | Fixed. The message now says "must never outrank a real release". The comment says that a field deploy still passes `VERSION=x.y.z`. |
| 2 | nit (optional) | The test reads a repo-root file from a package test. A shell guard would be another place for it. | Kept. It exercises the real `ListPlugins` path, and a reformatted `VERSION ?=` line fails loudly. |
| 3 | process | The review record was missing. | This file. |

The reviewer traced every consumer of `buildinfo.Version`:
- **No change:** selfupdate (`isDevBuild`, `ApplyVersion`), `updates.Newer` for all real releases, cloudsync (opaque string, change detection only), and the semver compare in ut-cloud (`v0.1.0-dev` is still below any `MinTillVersion`).
- **Changes, for the better:** the pages `releaseVersion` checks (link status, tills roster, update-follow, update-now) now treat a `make build` main till as a dev build. Before, it would have offered replicas an "update to 0.1.0", which is not a real release.
- **Unaffected:** `release.yml` and `nightly.yml` stamp explicit versions. e2e builds don't use the Makefile. `guard-makefile-version.sh` passes its own `VERSION`.

## Verified
- **TDD re-verified by the orchestrator:** with the Makefile default set back to `0.1.0`, all three `TestMakefileDefaultVersion_*` fail. Restored, they pass.
- `make build && ./bin/unitill-pos --version` prints `0.1.0-dev`.
- `bash scripts/ci/guard-makefile-version.sh` passes.
- `gofmt -l .` and `go vet` are clean, `go build ./...` passes, and `go test ./...` passes.

## Verdict
Safe to merge. This changes dev builds only: release and nightly binaries are stamped explicitly.
