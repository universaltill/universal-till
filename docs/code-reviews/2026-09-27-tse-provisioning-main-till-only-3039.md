# 2026-09-27 — TSE provisioning runs on the main till only (ut-docs#3039, #2969)

Two-repo change: this PR (till) and ut-cloud `fix/3039-tse-ready-main-till-only`
(cloud; its record points here). Docs: ut-docs `reference/manage-shop-catalog-api.md` §2.10.

## What shipped
ADR-0053 provisions one TSE per store, from the main till. Before this, a till
that follows a main till could take the payload-less `fiscal_tse_ready`
directive, spend the single-use credential handoff, keep the credential on its
own disk and flip shop-wide `fiscal.signing_device_configured.<cc>` (false for
the shop, reverted by the next admin pull); its retry tick could also POST a
kickoff for the store.

- **cloudsync:** `fiscal_tse_ready` joins `mainTillOnlyTypes` — a satellite's
  Tick skips it with no result post, so it stays pending for the main till.
- **`setup_tse.go`:** `errTSENotMainTill` + `tseRequireMainTill` (the
  `tillFollowsMain` test) gate `saveTSEProvisioningState`,
  `finishTSEProvisioning`, the retry tick and `retryTSEProvisioning`;
  `applyFiscalTSEReady` calls `requirePrimaryDirective` before touching the
  credential store or the network. The wizard hook fails closed (review nit 3).
- **Settings:** on a replica, retry/dismiss answer 409 with the existing
  `settings.error.change_on_main_till`, and the TSE block renders that hint
  instead of the two buttons (review finding 2). No new locale keys.
- `setup_tse.go` removed from `settingsWriteGuardFileAllowlist`; its three
  per-line `settings-write:allow` reasons now name the gate.
- Cloud: `fiscal_tse_ready` removed from `anyTillDirectiveTypes`, so a
  satellite is never served it; runbook/comment wording for a rotation
  re-arm corrected (review finding 1).

## Review
Independent reviewer: Fable 5.1 (built by Opus 5.5). Verdict: safe to merge, no blockers.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | ut-cloud `docs/fiscal-tse-provisioning.md` and `revoked_holder.go` said "a remaining till fetches" the re-armed key; now only the main till does. If the removed till was the main till, the handoff lapses after 24h unless a till is promoted. | **Fixed:** runbook + comment say the main till fetches it and what to do when the main till was removed. |
| 2 | should-fix | A replica's Settings still rendered Retry/Dismiss, which always 409. | **Fixed:** `tseFollowsMain` hides both and shows the hint; asserted in `TestTSEProvisioningSettingsActions_RefusedOnReplica`. |
| 3 | nit | The wizard hook logged a refused save and still POSTed the kickoff. Unreachable today (a till only gets `sync.primary_url` after a join), but a replica kickoff would re-mint and revoke the main till's key. | **Fixed:** returns on `errTSENotMainTill`. |
| 4 | nit | ADR-0053 says "till (setup wizard)", not literally "main till". | Accepted: consistent with the ADR (a wizard-running till is never a replica); the binding wording is in the catalog API reference. |
| 5 | nit | A pre-fix till in back-office display mode reports `role=backoffice`, which the cloud treats as main, so it can still take the directive. | Accepted: pre-existing, fail-open by design (§6 B1); a till with this change skips it by `sync.primary_url`. |

Checked and fine (reviewer): every `KeyTSEProvisioningState` write and every
`/fiscal/tse/` call lives in `setup_tse.go` and is gated; promotion/demotion
strands nothing beyond finding 1; `MainTillWaitingCount` now counts the
directive (correct); pending-cap exemption and the ready-signal dedupe are
unaffected; `reportsSatelliteOnly` is the right predicate under ADR-0116.

## TDD
Each new test failed on `main` for the real reason, re-verified by the
reviewer in a separate worktree:
- `TestApplyFiscalTSEReady_ReplicaWritesNeitherKey` (the card's AC),
  `…ReplicaWithLocalCredentialStillRefuses` — "succeeded, want a refusal";
- `TestTSEProvisionRetryTick_SkipsOnReplica` — "replica posted 1 kickoff(s)";
- `TestTSEProvisioningSettingsActions_RefusedOnReplica` — 200 instead of 409;
- `TestTickFiscalTSEReadyIsMainTillOnly` — "hook runs = 1, result posts = [applied]";
- cloud `TestSyncServesFiscalTSEReadyOnlyToMainTill` — "satellite till was served [fiscal_tse_ready]".

## Verified
- `gofmt`, `go build`, `go vet`, `go test ./... -race`, `golangci-lint` (0 issues), the `build` job's guards.
- `guard-docs-shots`: surface hash refreshed with `Docs-Shots-Unchanged: true` —
  a main till (what docs-shots captures) renders the same markup; only a replica's TSE block changes.
- ut-cloud `scripts/ci/verify.sh` green.
- **Not verified:** a real two-till German shop against a real fiskaly sandbox
  (no such rig in a cloud session); the replica's Settings block was checked by
  handler render assertions, not by a screenshot.

## Verdict
Safe to merge. Land the cloud PR and this one together (either order is safe:
each side alone already stops a satellite from taking the directive).
