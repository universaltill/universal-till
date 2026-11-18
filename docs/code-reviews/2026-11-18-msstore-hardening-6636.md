# Review record: Microsoft Store CI hardening (ut-docs#6636, items 1, 2, 5, 6)

Branch `fix/msstore-hardening-6636`. Epic: ut-docs#6380. Follow-up to
`2026-11-18-msstore-ci-6405-6407.md`. Design: the Architect's note on the card
(no ADR: each item makes an existing decision stricter and changes no
semantics; item 1 enforces ADR-0178 "After publish" step 3 word for word).

## What changed

### 1. `scripts/ci/release-upload.sh`: extend-only checksum files
- On a published release, `checksums.txt` and `checksums.txt.sig` are still
  re-uploaded with `--clobber` (marker kept), but only after `extend_only`:
  the published copy is downloaded (fresh temp dir, no `--clobber`; failure
  to read it fails closed) and the new file must start with exactly the
  published lines, byte for byte and in order (awk records, so a final line
  without a newline counts).
- `checksums.txt` only: every appended line must be `<sha256>  <file>` and
  must not name an asset the published file (or an earlier appended line)
  already lists.
- Draft releases are unchanged (clobber, never downloads).
- `.sig` decision: prefix rule only, no "new .sig verifies the published
  checksums.txt" check. ADR-0178 defines the post-publish `.sig` as every
  existing line kept plus one appended, so the prefix rule enforces the ADR;
  keeping the old signature line byte for byte keeps every till's already
  verified pair valid; `macos-dmg-attach` already runs `releasesig verify` on
  old and new before the upload. Re-run analysis: ed25519 is deterministic and
  `keep` is the already-extended file, so on a re-run `appendPruned` drops the
  original line and the prefix rule refuses it. That re-run already fails
  earlier, at `release-upload.sh "$TAG" "$dmg"` (the .dmg exists), so no new
  breakage.
- Tests: `release-upload_test.sh` (mock `gh release download`) cases a, a2, b,
  c, c2, d, d1, d2, e, e2, f, f2, g plus the existing exe/dmg/usage/unreadable
  cases.

### 2. One test flag, `UT_MSSTORE_TEST=1`
- `msstore-verify.sh` (SIGN_EXE, SOAK_CMD, SILENT_CMD, SIG_VERIFY_CMD,
  INSTALLER_NSI), `msstore-soak.sh` (MSSTORE_SOAK_ALLOW_HTTP, exit 2) and
  `msstore-submit.sh` (MSSTORE_ROOT, which relocates the opening-file switch)
  honour an override only when `UT_MSSTORE_TEST` is exactly `1`. An override
  that is set (even empty) without it fails closed and names the variable;
  any other flag value fails (verify, soak).
- `msstore-workflow_test.sh`: `msstore-submit.yml` never names the flag or any
  override (word-bounded grep). Existing tests export the flag.

### 5. `packaging/windows/sign-exe.sh`: exact leaf-signer CN
- The `grep -qiF` over `osslsigncode verify` output is replaced by
  `leaf_cn_is`: extract the signature (DER PKCS#7), require exactly one
  `signer_info:`, take its serial, find the single certificate with that
  serial in the signature, and require exactly one `CN=` whose value equals
  `SIGN_PUBLISHER` (exact, case-sensitive). Human-readable output is not
  parsed for the subject (a `/` in a name value spoofs the one-line form).
  Pinned-root and timestamp checks are unchanged. `openssl` is now a listed
  tool.
- Tests (`windows-signing_test.sh` part 2): leaves `evil` (CN suffix),
  `orgonly` (publisher only in O=), `slash` (`/` inside O= spelling a CN),
  an intermediate named like the publisher issuing leaf `Other`, and a
  different-case `SIGN_PUBLISHER` are all rejected.

### 6. Approval gate: doc only (accepted risk, below)

## Accepted risk: msstore-submit approval gate (item 6)
`prevent_self_review=false`, the owner is the only required reviewer and
`can_admins_bypass=false` (ut-infra `unitill-infra/msstore/main.tf:54-62`). The
gate is therefore custody of the owner's gh token, not a second person:
anything that holds that token can dispatch and approve `msstore-submit`.
A live write is additionally held by `store-release/microsoft.opened`
(ut-docs#6408), `dry_run` defaulting to true and `commit` defaulting to
false. Precedent: `bundle-publish-human` (ut-infra
`github-translations/README.md:11`). Owner options, not taken: a second
reviewer with `prevent_self_review=true`, or a bot token for pipeline
sessions. No ut-infra change. The runbook (ut-docs
`reference/microsoft-partner-center-runbook.md` section 10, job `submit`)
carries the same sentence.

## Deferred to epic ut-docs#6380
- Item 3: surviving mutants in the msstore-* tests (verify v1-v6, soak s1-s4,
  submit u1-u5).
- Item 4: ADR-0194 channel: Gradle `-PinstallChannel` fail-closed normaliser
  and the iOS `check-artifact-channel.sh appstore` step in
  `ios-testflight.yml` (needs a macOS run to prove).
- GitHub immutable releases on universal-till: breaks `macos-dmg-attach`'s
  post-publish .dmg + re-sign design (ADR-0178, ADR-0181) and the nightly
  prune; needs an owner decision and a superseding ADR.
- Re-running `macos-dmg-attach` after a partial attach (the .dmg is refused).

## Verification (Dev)
See the Dev notes on the card; commands and before/after results are recorded
there. Local run of `windows-signing_test.sh` part 2 used osslsigncode 2.14
built with `packaging/windows/install-osslsigncode.sh`; `makensis` is not
installed here, so the installer.nsi part skips locally (CI covers it).
Tester to apply by hand: remove the prefix awk, the duplicate-name check and
the download-failure branch in `release-upload.sh` (each must turn a case red);
add `UT_MSSTORE_TEST: "1"` to the verify step in `msstore-submit.yml` (the
workflow test must go red), then revert.

## Tester round
Gaps closed, each new case confirmed red against its mutant: truncated
`checksums.txt` (published `A\nB`, new `A`); a leaf with two CN RDNs; a forged
extra cert carrying the signer's serial and CN=publisher in the cert bag;
`UT_MSSTORE_TEST=yes` in verify with no override exported (only the "must be 1
or unset" check can refuse it); `UT_MSSTORE_TEST=yes` with `MSSTORE_ROOT` set
in submit (submit does not check the flag without an override, so no
flag-only case).
Defects fixed: D1, `release-upload.sh` printed two line numbers when the first
mismatch was before the last published line (a flag is set before `exit`,
checked in END; test pins one number). D2, `sign-exe.sh` `norm_serial` now
also takes the decimal serial that `openssl pkcs7 -print` writes below 2^128
(pure-bash long division, no `bc`), and the comment says 2^128; a genuine
80-bit-serial leaf is now accepted (test added).
Accepted gaps: an override exported as the empty string falls back to the
default (harmless); the "exactly one signer_info" check has no test because
osslsigncode cannot produce a two-signer PKCS#7 (it nests signatures).

## Review
(To be filled by the Reviewer.)
