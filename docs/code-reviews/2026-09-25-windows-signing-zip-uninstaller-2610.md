# Review — sign the portable Windows zip and uninstall.exe (ut-docs#2610)

Date: 2026-09-25 · Branch: `fix/2610-sign-windows-zip-uninstaller` ·
Author: Opus 5.5 (pipeline, `lane:cloud-54`) · Reviewer: Fable (independent subagent)

## What shipped

Follow-up to #2480 (universal-till#1344 review, finding 5): every Windows `.exe`
in a release is now signed through one helper, `packaging/windows/sign-exe.sh`
(jsign → Azure Artifact Signing; then `osslsigncode verify` against the pinned
Microsoft root — signature, timestamp, signer name — fail closed).

- **Portable zip:** `.goreleaser.yaml` post-build hooks on the `windows` and
  `desktop-windows` builds sign the binaries *before* archiving (flag
  `UT_WINDOWS_SIGN=1`), so `checksums.txt` covers the signed files. Re-zipping
  later was rejected: `macos-app` read-modify-writes `checksums.txt` in
  parallel, so a second writer would race it.
- **Least privilege:** the `goreleaser` job joins the `release-signing`
  environment with job-scoped `id-token: write`, all actions SHA-pinned and
  goreleaser pinned to an exact version; the pre-release `go test` run moved to
  a new `release-tests` job so no test/dependency code executes where the
  signing token can be minted. A post-step verifies every `.exe` in the zip.
- **uninstall.exe:** `installer.nsi` `!uninstfinalize` runs the helper on the
  generated uninstaller (makensis aborts on failure); `windows-installer` now
  only verifies the zip's exes, then signs Setup.exe.
- `packaging/windows/setup-signing.sh`: one home for the jsign/MS-root pins and
  token fetch (token in a 0600 file, masked, read by jsign via `file:` — never
  on a command line or in `$GITHUB_ENV`).
- Card item 3: Windows tills never self-update from the zip
  (`internal/selfupdate.supportedFor` → false on windows); nothing to change.

## Findings (Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `RELEASING.md` / `docs/arch/desktop-app.md` still said Windows is unsigned | fixed |
| 2 | minor | new CI step used a bare `apt-get update` (flaky-mirror rule, ut-docs#1933) | fixed — `retry-with-backoff.sh` |
| 3 | minor | goreleaser resolved as `~> v2` inside the token-holding job | fixed — pinned `v2.18.2`; comment notes `az` stays logged in until job end |
| 4 | nit | `SIGN_TSA_URL` override undocumented | fixed — documented |
| 5 | nit | hook-line grep in the test is deliberately literal | accepted |
| 6 | obs. | a failed `release-tests` still lets the upload jobs wait out their timeout (pre-existing behaviour) | accepted |

No blocker/major. Reviewer verified jsign 7.5 reads `file:` storepass, the
action SHAs, the environment-scoped federated credential (ref-independent,
same as the working `windows-installer` job) and the job graph.

## Verified beyond automated tests

- `scripts/ci/windows-signing_test.sh` (new, in `ci.yml` build job): wiring
  checks + behaviour with a stub jsign signing via osslsigncode, a throwaway CA
  and osslsigncode's built-in TSA. Each guarded behaviour was mutated
  (timestamp check, publisher check, token on argv, `!uninstfinalize` removed,
  one goreleaser hook removed) and the test failed every time.
- Real `goreleaser build --snapshot --id windows` with the stub signer: the hook
  ran with the environment inherited and produced an exe that verifies; without
  `UT_WINDOWS_SIGN` the exe is unsigned (local snapshots unaffected).
- `makensis` 3.09 with the helper: uninstaller signed; signer failure aborts.
- Not verifiable here: real Artifact Signing output and `osslsigncode verify`
  on an installed `uninstall.exe` on Windows — the next release run is the
  proof (its verify steps fail closed).

`shellcheck` clean, YAML parses, `go build`/`go vet`/`gofmt` clean (no Go
changes). **Safe to merge.**
