# Review: Windows signing rejects valid timestamps (ut-docs#3867)

Date: 2026-10-08 · Branch: `fix/3867-osslsigncode-timestamp` · Lane: `lane:cloud-54`
Author model: Opus 5.5 · Reviewer: Fable (independent subagent, one round)

## What shipped

Release v0.30.22 failed in `goreleaser`. `packaging/windows/sign-exe.sh`
rejected a correctly signed `unitill-desktop.exe` with "timestamp does not
verify". osslsigncode printed `Countersignatures: Timestamp time: …` with
no `Signing time:` line, then "Timestamp is not available".

**Root cause:** Ubuntu 24.04's apt osslsigncode is 2.8. In 2.8,
`print_cms_timestamp` fails when the RFC 3161 token's SignerInfo has no
PKCS#9 `signingTime` signed attribute. That attribute is optional, and
Microsoft's TSA sometimes leaves it out. The failure drops the timestamp.
Upstream fixed this in commit 71a046a (2024-03-08), first released in 2.9.
The card's guess (a missing TSA root in the CA bundle) was wrong: the chain
was never checked.

- `packaging/windows/install-osslsigncode.sh` (new): builds osslsigncode
  2.14 from upstream. It checks the full commit hash
  (`beec94e3…`) before building, and checks the installed binary is the
  first one on `PATH`.
- `release.yml`: the `goreleaser` and `windows-installer` jobs now run it
  in their own step **before** `azure/login`. That keeps upstream's build
  code away from the signing identity.
- `setup-signing.sh`: no longer installs osslsigncode. It fails if
  osslsigncode is missing.
- `sign-exe.sh`: fails closed on any osslsigncode older than 2.9.
- `ci.yml`: the signing regression step uses the same install script.
- `scripts/ci/windows-signing_test.sh`:
  - New wiring checks: the commit pin, the build-before-login order in
    both jobs, no apt osslsigncode anywhere, and no install from
    `setup-signing.sh`.
  - A local Python TSA that returns `openssl cms -noattr` tokens (no
    `signingTime`). The test asserts `Signing time: N/A`, so it can't pass
    by accident.
  - A version-floor case: a stub reporting 2.8 is refused.
- ut-docs `architecture/packaging.md`: documents the verifier and the step
  order.

## TDD evidence (reproduced personally)

- Real osslsigncode 2.8 build, spoofed past the floor: exactly one check
  fails, the new one. The error is the CI's verbatim: "timestamp does not
  verify", "Timestamp is not available".
- osslsigncode 2.14 (installed by the new script): 30/30 checks pass,
  including makensis part 3, with `UT_REQUIRE_SIGNING_TOOLS=1`.
- The new setup-signing wiring check is mutation-tested: an injected
  `bash …/install-osslsigncode.sh`, a bare `"…/install-osslsigncode.sh"`
  call and an `apt-get install osslsigncode` are each caught.

## Findings (Fable)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | libcurl comment/dependency wrong: with OpenSSL 3, upstream never links curl (`ldd` confirms). | Fixed: dependency dropped, comment corrected. |
| 2 | minor | The build ran after `azure/login`, next to the live signing identity. | Fixed: separate step before login in both jobs, ordering asserted by the test. |
| 3 | nit | `-noattr` drops every signed attribute, not only signingTime. | Fixed: comment says so. It reaches the same 2.8 code path. |
| 4 | nit | Install script didn't assert which osslsigncode ended up on PATH. | Fixed: it must be `/usr/local/bin/osslsigncode`. |
| 5 | nit | No retry around `git clone` / apt. | Accepted: the release job can be re-run, and the hash check makes a bad fetch fail loudly. |
| 6 | nit | Version parsing edge cases ("2.10", missing binary, banner change). | Checked: every case fails closed. No change. |

The reviewer confirmed 2.14's `verify_timestamp` is no weaker than 2.8's:
- the same `-TSA-CAfile` store and `CMS_verify` chain;
- the time comes from TSTInfo genTime;
- CRLs are still checked (`-ignore-crl` is off by default);
- the timestamping EKU and the message-imprint check are unchanged;
- the strings `sign-exe.sh` greps for are unchanged.

## Gates

- `shellcheck packaging/windows/*.sh scripts/ci/*.sh`: 0 issues.
- `guard-pipefail-grep-q.sh`: ok. The workflow YAML parses.
- All other `ci.yml` guards pass, except `guard-deadcode-baseline.sh`. That
  one fails identically on unmodified `main` here: the container's Go is
  1.26 and the repo needs 1.27. It's environmental, and CI runs it.
- No Go code changed.

## Not verifiable here

The real Artifact Signing round trip needs the release identity, and the
TSA host is blocked from this container. The live check is the v0.30.22
`release.yml` run after merge.

**Verdict: safe to merge.**
