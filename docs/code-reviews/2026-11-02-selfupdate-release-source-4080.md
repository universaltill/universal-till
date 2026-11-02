# Code review — self-update release source as a build-time value (ut-docs#4080)

**Date:** 2026-11-02 · **Branch:** `feat/4080-releases-base-build-value` (+ ut-docs `docs/4080-releases-base-build-value`) · **Author:** AI agent (Sonnet) · **Reviewer:** independent AI reviewer, Opus 5.5 (different model)

## Change

- New build-time value `buildinfo.ReleasesBaseURL` (the releases API collection, without `/latest`), stamped with `-X` like `MarketplacePublicKey` (ADR-0158). Empty = `releasesource.DefaultBase`, our GitHub releases, so default builds behave exactly as before.
- New leaf package `internal/releasesource` (imports only `buildinfo`, never reads the environment):
  - `Parse`/`Build` derive `LatestURL()` and `TagURL(v)`.
  - `AssetTrusted` decides which asset URLs a release may list: `https://github.com/<owner>/<repo>/releases/download/` for an `api.github.com/repos/<owner>/<repo>/releases` base, or the same host and port for any other https base.
  - A malformed stamped value is `ErrInvalidBase`, never a fallback to our GitHub.
  - `CleanURL` is the shared URL hygiene, moved here from `updates/notes.go`.
- `internal/selfupdate`: `fetchRelease` uses the build source (`releaseSource` seam replaces `releasesLatest`). `ghRelease.requireTrusted` refuses any off-host archive, setup, dmg or `checksums.txt` URL before anything is downloaded, on the Linux, Windows and macOS paths. On macOS, the "no checksums.txt" refusal now also runs before the work dir is created.
- `internal/updates`: the default check URL comes from the build source. `UT_UPDATE_RELEASES_URL` still moves only the check and the release-notes host, never the download. An invalid stamp means no check unless the env override is set. Release notes trust the build's asset host (`githubDownloadPrefix` removed).
- README `UT_UPDATE_RELEASES_URL` block; ut-docs `reference/independent-operator.md` Updates section and checklist.

**No ADR.** This extends ADR-0158's build-time-value pattern to one more value. It only narrows trust (assets must now match the release host; before, any listed URL was fetched), and default builds are unchanged.

## Findings

| # | Sev | Finding | Resolution |
|---|---|---|---|
| F1 | medium | Trailing-dot FQDN bypass (tester F1). `Parse("https://github.com./acme/till/releases")` was accepted as a generic source with prefix `/`, so it trusted every GitHub repo's downloads, which is what the GitHub-host refusal exists to stop. `api.github.com.` also skipped the `/repos/O/R/releases` shape check. Build-time only (the operator chooses the value), so not remotely exploitable. | **Fixed, test first** (`TestParseTrailingDotHost`, red before the fix). `normHost` lower-cases the host and strips a trailing dot before every host comparison in `Parse` and `AssetTrusted`. `api.github.com.` now normalises to the GitHub branch. |
| F3 | low | The GitHub owner/repo prefix compared case-sensitively (tester F3). A base stamped `…/repos/Acme/Till/releases` would refuse every asset GitHub lists as `/acme/till/…`, which silently breaks self-update for that operator. | **Fixed, test first** (`TestAssetTrustedGitHubOwnerCaseInsensitive`). GitHub paths compare case-insensitively (GitHub's own rule); generic mirrors stay case-sensitive. |
| F2 | info | An empty query (`…/x?`, `url.URL.ForceQuery`) passed `CleanURL` (pre-existing in the notes check, moved verbatim). | **Fixed, test first** (`TestCleanURLRefusesEmptyQuery`): `CleanURL` refuses `ForceQuery`. |
| D1 | low | independent-operator.md said mobile builds "do not self-update", but the next bullet describes Android's APK self-update. | Fixed: the Go self-updater does not run on mobile; the APK update has its own source (known gap below). |
| D2 | low | The doc said assets must be "on the mirror's own host"; the code also checks the port. | Fixed: "own host and port". |
| D3 | low | README: "Empty = the public GitHub Releases API" is only true for an unstamped build. | Fixed: "Empty = the build's buildinfo.ReleasesBaseURL (the public GitHub Releases API in an unstamped build)". |
| G1 | info | `TestApplyMacAppRefusesUntrustedAssets` is darwin-only and no CI job executes `*_darwin_test.go` (pre-existing for all of them). The macOS call site was verified by inspection: both refusals run before `os.MkdirTemp` and any download. | Accepted; follow-up below. |
| R1 | info | The trust check applies to the listed URL, not to the target of GitHub's 302 to `objects.githubusercontent.com`. SHA-256 against `checksums.txt` remains the integrity gate. The checksums come from the same trusted host, so a compromised release host is not covered. | Accepted by design (package doc says so). Tracked by ut-docs#5272 (signed `checksums.txt`). |

Reviewer checks with no finding:
- Userinfo, port, `http`, dot segments (`/releases/download/../`, `%2e%2e`), query and fragment are all refused.
- An encoded `%2F` in the owner can't reach another repo.
- Fail-closed paths: an invalid stamp means no request at all, not a fallback.
- Env isolation: `releasesource` has no env access, pinned by a test.
- No leftover references to `releasesLatest`, `defaultReleasesURL` or `githubDownloadPrefix`, except history in an older review record.
- No file writes added (existing `MkdirTemp`/`MkdirAll` unchanged). No cwd-relative paths. No user-facing strings or templates. Core-neutral: the default is our own repo, not a vendor list. No secrets.

## Verification beyond unit tests (Tester, re-checked where cheap)

- **Mutation run:** 17 mutants of the six implementation files, all killed. Covered: trust always-true, default base, each platform's `requireTrusted` args, fallback on an invalid stamp, env read (incl. `syscall.Getenv`), GitHub-host refusal, http base, port, notes ignoring the stamp, and `rel.src`. macOS was not mutated (darwin-only).
- **Driven run:**
  - Setup: real till binaries built plain, with an acme stamp and with `ftp://x`, under `env -i` behind a recording TLS proxy.
  - Check target: the plain build polled our GitHub and the acme build polled `updates.acme.test`. The malformed build made no request and logged the error.
  - Self-updater harness, scenarios A–J: the stamp moves the check and the download, and refuses off-host assets with no asset request. A malformed stamp makes no request and never reaches api.github.com. `UT_UPDATE_RELEASES_URL` moves only the check.
- **TDD claims re-verified by the reviewer:** each new test was run red before its fix and green after.

## Gate (after the reviewer's fixes)

- `gofmt -l .`: empty.
- `go build ./...`: ok.
- `go vet` on releasesource/updates/selfupdate/buildinfo: clean. `GOOS=windows` and `GOOS=darwin` `go vet ./internal/selfupdate/`: clean.
- `go test -race` on releasesource/updates/selfupdate/buildinfo: ok. `go test -race -run 'Update|Release' ./internal/pages/...`: ok.
- `golangci-lint run ./...`: 0 issues.
- `guard-core-neutral.sh`, `guard-i18n.sh`, `guard-mail-addresses.sh`, `guard-data-access.sh`: green.

**Pre-existing failures seen by the Tester, none attributable to this change:**
- Six packages (catalogpull, catsync, cloudsync, db, pages, plugins) hit the default 10-minute timeout in a full `go test -race ./...` under CPU load. catalogpull passes alone with `-timeout 30m`, and pages passes in CI's `-timeout 20m` form.
- `internal/data TestStockUploadPendingAndHasSendable_FastWithRandomCloudIDsAndUnlinkedMarks` exceeds its perf budget under `-race` on origin/main too.

## Follow-ups (not in this change)

- ut-docs#5271: Android APK update source (`updateApkUrlBase`) as a Gradle property.
- ut-docs#5272: signed `checksums.txt`.
- ut-docs#5273: hard-coded `universaltill.com/download` links.
- ut-docs#5274: untranslated self-update error strings (raw `err.Error()` in `update_api.go`).
- New, not filed: run `go test ./internal/selfupdate/` on the macos-15 job so `*_darwin_test.go` executes (G1).

## Verdict

**Safe to merge** once committed with the fixes above. The change only narrows what a till will download, default builds are unchanged, and every refusal is fail-closed before any asset request.
