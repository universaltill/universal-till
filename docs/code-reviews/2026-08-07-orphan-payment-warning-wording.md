# Code review — orphan payment-method warning wording

- **Date:** 2026-08-07
- **Task:** ut-docs#170 (Orphan payment-method warning: remediation text
  points at a UI surface that doesn't exist)
- **Branch:** `fix/170-orphan-payment-warning-wording`
- **Author:** pipeline Dev step (Sonnet, inline)
- **Independent reviewer:** general-purpose subagent on **Opus** (different
  model, per standing practice — this card is `complexity:medium`)

## What shipped

The startup warning `warnPaymentMethodAnomalies` logs in
`internal/plugins/plugins.go` for a `payment_methods` row whose
`plugin_id` has no matching installed plugin (`FindOrphanedPaymentMethods`,
ut-docs#16) told the operator to "reinstall the plugin, or **reassign/rename
the tender**". No such rename/reassign UI exists anywhere in `web/ui/` —
confirmed by grep both at grooming and independently by the reviewer
(`web/ui/pages/locations.html`'s hit is a *location* rename, unrelated).
Stronger still: no data-layer write to `payment_methods.name` exists
outside plugin sync at all, so the instruction wasn't just un-surfaced, it
was unimplementable.

Separately, a **legitimately-uninstalled** payment plugin — an explicitly
supported state per ADR-0031, whose tender row is deliberately retained for
sales history — triggered this same alarming warning at every boot,
forever, with no way to tell "this is fine" from "this is stale".

Of the three design options the ticket's acceptance criteria offered
((a) add a rename UI, (b) soften the wording, (c) add a suppression
mechanism), **(b)** was chosen as the proportionate fix for a p3/medium
card: (a) is a UI feature well beyond this ticket's scope, and (c) needs a
new table plus an admin surface just to manage acknowledgements.

- Extracted the log-line construction into a pure, unit-testable function
  `orphanPaymentMethodWarning(o data.OrphanedPaymentMethod) string`.
- Dropped the non-actionable "reassign/rename the tender" instruction.
- Reworded to say plainly that a deliberate uninstall is expected (not an
  error) — "the tender is kept for sales history" — leaving "reinstall the
  plugin" as the one real remedy for a stale/accidental capture.
- `log.Printf(fmt-string, args...)` → `log.Print(orphanPaymentMethodWarning(o))`:
  the reviewer flagged this as a secondary, positive fix — the old call
  passed the operator/plugin-author-controlled `o.Name` through a `%q`
  verb inside a `Printf` format string, which could not itself be
  reinterpreted as a format directive, but routing a fully-rendered string
  through `Print` instead removes even that theoretical class of surprise.

## TDD evidence (independently re-verified, not just claimed)

Wrote `TestOrphanPaymentMethodWarning_DoesNotClaimNonexistentRenameAffordance`
first — confirmed it fails to compile against pre-fix code (the helper
function didn't exist yet), then implemented the helper and confirmed it
passes. The existing `TestManagerInit_ToleratesOrphanedPaymentMethod` is
unchanged and exercises the new message through the real `Init` →
`warnPaymentMethodAnomalies` code path end-to-end (not just the pure
function in isolation).

The independent reviewer re-verified this personally rather than trusting
the diff: backed up `plugins.go` (sha256-verified), edited
`orphanPaymentMethodWarning`'s body to the exact pre-change wording, ran
the test (failed with the expected "contains \"reassign\"" message), then
restored the file (byte-diff verified clean) and confirmed the new test
passes again on the real fix.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/plugins/... ./internal/data/...` and the full
  `go test ./...` — clean except one pre-existing, unrelated failure
  (`internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`,
  confirmed failing identically on unmodified `main` via `git stash` — a
  read-only-directory permission quirk of this sandbox running as root, not
  a regression here; same failure already documented in
  `2026-07-31-plugin-payment-method-followups.md`).
- `bash scripts/ci/guard-data-access.sh` and `bash scripts/ci/guard-i18n.sh`
  — both green. No SQL added (string construction only). The reviewer
  independently confirmed `FindOrphanedPaymentMethods` has exactly one
  non-test consumer and the result never reaches a handler, template, or
  JSON response, so this is a server log line only — no `T` i18n key and no
  `web/help/` manual topic is owed.
- `gofmt -l` clean on both changed files.
- No UI/template surface touched — no browser-driven check needed
  (backend-only: a startup log line and its test).

## Review findings

| # | Severity | Finding | Outcome |
|---|----------|---------|---------|
| 1 | nit | Docstring said "EnsurePaymentMethod is the only creation path" for the row this function describes — wrong for a *plugin-owned* row, which `SyncPluginPaymentMethods` materializes (`EnsurePaymentMethod` is the FK-satisfying path for non-plugin methods) | **Fixed** — docstring now says plugin-owned rows are materialized by `SyncPluginPaymentMethods` and nothing in the data layer or UI can rename one afterward |
| 2 | nit | Minor wording wrinkles: a dangling relative clause in the docstring, and "reinstall the plugin to restore it" — ambiguous "it" (the tender row was never removed) | **Fixed** — docstring clause reworded; message tail now reads "reinstall the plugin to make this tender usable again" |
| 3 | nit | The test's forbidden-word check (`strings.Contains(msg, "rename")`) is case-sensitive — wouldn't catch a future regression that capitalized the word | **Fixed** — check now runs against `strings.ToLower(msg)` |
| 4 | should-fix (follow-up, not this diff) | A sibling warning message 10 lines below (`FindSuppressedPaymentNameEntries`, "pick a distinct label or rename the conflicting tender") has the identical nonexistent-UI problem, deliberately out of this ticket's scope (which quoted only the orphan message) | **Carded** — ut-docs#382, so the next reader doesn't mistake "out of scope" for "considered and kept" |

Also confirmed clean: repository pattern (no SQL outside `internal/data`,
guard passes mechanically); no money-type surface; no i18n/locale surface;
scope is appropriately minimal for a p3/medium card (one extracted pure
function, one wording change, one test — no new UI, no schema, no ADR
needed since the change conforms to ADR-0031 rather than revisiting it).

## Verdict

**Safe to merge.** All three nits from the independent review are fixed
and re-verified in-branch; the one should-fix is carded as ut-docs#382
rather than scope-creeping this ticket further.
