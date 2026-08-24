# Code review: auto-provision a register at till-to-till join

**Date:** 2026-08-24
**Branch:** `feat/894-auto-provision-register-enroll`
**Closes:** universaltill/ut-docs#894 ("Auto-provision a register at till-to-till join (server-side, /api/sync/enroll)")
**Dev:** subagent, model override `fable` (complexity:hard)
**Reviewer:** independent subagent, model override `opus` (deliberately not `fable`), isolated worktree

## Scope

Follow-up from ut-docs#651 (Registers admin page), deliberately scoped
out of that card. A second till joining a shop over LAN previously did
not get its own register automatically — a manager had to create one
(Settings → Registers) and assign it (Settings → Tills → "This till's
register") after every join, or the till couldn't open a shift
(`pos.ErrRegisterIdentityAmbiguous`). This card closes that gap
server-side, in the primary's `/api/sync/enroll` handler, so the new
register is part of the DB snapshot the joining till downloads next —
not something the joining till invents locally.

## What shipped

- `internal/data.POSRepo.CreateRegisterForEnrolment(ctx, baseName)` —
  tries `baseName` (the till's own display name), retries with
  `"<baseName> (2)"`, `"(3)"`, … up to 50 attempts on a `registers.name`
  UNIQUE collision (via the existing `isUniqueViolation` helper) rather
  than failing the enrolment over a name clash. Insert-then-catch, not
  check-then-insert — race-free under SQLite's single-writer lock.
- `POST /api/sync/enroll` calls it right after `InsertTill` commits,
  extends the `till_enrolled` audit payload with `register_id`/
  `register_name`, and adds `register_id` to the JSON response.
- `completeJoin` forwards `register_id` into `db.ReplicaIdentity`.
- `db.ApplyReplicaIdentity`: when the primary sent a `RegisterID`,
  `sync.till_register_id` is pinned to it (overwriting the primary's own
  value that the snapshot carried) instead of being cleared — so the
  replica resolves straight to its own freshly-provisioned register
  instead of hitting `ErrRegisterIdentityAmbiguous` now that 2+ registers
  exist. Empty `RegisterID` (older primary) keeps the exact pre-#894
  behaviour (clear, re-resolve).
- `web/help/{en,ar,fa,tr}/multitill.md`: the "Registers" section's last
  bullet rewritten — joining now auto-assigns a register; the manual
  create/rename/reassign path is kept for shops wanting more registers
  than tills.

## Independent review

Spawned a `general-purpose` subagent, `model: opus`, isolated worktree
(branched from a WIP snapshot commit on the feature branch, since Dev's
subagent doesn't commit). Told explicitly to run the full gate, re-verify
TDD claims by revert-run-restore, and scrutinize the enrolment ordering
and the register-name collision path for races, given this touches a
money-adjacent path (a wrong register assignment misroutes a
Pfandrückgabe/cash-drawer payout).

**Should-fix, both addressed:**

- **F1 — the PRIMARY's own register identity went ambiguous the instant
  a second till joined.** A fresh shop finishes setup with exactly one
  register and no persisted `sync.till_register_id` (`setup_page.go`
  calls `EnsureRegister` only; the pairing QR lives on `/tills`, which
  never resolves it). Auto-provisioning gives the primary a *second*
  active register, so from that moment `pos.ResolveTillRegisterID` on
  the primary itself returns `ErrRegisterIdentityAmbiguous` — the exact
  failure this card removes for the joining till, transplanted onto the
  primary's own payout path (`shifts_api.go`). Reviewer reproduced this
  empirically (throwaway probe) before fixing. Fixed: the enroll handler
  now calls `pos.ResolveTillRegisterID` for the primary itself,
  best-effort, immediately *before* creating the second register — at
  that moment exactly one register exists, so resolution is unambiguous
  by construction and persists. An already-ambiguous shop (2+ registers,
  nothing picked pre-existing) is left alone, per
  `ErrRegisterIdentityAmbiguous` being explicitly tolerated — still a
  manager's call, not silently forced. Regression test:
  `TestSyncEnroll_PinsPrimaryOwnRegisterBeforeProvisioning`.
- **F2 — a provisioning failure 500'd the whole enrolment after the
  token was already burned.** By the time `CreateRegisterForEnrolment`
  runs, `InsertTill` has already committed and the one-time enrolment
  token is already consumed (`tokens.consume`, earlier in the same
  handler). A hard 500 here forced the manager to mint a fresh pairing
  code over what may be a transient error, and left an orphan till row
  behind. Fixed (by me, after the reviewer flagged it as a judgment call
  rather than fixing it themselves): fail OPEN — log the error, continue
  with an empty `register_id`. This degrades to exactly the pre-#894
  path: `completeJoin`/`ApplyReplicaIdentity` already treat `""` as
  "older primary, no register sent" and fall back to manual assignment,
  so no new code path was needed, only reframing the existing one as
  deliberate. TDD'd myself: `TestSyncEnroll_RegisterProvisioningFailureDoesNotFailEnrolment`
  seeds all 50 name candidates `CreateRegisterForEnrolment` would try,
  confirmed the test fails with a 500 against the un-fixed handler
  (`got 500: create register for enrolment: 50 name candidates for
  "Till 2" all taken`), then confirmed it passes (200, empty
  `register_id`, till still enrolled) after the fix.

**Nits / deferred follow-ups (not fixed, tracked):**

- `TestCreateRegisterForEnrolment_ExhaustedBoundErrors` still passes with
  the retry loop's bound entirely removed during the reviewer's probe —
  it only pins "a collision eventually errors," not the specific
  `maxAttempts` value. Harmless, weaker than it looks; left as-is.
- The shift-open picker (`web/ui/pages/shifts.html`, `shifts_page.go`)
  lists all registers with no preselection of this till's own
  `sync.till_register_id`. Pre-existing gap from ut-docs#268, but now
  every multi-till shop has 2+ options in the dropdown where before a
  fresh shop had 1. Filed as ut-docs#940 for its own card.
- `web/help/ar/multitill.md` renders "Settings → Tills" as "Settings →
  Registers" (pre-existing wording issue, not introduced by this diff —
  the new bullet preserves it rather than introducing it).
- The literal `"Till 2 (2)"` example embedded in RTL (ar/fa) prose may
  render with mirrored parens under the bidi algorithm — cosmetic.

**Verified correct (no changes needed):**

- **Ordering**: the register is committed (`ExecContext`, autocommit, no
  open tx) before the enroll HTTP response is even written; the snapshot
  is a *separate*, later `GET /api/sync/snapshot` request that queries
  the live DB (`VACUUM INTO`) per-request, not a cached artifact.
  `completeJoin` strictly sequences enroll → snapshot → stage. No window
  where the replica could download a snapshot missing its own register.
- **Collision retry has no TOCTOU** — it's insert-then-catch-UNIQUE, the
  race-free pattern, not check-then-insert; two simultaneous "till"
  enrolments correctly produce "till" and "till (2)".
- `ApplyReplicaIdentity`'s empty-string fallback is byte-identical to the
  pre-#894 `DELETE` — no ambiguity between "no register sent" and a real
  UUID.
- The auto-provisioned register is created `is_active = 1`,
  `location_id = NULL` — same shape `ListRegisters`/
  `ResolveTillRegisterID` already expect from a manually-created one.
- No new user-facing template/JS string outside `{{ T }}` (this is an
  API + protocol change; `register_id`/`register_name` are wire/audit
  fields, not UI copy) — `guard-i18n.sh` confirms.
- The doc's `"Till 2 (2)"` suffix example matches the code exactly
  (`fmt.Sprintf("%s (%d)", baseName, i)`, `i` from 2). ar/fa/tr read as
  fluent, structurally faithful translations of the English bullet (done
  directly by the Dev model since the project's NAS Ollama translation
  endpoint, 192.168.1.231, is unreachable from this sandbox — filed as
  ut-docs#941 for a native-speaker/NAS-pipeline re-verification pass,
  same category as the existing ut-docs#915 follow-up).
- No file-write path added at all in this diff (checked for the two
  recurring bug classes: missing `os.MkdirAll`, a cwd-relative path
  where `paths.Data(...)` belongs — both N/A).
- No real client/shop name; the only secret-shaped literal in tests is
  an obvious one-character fake bearer (`"b"`).

**TDD re-verification, done by the reviewer personally** (isolated
worktree, safe to mutate on-disk since nothing else shares it): reverted
`ApplyReplicaIdentity`'s new conditional to the old unconditional
`DELETE` → `TestApplyReplicaIdentitySetsProvisionedRegisterID` failed
(`expected sync.till_register_id set, got scan err: sql: no rows in
result set`); restored, all 5 `TestApplyReplicaIdentity*` pass. Reverted
`CreateRegisterForEnrolment`'s retry loop to a single naive
`CreateRegister` call → `TestCreateRegisterForEnrolment_CollidingNameSuffixed`
failed (`UNIQUE constraint failed: registers.name`); restored, all three
`TestCreateRegisterForEnrolment_*` pass. I independently re-did the same
class of check for my own F2 fix (see above) rather than trusting the
reviewer's report for it, since it was added after their pass.

## Full gate (final, post-fix)

`gofmt -l .` — clean. `go build ./...` — clean. `go test ./...` (full
module, every package) — all green, zero failures — run independently
three times across this review (Dev's subagent, the reviewer's worktree,
and by me in the main checkout after the F2 fix). All CI-blocking guards
from `ci.yml`'s `build` job relevant to this diff: `guard-data-access.sh`,
`guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`, `guard-i18n.sh`,
`guard-compliance-claims.sh`, `guard-help-topics.sh`,
`guard-docs-shots.sh` (re-run after the F2 edit shifted the surface hash
again — `make docs-shots` re-captured all 88 screenshots; only the
`multitill` topic's content actually changed, every PNG came back
pixel-identical to its prior commit except one unrelated 1-byte
`tr/users.png` re-encode from Playwright's own capture jitter, reverted
to keep this diff scoped to what the card actually changed) — all pass.

## Verdict

**Safe to merge.** Both should-fix findings were addressed with real
code fixes, each backed by a TDD revert-run-restore proving the
regression test is genuine (not a false-pass). Two follow-ups filed as
their own backlog items rather than scope-creeping this PR; two cosmetic
nits left as-is.
