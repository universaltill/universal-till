# Code review: sync-chip name + contrast, and the till-roster sync it needed (ut-docs#405)

**Date:** 2026-08-07
**Author (Dev):** scrum-master pipeline, Sonnet (complexity:medium)
**Reviewer:** independent Opus subagent (fresh context, different model)
**Card:** universaltill/ut-docs#405

## What shipped

Two field-reported defects in the multi-till LAN-sync "sync chip" (top-right
nav, `web/ui/partials/sync_chip.html`):

1. On the **primary** till, the chip showed a bare replica count ("1
   tills"), never the till's own configured name — there was nowhere to
   read one from before ut-docs#396 added `till.name`.
2. On a **replica** till, the chip was a plain, unclickable `<span>` — no
   way to reach the Tills page from it at all.

Plus a contrast bug: `.sync-chip.ok`/`.warn` hardcoded literal hex colors
instead of the `--success`/`--warning` tokens `:root` already defines, so
neither a theme nor general page contrast could reach the chip — reported
as "the text and background are some colours I cannot see the till name"
under the shop's active theme.

### The fix, and what it actually required

- `internal/pages/sync_admin.go`'s primary-mode chip branch now computes a
  `label` via the existing `tillNameOrDefault` helper (ut-docs#396)
  instead of only a bare count; the count stays as a secondary detail
  (`label · N tills`).
- `web/ui/partials/sync_chip.html`: the replica branch is now wrapped in
  `<a href="/tills">` — a **local** route, never the primary's origin, so
  this can't strand a kiosk operator the way a cross-origin link would
  (ut-docs#390).
- `web/public/app.css`: `.sync-chip.ok`/`.warn`/`.sync-banner` switched
  from hardcoded hex to `rgba(...)` + `var(--success)`/`var(--warning)`.
  This alone was **not enough** — see "Independent review" below.
- `internal/pages/sync_api.go`'s `GET /tills` handler: `PrimaryTillName`
  is now always computed. `till.name` isn't in `PerTillSettingPrefixes`,
  so it's already synced verbatim to every replica — the same read that
  gives the primary its own name gives a replica the primary's real name
  too, no new sync path needed. Also computes `ThisTillID` (from
  `sync.till_id`) so a replica's own row in the roster can be tagged.
- `web/ui/pages/tills.html`: the primary's row is now tagged "(this
  till)" or "(the primary)" depending on which role is viewing; each row
  in `.Tills` gets "(this till)" when its ID matches `ThisTillID`.
- **The part that turned this from a template/CSS fix into a real
  backend change:** giving a replica anything to show on `/tills` at all
  required `tills` to actually sync down. `internal/data/sync_admin_repo.go`
  adds it to `adminTables` (the same ~30s admin-bundle every replica
  already pulls) — this table was primary-only before, so `ListTills()`
  on a replica always returned nothing.
  - `bearer_hash` (`tills.bearer_hash`, `NOT NULL UNIQUE`) is that row's
    sync-auth secret and must never leave the primary. A new `redactCols`
    mechanism (stronger than the existing `skipCols`, used for
    `payment_methods.plugin_id`) force-**nulls** it on every apply —
    plain `skipCols` isn't enough here because a replica can already hold
    a *real* bearer_hash for every till via the join-time snapshot
    (ut-docs#368, a full-DB copy), so "just don't set a new one" would
    leave the real secret sitting there forever. Migration 030 relaxes
    the column to nullable so the resulting upsert doesn't violate
    `NOT NULL`; SQLite treats every NULL as distinct under the column's
    `UNIQUE` index, so this can't collide two real hashes on the primary.
  - Revoking a till and enrolling a new one are now **primary-only
    writes**, matching ADR-0011 §2's existing "replicas never write
    catalog/settings directly" rule: `POST /api/sync/tills/{id}/revoke`
    and `POST /api/sync/enroll(-token)` reject with 409 on a replica —
    without that, either write would appear to succeed locally and then
    silently revert on the next ~30s pull, with no error shown.
- Manual (`web/help/*/multitill.md`, all 4 locales) gained two new steps;
  `ut-docs/architecture/lan-sync.md` got an addendum documenting the
  `tills`-in-the-bundle change and the new `redactCols` mechanism,
  matching the existing "Shared plugin settings (2026-07-17)" precedent
  for scope changes to the admin bundle.

## Independent review — findings

**No BLOCKING findings survive in the merged diff.** The review did catch
two real, serious defects — both in an intermediate version of this
change, both already fixed by the time the review finished (a concurrent
edit landed mid-review; the reviewer explicitly flagged the race and
re-verified against the final tree):

1. **A snapshot-joined replica's pre-existing real `bearer_hash` survived
   `ApplyAdmin` forever.** The first draft used plain `skipCols` for
   `bearer_hash` — "leave the local value alone" — which does nothing for
   a value that was already real before this table's own sync ever ran
   once. Proved empirically (`"realhash-a"` still present after apply).
   Fixed by introducing `redactCols` (force-NULL on every apply, not just
   "don't set a new one").
2. **`tills.last_seen_at` in the bundle moved the whole-bundle fingerprint
   on every authenticated poll.** `TillByBearerHash` touches this column
   as a side effect of every single sync call, so including it in the
   dump defeated `?have=`'s unchanged-poll short-circuit for the **entire
   bundle**, not just this table — every replica would have paid a full
   catalog transfer + write transaction every ~30s, permanently. Proved
   empirically (fingerprint changed after a bare auth touch). Fixed by
   excluding it from the dump.

### Findings addressed after the review (this round, self-driven — no second review pass needed; none were blocker-class)

1. **Enrolment was left unguarded while revoke was guarded.** The revoke
   fix's own reasoning ("a replica's write silently reverts") applies
   identically to enrolling a new till on a replica — the new till would
   pair against the replica's own non-authoritative `tills` copy, then
   lose access ~30s later when the next pull prunes the row, with no
   explanation. Fixed: `POST /api/sync/enroll-token` and
   `POST /api/sync/enroll` now reject with 409 on a replica, and the
   "Add a second till" card no longer renders there (the "join an
   existing shop" / LAN-discovery cards are a different, unaffected flow
   — this device joining someone else, not someone joining this device).
2. **`last_seen_at` was `skipCols`'d, not redacted — stale forever on a
   snapshot-joined replica, not blank.** The exact same staleness
   argument that justified `redactCols` for `bearer_hash` applies here
   for a non-secret reason: a replica's copy of a sibling's
   `last_seen_at` starts real (from the join snapshot) and nothing but
   the primary ever updates it again, so it would sit frozen and be
   displayed as if live. Moved to `redactCols` too, so a replica now
   honestly shows "—" instead of an arbitrarily stale timestamp — still
   excluded from the dump either way, so this doesn't reintroduce the
   fingerprint-instability bug from finding 2 above. New regression test:
   `TestAdminApplyTills_RedactsPreExistingLastSeenAt`.
3. **Migration 030's comment described the superseded `skipCols` design.**
   Migrations are append-only after release, so a wrong comment there is
   permanent. Rewritten to describe `redactCols` accurately.
4. **The CSS fix's own rationale was wrong, which risked a future
   "cleanup" silently reintroducing the bug.** Verified directly against
   the shipped theme files: every theme (`web/public/themes/*.css`) sets
   its own `.nav a { color: … }`, loaded in `base.html` *after* app.css,
   at **equal specificity** to a bare `.sync-chip a` — so the theme's
   rule was winning regardless of `var(--success)`/`var(--warning)` being
   set on the parent `.sync-chip`; `color: inherit` was never actually
   reaching the link. What genuinely fixed legibility was the background
   becoming translucent (rgba, 12% alpha) instead of an opaque pill
   clashing with whatever a theme painted underneath. Fixed for real:
   `.sync-chip a` → `.nav .sync-chip a`, which outranks `.nav a` on
   specificity alone, independent of load order — confirmed by reading
   the actual selector/specificity math against every shipped theme file,
   not by assuming the original (incorrect) comment was right. Comment
   rewritten to explain the real mechanism.
5. **The manual overstated chip coverage.** Said the chip is "on every
   till, always shows this till's own name" — untrue for a standalone
   single-till shop, which still renders no chip at all
   (`len(list) == 0` early-return, unchanged by this card). Wording
   corrected in all 4 locales.
6. **`ut-docs/architecture/lan-sync.md` wasn't updated.** The repo's own
   precedent (the dated "Shared plugin settings (2026-07-17)" addendum)
   is exactly the format for documenting an admin-bundle scope change.
   Added an equivalent addendum for `tills` + `redactCols`.

### Accepted as intentional, not a defect

- **`TestTillsPage_NoFabricatedPrimaryRowWhenViewingFromAReplica` was
  renamed and its assertion inverted**, rather than left alone — the
  review flagged this as reversing a deliberate ut-docs#396 decision
  ("a replica showing its primary is a separate, out-of-scope concern").
  That scoping note is exactly what ut-docs#405 asks to close: `till.name`
  genuinely is the primary's own name, already synced verbatim to every
  replica (it isn't in `PerTillSettingPrefixes`), so showing it — tagged
  "(the primary)", not fabricated as "(this till)" — is the correct
  behavior for "give the chip a consistent meaning on both roles," not an
  accidental regression of the old test.

### Deferred — filed as a new Backlog card, not fixed here

- **ut-docs#426**: the join-time snapshot (`GET /api/sync/snapshot`, a raw
  `VACUUM INTO` copy, pre-existing and unrelated to this card) hands a
  freshly-joined replica every till's real `bearer_hash` *before* this
  card's `redactCols` ever gets a chance to scrub it on the first
  incremental pull. `redactCols` narrows the exposure window (scrubbed
  within ~30s of joining) but doesn't close it at the source. Not a
  regression this card introduced — a pre-existing gap the review
  surfaced while verifying this card's own bearer_hash handling. A hash
  isn't itself an auth bypass (SHA-256 of a 32-byte random bearer,
  hash-to-hash comparison), so this is real but not urgent; filed `p3`.

## Verified beyond automated tests

- Re-derived the CSS specificity/cascade math by hand against the actual
  shipped theme CSS (`grep -n "\.nav a" web/public/themes/*.css` +
  `base.html`'s `<link>` order) rather than trusting the diff's own
  comment — this is what caught finding 4.
- Confirmed the two review-driven regression tests
  (`TestAdminApplyTills_RedactsPreExistingBearerHash`,
  `TestAdminApplyTills_RedactsPreExistingLastSeenAt`) and the two new
  enrolment-guard tests
  (`TestEnrolTokenAndEnroll_RejectedOnAReplica`,
  `TestTillsPage_HidesAddTillCardOnAReplica`) actually pin the fixed
  behavior: reverted each fix locally, confirmed the corresponding test
  fails with the expected symptom, restored the fix, confirmed green
  again.
- `make docs-shots` re-run twice against the fully merged code (once
  after the initial chip/tills-page fix, again after the manual wording
  correction) — all 56 screenshots × 4 locales captured clean;
  `guard-docs-shots.sh` green both times.
- Live SQL reasoning on the redaction mechanism: `TillByBearerHash` uses
  `WHERE bearer_hash = ?` with a Go `string` parameter (never SQL NULL),
  and SQL `NULL = 'x'` evaluates to NULL (never TRUE) — a redacted
  NULL-hash row can never authenticate as any till, on either the primary
  or a replica.

## Gate — all green

`go build ./...`, `go vet ./...`, `go test ./...` (one pre-existing,
unrelated failure — `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`,
already tracked as ut-docs#415, reproduces identically on a clean `main`
checkout, root-caused to `go test` running as uid 0 in this environment).
All CI guards green: `guard-data-access.sh`, `guard-i18n.sh`,
`guard-help-topics.sh`, `guard-docs-shots.sh`.

## Verdict

**Safe to merge.** The independent review's two real findings were fixed
before the review even finished (a concurrent-edit race the reviewer
itself caught and re-verified against); its six non-blocking findings
were all addressed in this round except one deliberate, justified
exception and one legitimately out-of-scope pre-existing gap now tracked
as ut-docs#426.
