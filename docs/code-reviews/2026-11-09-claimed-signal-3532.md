# Store claimed signal, till side (ut-docs#3532)

Design: ut-docs ADR-0060 amendment 2026-11 (`claimed` owner signal). Till scope only; the cloud emits the field in ut-cloud.

## Change
- `entitlement.Block.Claimed` (raw, lenient) and `entitlement.claimed` per-till key, written only for a JSON boolean; `StoreClaimed` reader; `Cached.Claimed` relay (`omitempty`).
- `enroll.applyRelayedEntitlement` writes the key only when relayed, before `last_confirmed_at`, under the existing newer-confirmation-wins rule.
- `pages.storeHasOwner` prefers the cloud signal, falling back to the plan/status inference when never told. Comments in `install_status.go` updated.
- `docs/data-model.md` documents the key.

## Checks
- Offline-first: wording only; not on the sale path (`sale_offline_test.go`, `salepath_imports_test.go` unchanged and green).
- No new user-facing strings (`repair`/`register`, `pair_again`/`register_new` reused); no migration; no SQL outside `internal/data`.
- Old cloud: no `claimed` key, no row, behaviour identical to before.

## Review findings (independent review, Opus 5.5; Sonnet wrote the code)
1. **Low, missing test (fixed).** The Architect asked for a test that
   `entitlement.claimed` stays per-till, out of the LAN admin bundle. Added
   `entitlement.KeyStoreClaimed` to `TestEntitlementSettingsArePerTill`
   (`internal/data/entitlement_per_till_2792_test.go`). It is covered today by
   the `entitlement.` prefix in `PerTillSettingPrefixes`. The test pins that
   against a later rename of the key.
2. **Low, stale value across a re-pair (accepted).** Nothing clears
   `entitlement.claimed`, by design. A till moved to a different store keeps
   the old store's value until that store's first sync answers. The plan and
   status keys behave the same way. The value only matters once the cloud
   rejects the credential, which needs a successful pairing first, so the
   window is very small.
3. **Info (accepted).** The Tester's driven test
   (`internal/pages/claimed_signal_driven_3532_test.go`) arms cloudsync's
   package-level start-up check-in state inside the pages test binary. No
   other pages test drives ticks. Kept: it is the only end-to-end proof from
   the real cloud bytes to the chip. Its dates carry `fixed-date:ok`.
4. **Checked, no finding.**
   - Relay ordering: `claimed` is written before `last_confirmed_at` and only
     past the newer-confirmation guard. A relay without it leaves the
     replica's row alone.
   - Values never rejects a block over `claimed`. JSON null reads as absent
     (`*bool`).
   - `storeHasOwner` callers are the chip, plugin install wording and cloudsync
     wiring. None is on the sale path, and there is no modal.
   - Help: `web/help/en/claim.md` already words both chips by "the store has
     no owner account", which is now what the till checks. No help or locale
     change is needed, and the chip strings are unchanged.

## Extra verification (Reviewer)
In a detached throwaway worktree, I re-ran Tester T6 (the replica writes
`claimed` even when it is absent). `TestRelayWithoutClaimedKeepsReplicaValue`
fails ("want the replica's own value kept") and passes once the code is
restored.

Gates:
- `gofmt -l .` clean; `go build ./...` ok; `golangci-lint run ./...` 0 issues.
- `go test ./internal/entitlement/ ./internal/enroll/ ./internal/pages/ ./internal/cloudsync/ ./internal/data/` ok.
- guard-fixed-dates, guard-data-access and guard-core-neutral pass.

## Verdict
Approve. Merge after the ut-docs ADR amendment and the ut-cloud PR.

## Deferred
None.

**Pre-flight:**
- help topic: n/a (wording unchanged, see 4)
- `reference/`: n/a. The contract lives in ut-cloud `docs/api-reference.md`
  (updated) and ADR-0060 (amended)
- `docs/data-model.md`: updated
- `en.json`/meta: n/a, no new key
- README: n/a
- review record: updated
