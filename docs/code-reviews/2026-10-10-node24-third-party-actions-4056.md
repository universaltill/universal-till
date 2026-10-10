# Code review — third-party actions to their node24 majors (ut-docs#4056)

## What shipped

- `android-actions/setup-android@v3` → `@v4` (v4.0.4, 2026-09-17) in
  `android-ci.yml` and `release.yml`. `packages: "platform-tools"` stays
  explicit; the comments now say v3's default (`tools platform-tools`) is
  what broke (ut-docs#2257) and that v4's default is `platform-tools`.
- `azure/login` v2.3.1 → `a641126d… # v3.1.0` (both signing jobs in
  `release.yml`, still SHA-pinned because they mint an OIDC token).
- `goreleaser/goreleaser-action` v6.4.0 → `f06c13b6… # v7.2.3`;
  `version: "v2.18.2"` stays exact (ut-docs#2610).
- Companion PR in ut-docs moves `dorny/paths-filter@v3` → `@v4`.
- After this, the only third-party actions in `.github/workflows` are
  these and `golangci/golangci-lint-action@v9`; all run on node24.

## Review (independent, Fable, fresh context)

Verdict: safe to merge, no blockers.

- Each new SHA resolves to the tag in its comment; every new
  `action.yml` is `using: node24`. No input these workflows pass was
  removed or changed meaning (`client-id`/`tenant-id`/
  `allow-no-subscriptions`; `version`/`args`; `packages`).
- **Should-fix, accepted (verification gap):** no CI run on this PR
  executes setup-android v4. `android-ci.yml` gates its SDK steps on a
  diff under `android/`/`mobile/`, and `release.yml` runs only on a tag or
  dispatch. v4 also bumps the default cmdline-tools from 16.0
  (`12266719`) to 22.0 (`15859902`). Evidence that JDK 17 (what both jobs
  set up) is fine: upstream's v4.0.4 README still sets up JDK 17, and no
  upstream issue reports a JDK failure since the default moved. The JDK
  requirement could not be confirmed directly (the proxy blocks
  `dl.google.com`). **Fallback if the next Android run fails in
  sdkmanager:** set `cmdline-tools-version: "14742923"` (20.0,
  upstream-tested on JDK 17) on both steps.
- Nit, no action: azure/login v3 masks `client-id` in logs by default.
- Nit, optional: goreleaser-action v7 verifies the downloaded archive's
  checksum and would also verify its cosign bundle if `cosign` were on
  PATH. Not in scope.

## Verified

- `actionlint` on `release.yml` and `android-ci.yml`: clean.
- `scripts/ci/windows-signing_test.sh` (incl. "goreleaser job actions are
  SHA-pinned"), `release-macos-nonblocking_test.sh`,
  `guard-pipefail-grep-q.sh`: pass.
- Open PR #1837 (release-job timeouts) edits nearby lines of
  `release.yml`, but no hunk overlaps.
