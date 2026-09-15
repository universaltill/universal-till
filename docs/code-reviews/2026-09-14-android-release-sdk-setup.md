# Android release SDK setup review — 2026-09-14

## Scope

The `v0.14.21` release workflow reached the Android job but failed in
`android-actions/setup-android@v3` because the action's default package list
included the obsolete `tools` SDK package. The release remained a draft and
never reached publication.

## Change reviewed

`.github/workflows/release.yml` now passes an explicit package list to
`android-actions/setup-android@v3`:

```yaml
with:
  packages: platform-tools
```

The NDK, Android platform, and build-tools remain installed explicitly by the
following step at the pinned versions already used by the project.

## Security and release safety

- No secrets, signing configuration, or artifact verification was weakened.
- The signed APK step and the Android embedded-version verification remain
  mandatory.
- The workflow still publishes only through GitHub Actions.
- The uncommitted `ut-cloud` feature branch is not part of this change.
- The existing failed draft is not treated as a successful release; a new
  workflow run must pass all gates before publication.

## Verification

- `git diff --check` passes.
- `actionlint` parses the workflow; it reports two pre-existing shellcheck
  warnings in unrelated commands at lines 193 and 312.
- `go test ./...` is run because the release job executes the repository test
  suite before publishing.
- An independent review must confirm that the explicit package list removes
  the obsolete default package without changing the required SDK setup.

## Follow-up release verification

After this workflow fix reaches `main`, dispatch a patch release through
`release.yml`. Confirm all of the following before reporting success:

1. `android-app` completes setup, builds, signs, and uploads both APK assets.
2. `verify-versions` downloads the versioned APK and validates both embedded
   Go libraries.
3. `publish-release` succeeds.
4. The resulting `v0.14.22` release is public and includes the Android APK.
5. The `ut-cloud` working tree and feature branch remain untouched.
