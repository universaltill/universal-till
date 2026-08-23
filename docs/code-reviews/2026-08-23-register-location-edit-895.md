# Code review: allow changing a register's assigned location after creation

- **Card**: universaltill/ut-docs#895
- **Branch**: `fix/895-register-location-edit`
- **Complexity**: easy
- **Reviewer model**: fresh-context Sonnet subagent (independent of the build), isolated `git worktree`

## What shipped

`POST /api/registers/{id}` (the existing rename endpoint) previously touched
only `name`; a register's `location_id` was fixed at creation with no edit
path, so a mis-assignment had no fix short of recreating the register
(follow-up from ut-docs#651's review, finding N2). This adds:

- `internal/data.POSRepo.SetRegisterLocation(ctx, id, locationID *string)` —
  a new repository method (`internal/data` only, per the repository-pattern
  rule), `UPDATE registers SET location_id = ? WHERE id = ?`, nil clears
  back to unassigned.
- `POST /api/registers/{id}` now also reads `location_id` from the same
  form and calls it, same empty-string-means-nil convention `CreateRegister`
  already uses.
- `web/ui/pages/registers.html`'s existing inline rename form gains a
  `<select name="location_id">` next to the name field, defaulting to the
  register's current assignment.
- `registerView`/`renderRegisters` (`internal/pages/registers_page.go`)
  gained `LocationOptions`/`LocationValue`: each row's picker offers every
  *active* stock location, plus — if the register's current location has
  since been deactivated — that location too (by id, resolved name), so
  editing never silently drops the row's existing assignment from the list.
- Tests: `TestSetRegisterLocation` (data layer: assign/move/clear/unknown-id)
  and `TestRegistersPage_ChangeLocationAfterCreation` (handler layer: move,
  clear back to "None", confirms name/id untouched).
- `web/help/en/multitill.md`'s Registers section updated to mention the new
  capability and that it doesn't touch past sales/shifts.

## Independent review — findings and resolution

An independent, fresh-context Sonnet subagent reviewed the diff in its own
isolated worktree (materializing the pre-review WIP commit rather than
sharing this checkout, per ut-docs#386) before this record was written. It
ran the build, the full test suite, every guard the diff touches, and did a
real revert-then-restore TDD check on the new handler test. Findings:

1. **Real, fixed: manual translation parity gap.** `web/help/en/multitill.md`
   was updated but `ar`/`fa`/`tr` were left describing only "rename it or
   deactivate it" — `guard-help-topics.sh` only checks topic *existence*
   per locale, not prose parity, so this wasn't CI-caught. Fixed: translated
   the same addition into all three, matching each locale's existing
   terminology for "stock location" / "sales" / "shifts" used elsewhere in
   the same file.
2. **Real, fixed: audit-trail mislabeling.** The endpoint now always updates
   both name and location together but still logged every call as
   `register_rename`, making a location-only edit indistinguishable from a
   rename in the audit trail — worth getting right given this product's
   GoBD/audit-trail emphasis. Fixed: relabeled to `register_update`.
3. **Accepted, not fixed: absent `location_id` treated as explicit-clear.**
   An absent form field and an explicitly-empty one both clear the location
   to NULL rather than "leave unchanged." Unreachable through the shipped
   UI (the form always sends the field); flagged only as an undocumented
   footgun for any future direct API caller. Deferred — fixing it would mean
   distinguishing "field absent" from "field present but empty" in a way
   this handler style doesn't currently do anywhere, for a path nothing
   can reach today.
4. **Accepted, not fixed: two sequential non-transactional UPDATEs.**
   `RenameRegister` then `SetRegisterLocation` aren't wrapped in one
   transaction. The reviewer confirmed this is unreachable through the
   legitimate UI (neither registers nor stock locations support hard
   delete, only soft-deactivate, so the location FK can't dangle from
   normal use) and is consistent with — arguably safer than — this file's
   existing `CreateRegister`+fire-and-forget-`audit` precedent. Deferred as
   a fast-follow rather than combined into one `UPDATE`, to avoid having to
   invent new error-message routing (distinguishing a name-uniqueness
   failure from a location-FK failure from one combined SQL error) for a
   path that can't be hit today.
5. **No findings** on SQL parameterization (confirmed `?` placeholders
   throughout, no injection risk), the empty-string-clears-to-NULL behavior
   (traced and confirmed correct), the deactivated-location dropdown wiring
   (confirmed a real per-row defensive copy, not a shared-slice aliasing
   bug), visible-label consistency with the pre-existing `<input>` in the
   same row (both rely on `aria-label` only), the two recurring bug classes
   this pipeline watches for (missing `os.MkdirAll`, cwd-relative path vs.
   `paths.Data(...)` — not applicable, no file I/O here), or README
   staleness (it doesn't describe register/location editing at this level
   of detail).

### TDD re-verification (independently reproduced)

The reviewer temporarily reverted just the `location_id`-read +
`SetRegisterLocation` call from the handler, leaving
`TestRegistersPage_ChangeLocationAfterCreation` untouched, and reran it:

```
registers_page_test.go:187: location_id = "loc_main", want loc_back
--- FAIL: TestRegistersPage_ChangeLocationAfterCreation (0.02s)
```

A genuine behavioral failure (not a compile error), confirming the test
actually exercises the shipped fix. Restored the handler; the test passed
again, and the worktree diffed identical to the pre-experiment state.

## Verification performed personally (not just trusting the diff/tests)

- `gofmt -l .` — clean.
- `go build ./...` — clean.
- `go test ./...` (full suite, all ~39 packages) — clean, 0 failures. (A
  separate `-race` run in this sandboxed environment hit the *default*
  10-minute `go test` timeout in `internal/plugins` alone — reproduced that
  this is environment slowdown under the race detector, not a regression:
  `go test ./internal/plugins/...` without `-race` passes in ~95s, and this
  diff never touches that package.)
- Every CI-blocking guard from `universal-till/CLAUDE.md`'s "Before
  committing" list run individually: `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-docs-shots.sh` (re-run after the
  locale-parity fix, since editing `ar`/`fa`/`tr` markdown re-triggers it —
  regenerated via `make docs-shots`, 84/84 Playwright screenshots pass),
  `guard-help-topics.sh`, `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `check-brand-assets.sh`,
  `guard-makefile-version.sh` — all pass.
- `web/help/img/**` diff beyond the `multitill` topic itself (`alerts`,
  `designer`, `invoices` PNGs across locales) is re-render drift from
  re-running the whole Playwright suite twice, not caused by this diff —
  same class of unrelated drift already documented in
  `docs/code-reviews/2026-08-23-registers-strand-warning-896.md`. No topic's
  `routes[0]` is `/registers`, so the registers page itself has no
  dedicated screenshot to change.
- No secret-shaped literal or real client/shop name introduced.

## Non-goals

- No transactional combination of the rename+location update (finding 4,
  deferred).
- No distinct handling of an absent vs. empty `location_id` (finding 3,
  deferred).
- No change to how a register's location factors into stock resolution or
  sync — this is purely the admin-page edit path, matching the card's
  acceptance criteria.
