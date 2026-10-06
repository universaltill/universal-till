# Review: `customer.erased` plugin event (ut-docs#3435)

**Card:** universaltill/ut-docs#3435 — plugins (loyalty/CRM/integration) that
keep their own copy of a customer were never told when `POSRepo.EraseCustomer`
ran, so a GDPR erasure left their copy in place.

**Author:** Opus 5.5 (dev), plus a follow-up fix pass (Opus 5.5) after round 1.
**Reviewer:** Fable, two rounds (different model from the author throughout,
per `MODEL-ROUTING.md`'s `complexity:hard` row — round 2 triggered because
round 1's finding was compliance-adjacent: a false GDPR-erasure signal to
plugins).

## What shipped

- New event `customer.erased`, payload `{customer_id}` only (never name/
  contact data) — `internal/plugins/ipc.go`, mirrors the existing
  `stock.adjusted`/`sale.completed` seam (ADR-0121 §2). Registered
  `NonBlocking`. Gated by the existing uniform `events:receive` permission;
  no new permission.
- Two publish call sites:
  - Primary: `POST /api/data/customers/erase` (`internal/pages/data_api.go`),
    right after a successful `EraseCustomer`.
  - Replica: `syncPullTick` (`internal/pages/sync_admin.go`), for every
    customer id `SyncAdminRepo.ApplyAdminWithResult` reports as pruned
    during that pull.
- `SyncAdminRepo.ApplyAdmin` keeps its existing signature/behaviour for its
  ~100 other call sites (now delegates to the new `ApplyAdminWithResult`,
  which also returns `AdminApplyResult.ErasedCustomerIDs`).
- Help text: `web/help/{en,de,ar,fa,tr}/display.md` item 16 (Settings → Data
  → Erase a customer) explains plugin notification, delivery-only guarantee,
  and that a disabled plugin isn't notified (no replay).
- `docs/arch/plugin-integration-roadmap.md` gets a `customer.erased` row.
  `/home/user/ut-docs` `architecture/plugin-architecture.md` §6 documents the
  same event (separate PR, ut-docs repo, "part of #3435").
- No ADR: the architect decision (recorded on the issue, 2026-10-05) is that
  this is a plain instance of ADR-0121 §2's existing neutral-event seam, not
  a new capability/permission/host function.

## Round 1 finding — must-fix, now fixed

The replica-side prune-detection assumed "a customer missing from the
primary's bundle only happens via GDPR erasure." False: `POST
/api/settings/remove-demo-catalogue` also deletes `customers` rows (demo/
sample data, `is_sample_data=1`) through a different path than
`EraseCustomer`, and that synced-down sample data would get pruned on a
satellite the same way — falsely telling plugins "cust-001/002/003 were
GDPR-erased" every time a shop owner cleared sample data after onboarding
satellites. Proven with a reproducing test in round 1 (deleted), then fixed
for real with a permanent test.

**Fix:** `sampleCustomerIDs(ctx, tx)` snapshots `is_sample_data=1` ids inside
the same transaction, before `deleteMissing` runs for the `customers` table,
and those ids are excluded from `ErasedCustomerIDs`. Round 2 independently
verified: snapshot timing is correct for both the hard-delete and
retire-in-place/FK-blocked prune branches, runs on the same `*sql.Tx`
(no race), is scoped to `customers` only, and the new end-to-end test
(`TestSyncPullTick_SampleCustomerRemovalPublishesNoErasure`) genuinely
exercises `syncPullTick` + the real plugin bus, not a shortcut.

TDD re-verified independently in **both** rounds for every new test (production
change reverted → real assertion failure, not a compile error → restored →
green): `TestCustomerErasedEvent_ConnectorContract`,
`TestApplyAdminWithResult_ReportsErasedCustomers`,
`TestEraseCustomer_PublishesCustomerErasedToPlugins`,
`TestSyncPullTick_PublishesCustomerErasedForPrimaryErasure` (round 1);
`TestApplyAdminWithResult_SampleCustomerRemovalIsNotAnErasure`,
`TestSyncPullTick_SampleCustomerRemovalPublishesNoErasure` (round 2).

## Accepted gap (not fixed, documented here per round 2)

An owner who manually GDPR-erases a customer that happens to be flagged
`is_sample_data=1` (via the real erase screen, not "remove sample data")
gets an asymmetric result: the primary publishes `customer.erased` to its
own plugins (unconditional on any successful `EraseCustomer`), but a
replica's sync pull excludes that id as sample data, so replica plugins
don't hear about it. Accepted because: no customer create/edit path exists
in this codebase for a `customers` row to carry real PII while
`is_sample_data=1` — it is always one of the three fixed fictional demo
rows seeded by `demo_customers_promos.sql` — so no real data subject's
Article 17 right is at stake; and the alternative (the round-1 bug) is
strictly worse. A future fix would derive the replica-side signal from the
primary's own `customer_erased` audit row instead of inferring erasure from
bundle absence — out of scope for this card.

## Other nice-to-haves (filed as Backlog, not blocking)

- Doc-comment attachment bug where `sampleCustomerIDs` was inserted between
  `deleteMissing`'s doc comment and its declaration (Go attaches comments to
  the *next* declaration) — fixed directly in this review pass, trivial,
  zero runtime impact.
- Test coverage: the new sample-data-exclusion tests only exercise the
  hard-delete prune branch, not the retire-in-place/FK-blocked branch for a
  sample customer pinned by a replica-local sale. Round 2 confirmed by code
  reading that the exclusion is branch-independent (snapshot precedes both
  branches), so this is a confidence/coverage gap, not a known defect.
  Backlog card filed.
- Pre-existing translation drift (predates this card, from ut-docs#3253):
  DE/AR/FA/TR item 16 never got the EN-only sentence about parked orders and
  other-till sync added back then, so this card's new sentence builds on a
  premise the other four languages don't fully state. Backlog card filed.

## Verified beyond automated tests

- Swept every production write path for the `customers` table (not just
  what the diff touches) to confirm the invariant the fix now makes true:
  the only non-test code that creates rows is the sync upsert + the demo
  seed; the only code that deletes rows is `EraseCustomer`, the sync prune,
  and the demo-removal SQL — nothing else.
- Confirmed `ApplyAdmin`'s ~100 existing call sites are unaffected (full
  `internal/data` suite green; signature unchanged).
- Read all five `display.md` translations in full and confirmed they say
  the same three things in-register with their surrounding file, not just
  "a sentence exists."
- No real client/shop name or literal secret in the diff; no file writes
  (`os.MkdirAll`/`paths.*` rules don't apply).

## Gate (final, after the comment-placement fix)

- `gofmt -l internal/` — clean
- `go build ./...` — OK
- `go vet ./internal/data ./internal/pages ./internal/plugins` — OK
- `go test ./internal/data ./internal/pages ./internal/plugins -count=1` — all `ok`
- `bash scripts/ci/guard-data-access.sh` — pass
- `bash scripts/ci/guard-i18n.sh`, `guard-help-topics.sh`, `guard-help-drift.sh`,
  `guard-core-neutral.sh`, `guard-compliance-claims.sh`,
  `guard-competitor-naming.sh` — all pass (round 1; diff since then doesn't
  touch help/i18n/compliance surfaces)

## Verdict

**Safe to merge.**
