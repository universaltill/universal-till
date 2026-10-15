# Review: docs-shots pins the disk probe roomy (ut-docs#4280)

**Date:** 2026-10-15 · **Lane:** `lane:cloud-54` · **Branch:** `fix/4280-docs-shots-disk-pin`
**Author model:** Sonnet (complexity:easy) · **Reviewer:** Opus 5.5, fresh context

## What shipped

- `internal/diskspace`: `UT_DOCS_SHOTS_DISK_ROOMY` (harness-only, read once like
  `internal/clock`'s `UT_DOCS_SHOTS_NOW`). When it parses as a true boolean,
  `Probe` returns a fixed `Usage{Free: 32 GiB, Total: 64 GiB}` without touching
  the filesystem. Empty, a typo or "yes" leaves the live probe.
- `e2e/playwright.docs.config.ts`: both docs tills get one shared
  `DOCS_SHOTS_ENV` (`UT_DOCS_SHOTS_NOW` + the new pin). `run-till*.sh` is
  untouched, so the real e2e suite keeps the live probe.
- Tests: `TestParseRoomy`, `TestDocsShotsRoomyIsNotLow`, `TestProbeWith`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `TestProbe_MissingDirectoryIsAnError` called `Probe`, so it failed when the pin was in the test process's env. | Fixed: it calls `probeWith(false, …)`. The package passes with and without `UT_DOCS_SHOTS_DISK_ROOMY=1`. |
| 2 | minor | Review record missing. | This file. |
| 3 | nit | `parseRoomy` comment said "exported-to-package" (copied from clock.go); `Probe` comment didn't say the pin also changes housekeeping. | Fixed both comments. |
| 4 | nit | The same four-line comment and env object were duplicated on both webServers. | Fixed: one `DOCS_SHOTS_ENV` const. |

Production safety, as checked by the reviewer: `diskspace.Probe` has two callers, the status-bar chip (`internal/pages/disk_space_chip.go`) and `runHousekeeping` (`internal/server/server.go`). Both get the pin, so a docs till skips the low-disk prune and caps backups against 32 GiB. Any skew keeps data rather than deleting it, and the till is throwaway. Nothing outside the docs config sets the variable. There are no file writes and no cwd-relative paths.

## Verified beyond unit tests

- **Real till, driven:** on this container the volume sat at 25.1 GB free of 258 GB, below the 25.8 GB floor (filled with a scratch file). The till binary was booted with `UT_AUTH=off`:
  - without the pin, `GET /ui/disk-space-chip` returned the `sb-disk-low` "disk is almost full" chip;
  - with `UT_DOCS_SHOTS_DISK_ROOMY=1`, it returned an empty body.
- **Acceptance:** `make docs-shots` on that same below-floor volume passed 120 tests. Every committed PNG and `manifest.json` came out byte-identical (`git status` clean under `web/help/img`).
- **TDD re-check:** with the pin branch disabled, `TestProbeWith` fails; with it restored, it passes. The reviewer separately inverted the branch, and the tests failed.
- **Gate:** `gofmt`, `go build ./...`, `go vet ./...`, `golangci-lint` (0 issues), the full `go test ./...`, and guards core-neutral, data-access, demo-env, e2e-no-browser, e2e-no-lan-discovery, i18n, netaccess, page-http-error, deadcode-baseline-growth and docs-shots. `playwright --list` loads the edited config (120 tests).

## Not done

- No visual surface changed, so no screenshots were taken beyond the docs-shots byte comparison.
- `guard-docs-shots-determinism.sh` (two full runs) wasn't run locally. Its workflow is path-scoped to `e2e/playwright.docs.config.ts` and runs on this PR.

**Verdict:** safe to merge.
