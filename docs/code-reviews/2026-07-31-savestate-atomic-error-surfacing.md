# Code review — SaveState atomic write + error surfacing

- **Date:** 2026-07-31
- **Task:** ut-docs#157 ("SaveState settings write is non-atomic and swallows
  errors (real shop-facing path)") — deferred from ut-docs#12's own review.
- **Branch:** `fix/savestate-atomic-157`
- **Author:** pipeline Dev step
- **Independent reviewer:** general-purpose subagent on **Opus** (different
  model, per standing practice), run in an isolated worktree

## What shipped

1. **`internal/pages/common.SaveState`** (`state.go`) — was 9 sequential
   non-transactional `store.Set` calls with every error discarded
   (`_ = store.Set(...)`). Now builds one `map[string]string` and writes it
   via the existing `Store.SetMany` (added for ut-docs#12, already used by
   `SaveRuntimeConfig`) — one transaction, all-or-nothing. Signature changed
   from `func(...)` to `func(...) error`; the two conditional writes
   (`UIScale` only when `> 0`, `OSKMode` only when non-empty) were preserved.
2. **All 8 original call sites** updated to check the error:
   `init.go` (startup default-persist: logs via `log.Errorf`, does not block
   boot — offline-first, this only re-persists already-loaded defaults);
   6 HTTP handlers in `settings_page.go` (idle-lock, kiosk-idle-reset,
   ui-scale, osk, theme, save) now answer 500 and skip every live side-effect
   (`Engine.SetConfig`, `AuthSvc.SetIdleLockMinutes`, `httpx.Init*`) on
   failure; `setup_page.go`'s first-boot wizard renders a new localized
   error (`setup.error.save_failed`, all 4 locales) instead of continuing.
3. **Second round, from the independent review (below):**
   `Deps.SetState` commits a candidate `RuntimeState` to memory only *after*
   `SaveState` succeeds — all 7 `SaveState`-calling handlers now build off
   `CurrentState()`, persist, then commit, instead of committing first via
   `UpdateState` and persisting after. `settings.html`'s six affected forms
   only reload on `event.detail.successful`, otherwise reveal a shared
   `#settings-save-error` banner (new `settings.error.save_failed` key, all
   4 locales). `setup_page.go`'s two remaining swallowed writes in the same
   handler (`store.name`, `setup.completed`) now surface errors too.

## TDD evidence (independently re-verified, not just claimed)

- `TestSaveState_Atomic` (unit) and `TestSettingsSave_FailsClosedOnSaveError`
  (HTTP integration) both use a `BEFORE INSERT` SQLite trigger that aborts on
  `store.tax_rate` to force a mid-transaction failure.
- The reviewer **mutation-tested** independently, restoring the fix after
  each:
  - Reverted `state.go` to the old body verbatim → both tests failed with
    the expected symptom (`nil` error / `204` instead of `500`).
  - A transitional mutant that returns the first error but keeps the writes
    sequential (isolating atomicity from error-surfacing) → both tests
    failed on `Currency = "EUR" after a failed save, want seeded "GBP"` —
    confirms the atomicity assertion is load-bearing, not just checking
    the error return.
  - Dropped the `UIScale > 0` / `OSKMode != ""` guards → the two pre-existing
    conditional tests failed, confirming the map rewrite didn't silently
    always-write those fields.
- `TestSettingsSave_FailedSaveDoesNotLeakIntoLaterSave` (added after the
  review's finding #2) drives the real HTTP mux: seeds GBP/20%, forces the
  next save to fail, asserts the in-memory state is unchanged, then drives
  an unrelated successful save (`ui-scale`) and asserts the DB and live
  `pos.Engine` config still show the seeded values — proving the rejected
  change doesn't ride along on the next save.

## Verified beyond automated tests

- Full `go build ./...`, `go vet ./...`, `gofmt -l` on every changed file —
  clean.
- `go test ./...` — green except `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`,
  confirmed pre-existing and unrelated: it fails identically on `main` in
  this sandbox because tests run as root, which bypasses the read-only-
  directory permission check the test relies on.
- Both CI guards green: `guard-data-access.sh` (all new SQL-adjacent test
  triggers live in `_test.go`, which the guard excludes; no new SQL text
  outside `internal/data`/`internal/db`) and `guard-i18n.sh` (783 template
  keys resolve, all 4 locales match `en.json` exactly).
- Manually confirmed the HTMX fix is real, not cosmetic: the vendored
  `htmx.min.js` (1.9.12) does populate `event.detail.successful`; the six
  edited forms in `settings.html` were checked individually against their
  handler's new 500 path.

## Review findings

| # | Severity | Finding | Outcome |
|---|----------|---------|---------|
| 1 | should-fix (ticket's own intent) | Every SaveState-backed form used `hx-on::after-request="window.location.reload()"` unconditionally — HTMX fires `afterRequest` regardless of status, so the new 500 was invisible to the shop owner; the ticket's headline complaint ("...swallows errors") was only half-fixed | **Fixed** — reload now gated on `event.detail.successful`; shared error banner added |
| 2 | should-fix | `d.UpdateState(...)` committed the candidate state to memory *before* `SaveState` ran; on failure the rejected change stayed in memory and rode along on the next unrelated successful save, reaching the DB and the next boot that way — the exact harm class #157 describes, via a different route | **Fixed** — `Deps.SetState` commits only after a successful persist; new regression test |
| 3 | cheap, in-scope | `setup_page.go`'s `store.name` / `setup.completed` writes, two lines below the fix in the same handler, were still `_ = ...`-swallowed | **Fixed** — both now surface `"setup failed"` 500s, matching the handler's existing convention |
| 4 | nitpick | `TestSaveState_Atomic` used `TaxRatePct: 2000`/`700` — basis-point-scale numbers in a percent field | **Fixed** — corrected to `20`/`7` |
| 5 | pre-existing, out of scope | `init.go`'s boot-time persist passes a partial `RuntimeState` with no `IdleLockMinutes`/`KioskIdleResetSeconds`, and `SaveState` writes those keys unconditionally — first boot zeroes both, silently disabling idle auto-lock and the kiosk idle-reset from boot 2 onward. Confirmed present on `main` before this branch too. | **Not fixed here** — filed as a new Backlog card (see below); shop-facing and security-relevant, but a distinct bug from #157 and touching it would have widened this diff well past its own scope |
| 6 | pre-existing, out of scope | The currency-save handler (`/api/settings/save`) always writes `TaxInclusive`/`AllowNegativeInventory` from form fields that the shipped currency-only form never sends, so any currency change silently resets both to false. Confirmed present on `main` before this branch too. | **Not fixed here** — filed as a new Backlog card; more damaging than #157 itself but a separate defect in a different code path (form/handler field mismatch, not persistence atomicity) |
| — | nitpick, accepted | `hx-on::after-request` still surfaces the pre-existing hardcoded-English `"could not save"` string only in the log, not the UI — the visible banner text is fully localized (`settings.error.save_failed`); the plain-text 500 body itself follows this file's own pre-existing convention (`"manager or admin required"`, `"could not save override"` elsewhere), so no new i18n gap | Accepted, no change |

Also checked clean: no `os.MkdirAll`/`paths.Data` class issue (no file
writes here, all DB); no real shop/client name in test data; no
secret-shaped literals; `guard-data-access.sh` correctly excludes the new
`_test.go` trigger SQL.

## Verdict

**Safe to merge.** The core atomicity + error-propagation fix was correct
from the first commit; the independent review's two should-fix findings
(HTMX swallowing the 500, in-memory state leak) are exactly the kind of
"looks done, isn't actually shop-facing-safe yet" gap this review step
exists to catch, and are fixed in the second commit with their own
regression tests. Findings 5 and 6 are real, separate, pre-existing bugs
surfaced while reviewing this code — carded as new Backlog items rather
than scope-creeping this branch.
