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

## Review findings
Pending Reviewer step.
