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
Independent reviewer: Opus 5.5 (author: Sonnet). Scope: the universal-till
diff `origin/main...fix/msstore-hardening-6636` and the ut-docs diff
`origin/main...docs/6636-msstore-hardening` (two reference pages, both match
the code).

Pre-flight: `shellcheck scripts/ci/*.sh packaging/windows/sign-exe.sh` 0
issues; the six touched tests (`release-upload`, `msstore-verify`,
`msstore-soak`, `msstore-submit`, `msstore-workflow`, `windows-signing`) all
pass after the fixes below (windows-signing part 2 ran for real with
osslsigncode 2.14 / OpenSSL 3.0.13, the same OpenSSL as the ubuntu runners
that call sign-exe.sh; the installer.nsi part skips locally, no makensis, CI
step `UT_REQUIRE_SIGNING_TOOLS=1` covers it); guards `guard-store-private`,
`guard-pipefail-grep-q`, `guard-ci-tests-sandboxed`, `guard-mail-addresses`,
`guard-fixed-dates` pass. No Go changed, so no go build/test/lint rerun.

### Findings
1. **Major, fixed.** `release-upload.sh` duplicate-name check bypass: the
   appended-line regex `^[0-9a-f]{64}  [^ ]+$` let a CR or a tab through, so
   `<sha>  <published-name>\r` (CRLF) or `<sha>  <name>\tx` was not seen as
   the published name and a second hash for a published asset was accepted
   (both probed: rc 0, upload done). The till's `parseSums` uses
   `strings.Fields`, which drops the CR and reads the same name. The name is
   now `[^[:space:][:cntrl:]]+`. New cases (d3) CRLF and (d4) tab, red before
   the fix, green after (also under `LC_ALL=C`).
2. **Minor, fixed (hardening).** `sign-exe.sh` took the signer serial from
   the first line matching `/issuer_and_serial:/` and then `/serial:/`
   anywhere in `openssl pkcs7 -print`. The certificate bag (unauthenticated,
   attacker-addable) is printed before `signer_info:`, and a subject is
   printed on one line, so a bag cert whose subject contains that text could
   steer the lookup. Probed on OpenSSL 3.0.13: newlines are escaped
   (`\x0A`) and the following `$2` cannot be a valid serial, so it fails
   closed today, but it rested on print-format details. The awk is now
   anchored and scoped to after the (counted, unique) `^ *signer_info:` line;
   the PEM split matches `^-----BEGIN/END CERTIFICATE-----$` only. No test
   (no input reaches a different result on this OpenSSL); the real v0.36.2
   exe is still accepted with `TASK RUNNER TECHNOLOGY LTD` and refused with
   the substring.
3. Checked, no finding: trailing-newline handling (awk records and `read ||
   [ -n ]` count the same lines); CRLF in a published line is refused by the
   byte-for-byte prefix; an empty published file makes the `NR==FNR` idiom
   misfire, but K=0 means every file is a valid extension, so the result is
   the same; `.sig` prefix-only is consistent with `appendPruned`
   (Split/Join keeps old lines byte for byte); a second `extend_only` for the
   same name in one call fails closed (download into an occupied dir).
   Override gate: every test override in the three scripts is gated (all
   other `${X:-}` are production config); unset/empty/`yes` behave as
   recorded; verify `die` exits 1 and soak's exit 2 surfaces as verify's
   "soak check failed" (exit 1); `msstore-submit.yml` treats any non-zero as
   not ready and sets none of the variables. `dec2hex`/`norm_serial`
   compared with Python on 212 values up to 160 bits (0 mismatches;
   negatives and bare `0x` refused). Multi-valued RDNs print as one
   `    CN=a + O=b` line, which never equals the publisher (fail closed);
   `esc_ctrl` keeps CN on one line. `set -e` is off inside `leaf_cn_is`
   (called under `if !`) and every path returns explicitly. release.yml:
   the checksums job runs on a draft (unchanged path); macos-dmg-attach's
   first run passes (Tester's end-to-end), re-runs were already refused at
   the .dmg upload (deferred, epic #6380).

### TDD re-verified (reviewer, inline; each file restored and `cmp`-checked)
- Item 1: prefix check disabled (`bad=""`) gives 15 FAIL lines; restored.
- Item 1 (my fix): regex reverted gives (d3)/(d4) red; restored.
- Item 2: the verify per-variable `die` neutralised gives the five
  "without the flag" cases red; restored.
- Item 5: `grep -qiF` back gives evil, orgonly, slash, two-CN, forged,
  issuer, case red; restored.

Verdict: **APPROVE** (with findings 1 and 2 fixed in the working tree).
