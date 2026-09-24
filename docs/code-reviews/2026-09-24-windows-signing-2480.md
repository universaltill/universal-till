# Review — sign the Windows installer in release.yml (ut-docs#2480), 2026-09-24

**Change:** `windows-installer` job runs in environment `release-signing` with `id-token: write`; `azure/login` (OIDC, app `unitill-release-signer`) → token for `codesigning.azure.net` → jsign 7.5 (sha256-pinned) signs both `.exe` files inside the installer and then `Setup.exe` (Azure Artifact Signing, profile `unitillcodesign/unitillreleasespt`, RFC 3161 timestamp). Each file is verified before upload; the job fails closed.

**Reviewer:** independent subagent, different model (Fable 5.1), read-only.

## Verified by the reviewer
OIDC subject for an environment-bound job is `…:environment:release-signing` with the repo's immutable sub claim (matches ut-infra `release-signing/main.tf`); jsign flags (`TRUSTEDSIGNING`, bare endpoint gets `https://` prepended, `--alias account/profile`, `--tsmode RFC3161`, `--replace`) correct; jar hash matches; `az account get-access-token` works after `allow-no-subscriptions`; downstream `needs` only read `result`, reruns re-check the branch policy.

## Findings and triage
1. **High — verification checked text, not signatures.** `grep -qi timestamp` matched "Timestamp is not available"; exit status discarded because the Artifact Signing root isn't in Ubuntu's bundle. **Fixed:** Microsoft Identity Verification Root CA 2020 fetched and SHA-256-pinned (`5367f20c…`), combined with the system bundle, `osslsigncode verify -CAfile/-TSA-CAfile` must exit 0, publisher match, and `Timestamp Server Signature verification: ok`. **Tested locally** (osslsigncode 2.14) against a real signed exe: signed → OK; signature stripped → rejected; other signer → rejected; right file, wrong root → exit 1.
2. **Medium — ut-infra docs described a stricter environment than the live one.** Owner chose option 2 (main + v*, no reviewer; gate = main ruleset, ut-docs#2574). **Fixed** in ut-infra#45. No `pull_request_target` workflow exists in this repo.
3. **Medium — token in argv; tag-pinned actions in the token job.** **Fixed:** `--storepass env:SIGN_TOKEN`; `actions/checkout` and `azure/login` SHA-pinned in this job.
4. **Low — case-sensitive publisher grep.** **Fixed:** `grep -qiF`.
5. **Low — NSIS `WriteUninstaller` ships an unsigned `uninstall.exe`.** Deferred → follow-up card (with signing the portable zip).
6. **Info — comment said only azure/login gets `id-token`.** **Fixed** (job-scoped; that's why the actions are SHA-pinned).

## Not testable before a release
The live signing call only runs in a release. First release after merge: check the job log for `signed OK` ×3, download `unitill-pos-setup-<ver>.exe`, verify locally with `osslsigncode verify` against the pinned root. A brand-new role assignment can 403 for ~10–30 min after creation (created 18:3x UTC).
