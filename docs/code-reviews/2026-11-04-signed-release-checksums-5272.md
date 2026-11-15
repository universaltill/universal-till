# Review — signed release checksums for the self-updater (ut-docs#5272, ADR-0178)

- **Date:** 2026-11-04 · **Lane:** `lane:cloud-41` · **Card:** universaltill/ut-docs#5272
- **Built by:** Sonnet (Dev + two fix rounds) · **Reviewed by:** Fable (independent, security — high-risk tier) · **Tested by:** separate Tester agent
- **Design:** ut-docs `adr/0178-signed-release-checksums-for-the-till-self-updater.md`

## What shipped
- `internal/releasesig`: parse/sign/verify/append (`AppendPruned`) of `checksums.txt.sig` —
  `ed25519 <key-id> <base64>` lines, key id = first 16 hex of SHA-256(pubkey), strict anchored
  line regex, size and line caps.
- `internal/selfupdate/verified.go`: one `verifiedChecksums` used by the tar.gz, Windows setup and
  macOS dmg paths **before any download**; `checksumFor` deleted; `ErrSignature` → `failed:signature`
  (checked before the `checksum` substring case). No key stamped → today's behaviour + one Info log
  line; key stamped → fail closed. No env/setting override.
- Downgrade: "latest" must be newer than the running build; a pinned `/releases/tags/vX` answer must
  carry tag `vX` (review finding 1).
- `buildinfo.ReleasePublicKeys` stamped at every `MarketplacePublicKey` stamp site (goreleaser ×5,
  release/nightly/iOS workflows, Makefile, `build-app.sh`, gradle); `resolve-release-keys.sh`,
  `guard-release-key-stamped.sh` (+ sandboxed tests, wired into `ci.yml`).
- `release.yml`: sign in the `checksums` job (before publish), re-sign + prune in `macos-dmg-attach`,
  verify the published pair; seed only in the env of the sign steps (`release-signing` environment).
  Dormant while secret + variable are unset; half-configured → job fails, release stays draft.
- `scripts/releasesig` CLI (`keygen`, `sign [--keep-checksums]`, `verify`); `RELEASING.md` signing +
  rotation runbook; `docs/arch/desktop-app.md`, `README.md`; docs-shots surface hash refreshed
  (`web/help/img/manifest.json`, no screenshot change).

## Findings
| # | Sev | Where | Finding | Outcome |
|---|---|---|---|---|
| T1 | blocker (CI) | `update_follow.go` | docs-shots surface hash stale | fixed (hash regenerated) |
| T2 | medium (docs) | `RELEASING.md` rotation | runbook contradicted ADR §6 / `sign` refusal | fixed |
| T3 | low | `verified.go` | non-200 `checksums.txt` became `failed:install` | fixed, stays `failed:checksum` |
| T4 | low | `verified.go` | transport error on `.sig` labelled `failed:signature` | fixed: 404 → signature, 5xx → download, transport → network |
| T5 | low | `release.yml` dmg re-sign | `.sig` grew per re-run (8-line cap) | fixed (`AppendPruned`, ≤2 lines/key after 12 re-runs) |
| R1 | low (security) | `selfupdate.go` `fetchRelease` | pinned-version answer tag not checked → forged older release could downgrade a follower | fixed + `TestFetchReleaseTagMismatch` |
| R2 | low | `release.yml` dmg-attach | keys empty but `.sig` present → stale sig | fixed: job fails (both jobs) |
| R3 | low | `release.yml` | seed in env of a 60-line step | fixed: dedicated sign steps |
| R4 | low (docs) | ADR §10 | wording vs code | fixed; resolver warns on a one-key list |

## Verification beyond unit tests
- Tester: full gate (139 ci.yml build-job commands; all green after T1), 10 deliberate mutations
  (9 caught; the 10th still refuses the update by another route), CLI round trip (keygen/sign/verify,
  one-byte tamper rejected, unlisted key refused, dmg re-sign verifies old + new), install through
  the real `Apply()` with a signed fake release, tampered release refused with 0 artifact downloads.
- Reviewer re-verified (break → fail → restore → pass): `TestApplyVersionWhenIdleArmedRefusesUnsignedRelease`,
  `TestVerifiedChecksumsSigFetchClassification`, `TestAppendPrunedIdempotentOverReRuns`; orchestrator
  re-verified `TestFetchReleaseTagMismatch` the same way.
- No UI surface changed (no screenshots).

Pre-flight: help topic n/a (no shop-visible change; roster shows the existing `tills.update.failed` with the code) · `en.json` keys n/a (none added) · README updated · ADR index updated (ut-docs, `guard-adr-index.sh`) · reference pages updated (ut-docs `architecture/packaging.md`, `reference/independent-operator.md`) · follow-ups → Backlog cards + owner arming card (linked on ut-docs#5272).

## Not verified here
- The `.dmg` path test runs only on darwin (compile-checked; `desktop-darwin.yml` runs it).
- Workflow behaviour armed/dormant needs a real Actions release run — the owner's arming card checks it.

**Verdict:** approve.
