# Code review: till has a persistent register identity; Pfandrückgabe resolves against it

**Date:** 2026-08-13
**Author (Dev):** scrum-master pipeline, Fable (complexity:hard)
**Reviewer:** independent Opus subagent (isolated worktree)
**Card:** universaltill/ut-docs#268

## What shipped

`CurrentOpenShift` resolved "whichever shift was opened most recently anywhere"
— defensible for the read-only Shifts page, but a real money-path risk for
`PfandRueckgabe` (the one write path relying on it): on a multi-register shop
with two concurrent open shifts, a payout could silently land against the
wrong register's drawer.

A till now has a persistent register identity (`sync.till_register_id`, the
same settings-key mechanism as `sync.till_id`/`sync.till_name`), resolved by
`pos.ResolveTillRegisterID`:

- 0 registers: `EnsureRegister`'s existing self-heal default, persisted.
- Exactly 1 register: not a guess, adopted and persisted (sticky across a
  second register appearing later — survives restart/upgrade).
- 2+ registers, nothing persisted: `ErrRegisterIdentityAmbiguous` — fail
  loudly on a write rather than silently guess.
- A persisted id that's since been deactivated falls through and re-resolves
  rather than writing against a retired register.

`PfandRueckgabe` now resolves via `pos.ResolveTillRegisterID`, then
`CurrentOpenShiftForRegister` (a register-scoped sibling of
`CurrentOpenShift`) instead of `CurrentOpenShift` — ambiguous identity
responds 409 pointing staff at Settings, never a silent guess.
`shifts_page.go`'s read-only display is unchanged, per the recorded product
decision ("defensible for reads"). Settings gains a "This till's register"
picker (Settings → Tills, renders on primary AND replica — unlike till-name,
every till processes its own local payouts) backed by a new manager-gated
`POST /api/settings/till-register` that rejects any id that isn't an active
register.

## Independent review — round 1 findings

**DO NOT MERGE AS-IS.** Two findings:

### Blocker (B1) — `till.register_id` was a shop-wide SYNCED key: the primary would clobber every replica's register identity

The first version of this feature named the settings key `till.register_id`.
`sync_admin_repo.go`'s `PerTillSettingPrefixes` (`sync.`, `printer.`,
`display.`, `reports.eod_`) is what keeps a per-till key from being dumped
off the primary and upserted onto every replica on each admin pull — a bare
`till.`-prefixed key is NOT covered (`till.name` is a deliberate, documented
exception that IS meant to sync shop-wide — ut-docs#396/#405 — a
register-identity key sharing that prefix by coincidence is not the same
case). The reviewer proved this empirically with a throwaway dump/apply test
against two real migrated DBs before reporting it: a replica's own register
choice was silently overwritten by the primary's on the very next admin
pull — **worse than the original bug**, because the resolver then persists
that wrong answer with high confidence and every subsequent payout on that
till keeps landing on the primary's drawer, invisibly.

**Fixed:** renamed the key to `sync.till_register_id` (covered by the
existing `sync.` prefix — no sync-layer change needed beyond the rename).
Added `internal/db/replica.go`'s `ApplyReplicaIdentity` clearing this key at
join time (the ONE path that inherits a whole settings row via snapshot
restore rather than an admin-bundle apply, so the prefix filter alone
doesn't cover it) so a joining replica re-resolves its own register instead
of starting life believing it's the primary's. Added
`TestAdminDumpApplyRoundTrip_TillRegisterIDNeverSyncs` (both directions:
primary→replica and replica→primary) and
`TestApplyReplicaIdentityClearsTillRegisterID` — both independently
confirmed to fail against the original `till.register_id` key/no-clear
behavior (the sync test failed with "till register identity leaked into the
admin dump" when temporarily pointed at the old key name; restored after
confirming), and pass now.

### Acceptance-criteria gap (B2), not fixed here — scoping decision recorded below

`EnsureRegister` is the *only* code path anywhere in the repo that ever
creates a `registers` row, and it only ever creates one (`reg-default`).
There is no UI, API, seed, or import that creates a second register, and the
till-to-till join flow doesn't provision one for a joining replica either.
So the two-register topology this card protects against is not yet
reachable through the product itself — today, every real shop still only
ever has one register, self-healed identically to before this change.

**Decision (Scrum Master, in place of a live product-owner call — cold cloud
cycle, no synchronous human to ask): ship the write-path-resolution fix now,
track register provisioning separately.** Reasoning: the fix is strictly
additive safety for single-register shops (byte-identical behavior via the
same self-heal path, confirmed by the full test suite) and correctly
*refuses* to guess the moment a second register exists by any means (hand-
seeded data, a future import, a future register-management page) — exactly
the fail-loud requirement the original decision asked for. Withholding a
working, tested safety fix until an unrelated, larger feature (register
creation/management UI) is also built would leave the reported bug's root
cause (an implicit "most recent" resolution on a money path) in place
longer for no benefit. Opened universaltill/ut-docs#651 to track "no way to
create/manage additional registers" as its own card — the natural next
step once (if) a real shop asks for a second register, and out of this
card's original scope by the BA/Architect steps' own framing.

## Independent review — round 1 non-blocking findings, triaged

- **N2 fixed** (cheap, same function as B1's neighbor): `CurrentOpenShiftForRegister`
  lacked `ORDER BY opened_at DESC` — `pos.OpenShift`'s duplicate-open guard is
  non-transactional and there's no unique index enforcing "one open shift per
  register" at the DB level, so two open shifts for one register isn't
  provably impossible. Added the same `ORDER BY` `CurrentOpenShift` already
  uses, and softened the doc comment to stop overclaiming a DB-level
  guarantee that doesn't exist.
- **N3 fixed** (free): `ResolveTillRegisterID` now reads the persisted value
  before listing registers, matching the reviewer's suggested ordering — the
  race window this closes needs a concurrent register-creation path to be
  reachable at all, which doesn't exist yet (see B2), but the fix costs
  nothing and removes a "resolver silently overwrites an explicit choice"
  shape on a money path.
- **N1, N4, N5, N6, N7, N8 — deferred, not fixed in this round.** All
  correctly categorized non-blocking by the reviewer: N1 (a clearer 404
  message when this till's own register has no open shift while another
  register's does) and N7 (a proactive status chip for the ambiguous state
  rather than discovery-by-failure) are UX polish; N4 (GET /settings' side
  effect) and N5 (a pre-existing, narrower TOCTOU already flagged as
  pre-existing by the reviewer) are accepted as-is; N6 (i18n gap on the
  self-healed "Default Register" literal — pre-existing in `EnsureRegister`,
  not introduced here) and N8 (till/register terminology collision in
  ar/fa/tr) are follow-up polish. Noted for a future Backlog card rather
  than expanding this round's diff — this round is scoped to the blocker per
  this pipeline's process-depth rule.
- **Translation provenance** — the reviewer independently verified the
  homelab Ollama NAS (`192.168.1.231:11434` and the in-cluster DNS
  alternative) is genuinely unreachable from this sandbox, so the ar/fa/tr
  strings were authored directly rather than through the documented
  self-hosted flow. This is a real, disclosed deviation from
  `reference/translation.md`'s stated rule, not an oversight — recorded here
  for visibility; the strings themselves were independently checked for
  sense and accuracy (not copies, not garbled) by the reviewer.

## Verified beyond automated tests

- **TDD, independently re-verified three times**: once by the implementing
  Fable subagent, once by the Opus reviewer (reverted the fix, reran both
  original regression tests, confirmed the exact claimed failure messages,
  restored, confirmed clean diff), and once more by the orchestrator for
  the round-2 fix specifically (temporarily reverted just the settings key
  to its pre-fix value, confirmed the new sync test fails with "till
  register identity leaked into the admin dump", restored, confirmed pass).
- **Sweep confirmed independently by the reviewer**: `CurrentOpenShift(` has
  exactly one remaining production caller (`shifts_page.go`, deliberately
  read-only/unchanged); `RecordCashAdjustment` takes an explicit `shift_id`
  and was never at risk.
- **No money-type, file-write, or path-construction issues** — confirmed by
  the reviewer; nothing in this diff handles amounts directly, writes files,
  or constructs paths.
- **Settings UI behavior traced end-to-end** by the reviewer: the ambiguous
  state never fails the whole `/settings` page render; the `<select>`'s
  disabled placeholder plus `required` plus the server's own independent
  400 rejection is belt-and-braces against an accidental empty submit.

## Gate — all green

`go build ./...`, `go vet ./...`, full `go test ./...`, `guard-data-access.sh`,
`guard-i18n.sh`, `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
`guard-help-topics.sh`, `guard-docs-shots.sh` — all clean, run independently
by the Dev subagent, the Reviewer subagent, and the orchestrator after the
round-2 fix.

## Verdict

**Safe to merge after the round-2 fix.** Single-repo, no ADR needed (extends
the already-established settings-key-per-till-identity pattern —
`sync.till_id`/`sync.till_name`/`marketplace.device_id` — not a new
architectural decision); the round-1 blocker was specifically a case where
that pattern's own guardrail (`PerTillSettingPrefixes`) needed the right key
name to apply, now fixed and independently pinned by tests. No money-type
impact. Manual (`web/help/{en,ar,fa,tr}/{display,reports}.md`) accurate
against what shipped, confirmed by the reviewer.
