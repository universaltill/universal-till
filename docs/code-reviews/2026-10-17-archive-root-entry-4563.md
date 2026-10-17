# Review — plugin archive extractors accept a bare "./" root entry (ut-docs#4563)

- **Date:** 2026-10-17 · **Lane:** lane:cloud-54 · **Built by:** Opus 5.5 · **Reviewed by:** Fable 5.1 (independent security review)
- **Scope:**
  - New helper `archiveEntryTarget(destDir, name, isDir)` in `internal/plugins/importer.go`. Three extractors now use it: `extractMarketplaceTarGz` and the importer's `extractTarGz` and `extractZip`.
  - A **directory** entry whose name cleans to the extraction root (`./`, which `tar -C dir -czf x.tgz .` writes first) is skipped.
  - A non-directory entry naming the root is refused, as is anything that escapes it. The error text is unchanged.
  - Before this change, such a vendor bundle passed the scan and was signed, but no till could install it.

## Verified
- `gofmt`, `go build ./...`, `go vet ./...`, golangci-lint (0 issues). `go test ./...` passes. `internal/plugins` passes with the wider timeout CI uses for it (ci.yml): under the default 600s it times out on a loaded box, with or without this change.
- Guards from ci.yml's `build` job all pass locally except two that can't run in this container: `guard-deadcode-baseline.sh` (the deadcode binary is built with go1.26 but the module needs go1.27) and `guard-shellcheck-version.sh` (no shellcheck binary). CI runs both.
- **TDD:** `TestExtractMarketplaceTarGzAcceptsDotRootEntry`, `TestImportAcceptsTarWithDotRootEntry` and `TestImportAcceptsZipWithDotRootEntry` fail on the pre-fix code with "illegal file path in archive: ./" and pass after the fix. The reviewer re-verified this independently by reverting the fix. The `StillRefuses*`/`Refuses*` tests are regression guards, not TDD red: before the fix the leading `./` was itself refused with the same text.
- **Real driven check:** a bundle made with GNU `tar -C dir -czf x.tgz .` (`./`, `./assets/`, `./assets/a`, `./manifest.json`) is refused by `extractMarketplaceTarGz` on `main` and extracts on the branch.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Minor | `internal/selfupdate` has the same prefix check and would refuse a `./`-first tarball. It can't be hit today: goreleaser writes no `./` entry, and nightlies have no checksums so they are refused earlier | Out of scope (a different package and update path); filed as ut-docs#4565 |
| 2 | Minor | The zip path had no negative test for `./` then `./../x`, or for `.` as a file | Fixed: `TestImportRefusesZipRootEscapes` |
| 3 | Nit | The refusal tests also pass before the fix | Recorded accurately above |
| 4 | Nit | No review record yet | This file |

The reviewer's escape analysis covered absolute names, every `..` form, backslashes, the symlink/hardlink/char/fifo typeflags named `.`, legacy `TypeRegA`, and zip `IsDir()` from mode bits. An outside-dest sweep found nothing written outside the destination. The only loosening is the intended root-directory case. The verify-then-extract order (`VerifyArtifact` before extract, `VerifyManifest` after) is untouched.

**Not done:** the optional ut-cloud `bundlecheck` warning. Such bundles now install, so a warning would only add noise.

**Verdict:** safe to merge.
