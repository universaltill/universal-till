# Review: build-provenance attestations for till releases (ut-docs#5437, ADR-0181)

**What shipped:**
- `release.yml` attests every release asset with GitHub artifact attestations (SLSA Build L2, public-good Sigstore). It uses `actions/attest-build-provenance`, pinned by SHA to v4.2.2.
- A new `provenance` job needs `checksums`, and `publish-release` needs it, so an unattested release stays a draft. A post-publish `provenance-dmg` job attests the `.dmg` and the rewritten `checksums.txt`/`.sig` after `macos-dmg-attach`.
- Each attested file is bound to a digest output of the job that uploaded it (`checksums_sha256`, `sig_sha256`, `dmg_sha256`), so a file swapped between upload and attestation fails the job.
- Only these two jobs hold `attestations: write`. Neither is in the `release-signing` environment, so no job holds both the Ed25519 seed and an attestation token.
- `scripts/ci/verify-release-provenance.sh` runs `gh attestation verify` with `--repo`, `--signer-workflow`, `--source-digest` and `--deny-self-hosted-runners`. It checks that the attested ref is the tag or `main`, and retries only the positive verify (3 attempts). Two negative drills must fail: a tampered copy and a wrong signer workflow.
- `scripts/ci/release-provenance_test.sh` is new. It runs sandboxed and is wired into `ci.yml`.
- `RELEASING.md` and `README.md` are updated.

Provenance proves which commit and workflow built a file. It does not prove the commit is safe. ADR-0181 §3 and §7 say so, and the merge-control decisions (O1, O2) are open on ut-docs#5437.

Built by Sonnet. A separate Tester (Opus) ran. Round 1 was reviewed by Fable, as a security card; round 2 was reviewed by Opus 5.5, scoped to the fixes.

## Findings
- **Tester MAJOR-1/2 (fixed):** the tests passed against broken code. The flag checks also searched comments, and the stub ignored `--source-digest` and `--deny-self-hosted-runners`. Each test mode now weakens only the check it names, and each case asserts its own error message. Mutations confirmed each one is caught.
- **Tester minors (fixed):** the test now catches `continue-on-error`, `if: always()` and a removed digest comparison. The RELEASING.md file name is fixed. "Start one-click releases from `main`" is documented, and `prepare` is unchanged because it belongs to ADR-0180. RELEASING.md now says to re-run `macos-dmg-attach` if it fails after its uploads.
- **Review M1 (fixed, docs):** anyone with push access can create a `v*` tag on any commit; the ruleset blocks only moving tags. That tag's own `release.yml` skips `prepare`'s ancestry check, reaches `release-signing` and gets a valid attestation. The ADR threat table no longer overstates today's controls. RELEASING.md and ADR §6 add the verifier-side `git merge-base --is-ancestor <sha> origin/main` check. The real fix is filed as ut-docs#5609 (restrict `v*` tag creation). It depends on the owner, who chose no reviewer on `release-signing` on ut-docs#2574.
- **Review m1–m5 (fixed):** the private-fork text is consistent across documents. The O2 cost is stated: one approval prompt per job in the environment. `write-all` is caught. The SC2016 suppressions are line-level. Only the positive verify is retried.
- **Review nits (fixed):** first person removed, a self-test range fixed, `.sig` bound with `sig_sha256`, and wording tightened.
- **Round 2 (fixed):** the O2 wording claimed O2 blocks the tag path. It keeps the seed out of reach but not the attestation. The fork verify line now points at the full RELEASING.md check. An indent is fixed.
- **Round 2 (accepted nits):**
  - Stdout of failed retry attempts accumulates in one file. This could matter only if gh printed JSON on a failure, and it does not.
  - A non-numeric `VERIFY_ATTEMPTS` loops until the job timeout. It still fails closed, and the workflow never sets it.
  - The test does not pin `macos-dmg-attach`'s `sig_sha256` output. A run would still fail closed at runtime.
  - The ancestry check will need revisiting if ADR-0180's release branches are accepted.

## Verification
- **TDD:** the reviewer re-verified it in a worktree. With `release.yml` reverted to before the change, the test exits 1 ("release.yml has no provenance job"); restored, it passes. The Dev and the Tester ran mutation sets (listed above): every listed mutation now fails the test.
- **`jq` path:** `.[].verificationResult.signature.certificate.sourceRepositoryRef` and the gh flag names were checked against the source of gh v2.103.3 and sigstore-go v1.3.0 (file:line in the Tester notes).
- **Commands:** `release-provenance_test.sh`, every `release-*_test.sh` and `check-release-checksums_test.sh` pass. All 138 single-line `bash scripts/ci/*.sh` steps of `ci.yml`'s build job pass. `shellcheck scripts/ci/*.sh` reports 0 issues. actionlint shows only 4 old notices, none in new lines. `guard-ci-tests-sandboxed.sh` passes. No Go changed.
- **Not possible here:** a real release run. The first real release must confirm that:
  - `provenance` is green with every asset `ok` and both drill lines printed;
  - the release was published only after that;
  - a one-click run shows ref `refs/heads/main`;
  - `provenance-dmg` is green under `contents: read`;
  - a local `gh attestation verify` on an archive and on `checksums.txt` prints the ref.

Pre-flight: review record this file; ADR new (ut-docs ADR-0181) + README index + ADR-0178 amendment note (`guard-adr-index.sh` ok); reference updated (ut-docs `architecture/packaging.md`, `reference/independent-operator.md`); RELEASING.md + README updated; help topic n/a (no till user-visible behaviour); en.json n/a (no strings); tag-creation follow-up → ut-docs#5609; on-till attestation check, dependency provenance, nightly, SLSA L3 → listed as follow-ups in ADR-0181.

Verdict: approve, merge once CI is green.
