# Code review — macOS updater follow-ups: Intel UX + checksums.txt regression gate (board #18)

- **Date**: 2026-07-31
- **Branch**: `fix/macos-updater-intel-checksum-gate` → PR (this change)
- **Card**: ut-docs #18 — three follow-ups from the 2026-07-30 checksum fix's
  review (PR #95). Two were real code gaps (this PR); the third ("watch the
  first real release") was verified out-of-band, no code change (see below).

## What shipped

1. **Intel Macs no longer see "Update now"** — `internal/selfupdate.Supported()`
   returned `true` unconditionally for any macOS `.app` bundle install,
   regardless of CPU architecture. Only arm64 `.dmg` releases are ever
   published (`.goreleaser.yaml`'s darwin build id is `arm64`-only; the
   `macos-app` release job only ever builds/attaches an arm64 dmg), so an
   Intel Mac user would see the button, click it, and only then get
   `applyMacApp`'s "clean refusal" error. Fixed by moving the `goarch` test
   seam from `macapp_darwin.go` (darwin-only) to `selfupdate.go`
   (cross-platform) so `Supported()` can gate on it too, via a new pure
   helper `macAppBundleSupported(arch string) bool`. No template changes
   needed: `web/ui/layouts/base.html`'s status-bar chip already shows the
   "— Download" link whenever `canselfupdate` is false (fixed for the
   analogous Windows case in PR #121/commit c5b90c5), so widening
   `Supported()` to return false for Intel automatically gets the same
   treatment for free.
2. **`verify-versions` now asserts the dmg's checksums.txt entry** — the
   `macos-app` release job builds the `.dmg` and appends its SHA-256 to
   `checksums.txt` after the fact (goreleaser's own checksums.txt only
   covers goreleaser's own artifacts), and `applyMacApp` fails closed if
   that entry is missing/wrong. A silent break in the append step would
   only ever surface as every in-app mac update being refused, with no
   CI-visible signal. Added a step to `verify-versions` (runs on
   `macos-14`) that downloads `checksums.txt` alongside the release
   artifacts, computes the real SHA-256 of the downloaded `.dmg`, and fails
   the job if the `checksums.txt` entry is missing or doesn't match.
3. **Third bullet ("watch the first real release")** — verified directly
   against the real v0.2.50 GitHub release (the first built with this
   workflow): fetched its `checksums.txt` and compared the dmg's line
   (`147ea2c0…`) against GitHub's own reported asset digest for the dmg —
   they match. The append step has been working correctly since its first
   real run. No code change; this is what the new `verify-versions` step
   above now checks automatically on every future release.

## Independent (opus) review — real findings, addressed

The reviewer independently verified every factual claim in the card (the
arm64-only build config, the `macos-app` job's asset name, `base.html`'s
chip logic, `update-checksums.sh`'s output format) before reviewing, and
gave an overall **safe-to-merge** verdict. It found:

- **MINOR (real, user-facing, caused by this change) — fixed.** Widening
  `Supported() == false` to Intel macOS meant the Settings page's "Check for
  updates" fell through to `updateUnavailableHTML`, which only gave an
  actionable download link for `goos == "windows"` — everything else
  (including now Intel Mac) got the unix-kiosk dead-end message with no
  link, contradicting the status bar's own "— Download" link on the same
  machine. Fixed: `updateUnavailableHTML` now treats `darwin` the same as
  `windows` (both are windowed desktop OSes with a browser, unlike a
  fullscreen kiosk) — new regression test case added to
  `TestUpdateFallbackHTML`, TDD'd (confirmed red against the pre-fix
  branch, then green).
- **MINOR (real, but zero current UI impact) — deferred, not fixed.**
  `/api/update/apply`'s early-return-if-unsupported path returns
  `selfupdate.ErrUnsupported.Error()` verbatim ("...use the installer
  (Windows) or apt (.deb)" — wrong advice for Mac), making
  `applyMacApp`'s much better Intel-specific message unreachable via
  `Apply()`. Checked whether this is actually visible: the status-bar
  button's JS never renders the JSON `message` field (only a static
  "Update failed — see logs"), and the Settings-page inline apply button
  posts with `hx-swap="none"`, so HTMX never renders the response body
  either. Confirmed zero current UI impact — not fixed, to avoid
  unverifiable-by-UI work; logged as a candidate cleanup if this field is
  ever surfaced. This endpoint is also only reachable via a stale page or a
  direct POST now that the button itself is hidden for Intel Macs.
- **NIT — fixed.** `macapp_darwin.go`'s `goarch != "arm64"` check inside
  `applyMacApp` is legitimate defence-in-depth (`TestApplyMacAppRejectsIntelMac`
  drives it directly; it's the only guard if a future caller bypasses
  `Supported()`), but its comment read as if it were the primary gate.
  Updated to say `Supported()` now hides the button first.
- **NIT — fixed.** `selfupdate_test.go`'s doc comment on `supportedFor`
  still said "macOS is always eligible" without noting the arch gate lives
  in `Supported()`, not `supportedFor`. Updated to match how the existing
  writability-precondition split is already documented there.
- **NIT — fixed.** `macAppBundleSupported(goarch string)` shadowed the
  package-level `goarch` var; renamed the parameter to `arch`.
- **Observation (pre-existing, not this diff), not fixed.** `ci.yml` only
  runs Go tests on `ubuntu-latest`, so the two new darwin-tagged tests
  (`TestSupportedFalseOnIntelMacAppBundle`, `TestSupportedTrueOnArm64MacAppBundle`
  in `macapp_darwin_test.go`) never execute in CI, matching every other
  test in that file. The reviewer manually typechecked them via
  `GOOS=darwin GOARCH=arm64 go vet` (I did the same). Not this card's scope
  to fix CI's OS matrix.

## Verification

- `go build ./...`, `go vet ./...`, `gofmt -l` on every touched file — clean.
- `GOOS=darwin GOARCH=arm64 go vet ./internal/selfupdate/...` — clean
  (typechecks the darwin-only files that this sandbox can't execute).
- `go test ./internal/selfupdate/... ./internal/pages/... -v` and the full
  `go test ./...` — all green except
  `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`, confirmed
  **pre-existing and unrelated** by both myself and the independent
  reviewer, independently: this sandbox runs as uid 0, so the test's
  `chmod`-based read-only-dir probe doesn't actually block root (the same
  root-in-CI hazard `internal/selfupdate`'s own analogous test already
  documents skipping for this exact reason).
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh` —
  both green (i18n key count unchanged at 781 — no new user-facing strings,
  confirming the base.html chip needed no template change).
- **TDD, both fixes**: for `macAppBundleSupported`, confirmed
  `TestMacAppBundleSupported` fails with a real assertion error (not a
  compile error) against a mutated `return true`, then passes restored —
  done via manual file backup/restore, not `git checkout --` (this branch
  had other uncommitted work). For the Settings-page fix, confirmed
  `TestUpdateFallbackHTML` fails with the un-fixed `updateUnavailableHTML`
  (asserted the missing download link), then passes after the fix.
- No real client/shop name or secret-shaped literal in the diff; the only
  literal is the pre-existing `universaltill.com/download` URL.
- Checked the two recurring bug classes this pipeline's history flags: no
  file writes were added (pure functions + a moved test-seam var only), so
  neither the `os.MkdirAll` nor the `paths.Data(...)` class applies here.

## Safe to merge

Yes — both fixes are small, targeted, TDD'd, and independently re-verified.
The independent review's one real, currently-invisible finding (the
`/api/update/apply` message wording) is explicitly deferred with reasoning,
not silently dropped; every other finding was fixed and re-verified.
