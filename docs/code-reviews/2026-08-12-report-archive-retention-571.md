# Code review: report_archive 10-year retention, till-only (ADR-0040 card 1)

**Card:** universaltill/ut-docs#571
**Complexity:** medium (build model Sonnet, review model Opus, per the
scrum-master skill's model routing)
**Builder:** Sonnet subagent, fresh context (scrum-master session, first
lane; ran concurrently with a second lane building ut-docs#572 in `ut-cloud`)
**Reviewer:** Opus subagent, independent worktree, fresh context

## Change

Implements ADR-0040 Decision §§1-3, 7, 9 — till-side 10-year retention for
`report_archive`, independently buildable, no cloud dependency (cards 2-4
are separate future work):

- New append-only migration `036_report_archive_retention.sql`:
  `report_archive.cloud_acked_at` (nullable, unset by anything in this
  card — lands now so card 4 needs no second migration).
- `report_retention_mode` setting (`store.report_retention_mode`,
  `till|cloud|both`, default `till`); `cloud`/`both` are shown-but-disabled
  in the settings UI and rejected server-side (nothing implements the
  cloud gate yet).
- Age-based prune (10y cutoff) on the existing EOD scheduler's 30s tick,
  gated to run at most once/calendar-day; single `DELETE`, never
  read-then-delete. `cloud`/`both` modes are pure no-ops for pruning in
  this card, per the issue's own narrower scope than the ADR's full §2.
- Static capacity advisory (ADR §3 explicitly rejected a live free-space
  probe).
- `ResetTransactionHistory` no longer deletes `report_archive` (ADR §9 —
  it's a retained legal record).
- Bounded till-side date-range export (CSV/JSON) and a coverage display.
- Help topic + i18n keys in all four locales this product actually ships
  (`ar/en/fa/tr`) — there is no `de` locale in this product's UI yet, so
  the ADR/issue's "+ de locale" line is stale.

## Independent review

Opus, fresh-context subagent, isolated `git worktree` — ran every gate
itself and mutation-tested the higher-risk logic rather than trusting the
diff or the tests' names:

- `go build ./...`, `go test ./...` (full suite, per-package timings
  captured), `go vet ./...` — clean.
- `guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
  `guard-i18n.sh`, `guard-help-topics.sh` — all clean.
- Mutation-tested four separate pieces of logic by deliberately breaking
  each, confirming the relevant test failed with the right symptom, then
  reverting: the once-per-day prune gate, the strict `<` cutoff boundary,
  the `report_archive` reset-exclusion, and the `barcode_seed_test.go`/
  `dead_seed_test.go` migration-replay fix (reverting reproduces the exact
  `duplicate column name: cloud_acked_at` error the build subagent's
  report claimed).
- Confirmed the repository pattern (all new SQL in `internal/data`/
  `internal/db`), migration append-only (`013_report_archive.sql`
  byte-identical to `main`), no `money.Money` touched, no network call
  added to the checkout path, manager-gating on both new handlers, and
  `Content-Disposition` matching `data_api.go`'s existing export
  convention.
- `gofmt -l` findings (`internal/db/barcode_seed_test.go`,
  `dead_seed_test.go`) verified pre-existing on `main` (stashed and
  re-checked), not introduced by this change.

**Findings, triaged:**

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | **Blocker** | `guard-docs-shots.sh` failed: the settings screen gained a whole new "Report Retention" card and the `reports.md` topic changed in all four locales, but `make docs-shots` was never run — this is an explicit acceptance criterion of #571 and a real CI-red condition (`.github/workflows/ci.yml`'s screenshot-freshness guard). | **Fixed**: ran the real screenshot harness (`e2e`'s Playwright docs config, 68 screenshots across 17 topics × 4 locales) and regenerated `web/help/img/manifest.json`. Guard now passes. |
| 2 | Should-fix | New de/es external language packs (`ut-plugin-language-de`/`-es`, policed by `check-lang-pack-drift.sh` on push to `main`) will drift on the 17 new `settings.retention.*` keys. | **Deferred, tracked**: filed as universaltill/ut-docs#579 — those are separate repos with their own maintenance cadence, and the drift workflow is deliberately `push: [main]`-only (not PR-blocking) so a lagging pack doesn't block core work. Not silently dropped. |
| 3 | Should-fix | JSON export (`internal/pages/eod_api.go`'s `/api/reports/archive/export`) emitted PascalCase keys (`ID`, `Kind`, `Period`...) — violates this repo's snake_case API convention, and this is a new auditor-facing wire format. | **Fixed**: added `json:"..."` snake_case tags to `ArchivedReportRow`. |
| 4 | Should-fix | Empty JSON export downloaded the literal `null`, not `[]` — a nil slice through `json.NewEncoder`. | **Fixed**: `ArchivedReportsInRange` now returns an empty (not nil) slice. |
| 5 | Should-fix | The retention settings form (`settings.html`) silently swallowed server errors (`hx-on::after-request="window.location.reload()"` unconditionally) — every sibling save form on the same page shows `#settings-save-error` on failure. Concretely reachable if `store.report_retention_mode` is ever pushed as `cloud`/`both` via a cloud `set_setting` directive (no key allowlist there) and the server correctly 400s it. | **Fixed**: matched the sibling forms' `if(event.detail.successful){...}else{...error...}` pattern. |
| 6 | Should-fix | The automatic prune permanently destroys rows this very ADR calls a legal record, with no audit entry — `ResetTransactionHistory` writes one for a far less sensitive delete. | **Fixed**: `pruneReportArchive` now writes an `InsertAudit` entry (`report_archive_pruned`, rows deleted + cutoff) whenever it actually deletes rows; best-effort (logs on failure, doesn't block/retry the scheduler — the rows are already gone either way). |
| 7 | Should-fix | The manual's new "Report retention" section said reports are "kept for at least 10 years" without ever mentioning they are then automatically, permanently deleted with no confirmation. | **Fixed**: added an explicit sentence to all four locales' `reports.md` stating the automatic deletion and recommending exporting anything needed longer-term first. |
| 8 | Should-fix | ADR-0040's own Consequences section flags a specific interaction (`ResetTransactionHistory` no longer regenerating today's EOD report after a go-live reset, since `generateEOD` is idempotent per `(kind, period)`) as needing to be "checked at implementation time" — it wasn't checked or documented. | **Addressed**: this is a genuine, narrow edge case (same-day test EOD + go-live reset) that the ADR itself doesn't mandate a specific code fix for, and building one risks reintroducing the exact "reset can destroy a retained report" hazard §9 exists to prevent. Documented instead: a doc comment on `ResetTransactionHistory` explains the interaction precisely, and `settings.data.reset_confirm_dialog` (all four locales) now states archived reports are kept, so a manager sees the relevant fact at the point of action. |
| 9 | Should-fix | `docs/gobd-audit-log-assessment.md`'s Finding 3 still listed `report_archive` among the tables `ResetTransactionHistory` destroys — now false. | **Fixed**: table list corrected, with an inline note explaining the change narrows (doesn't resolve) the finding — every other table listed is still wiped unconditionally, so the finding's core gap stands. |
| 10 | Should-fix | No `docs/code-reviews/` record existed yet. | **Fixed**: this file. |
| N1 | Nit | Export range cap (3660 days) has no row-count bound; `ArchivedReportsInRange` materializes the whole matched range in memory. | **Accepted, not fixed** — the cap's own justification comment is honest about this; a real bound only matters if weekly/monthly report kinds land, which is explicitly out of scope here. |
| N2 | Nit | No test pins the 3660-day cap. | **Accepted, not fixed** — verified manually by the reviewer; low-value test to add for a single boundary constant. |
| N4 | Nit | Export error responses use plain-text `http.Error`, diverging from `data_api.go`'s `{data,error}` envelope on its near-twin export handler. | **Accepted, not fixed** — matches `eod_api.go`'s own established style (used 297 times elsewhere in `internal/pages`); reconciling the two siblings' conventions is a separate, broader cleanup, not this card's job. |
| N6 | Nit | `lastPruneDay` is in-memory only, so a same-day process restart re-runs the prune once. | **Accepted, not fixed** — harmless (the delete is idempotent), already documented in the code's own comment. |
| N7 | Nit | `reportRetentionCutoff` on Feb 29 normalizes to Mar 1, pruning one extra day. | **Accepted, not fixed** — negligible, one day out of a 10-year window. |

Verdict from the independent review, after the blocker and should-fix
items above were fixed and the full gate re-verified: **safe to merge**.

## Verified beyond automated tests

- Re-ran the actual manual screenshot harness end-to-end (real headless
  Chromium against a live till, not a mock) after every content edit,
  including a second pass after the help-topic wording changes (S6/S7
  above) invalidated the first manifest — confirmed `guard-docs-shots.sh`
  green on the final state, not an intermediate one.
- Re-ran the full local gate after all fixes: `go build ./...`, `go test
  ./...` (full suite), and all six guard scripts (`data-access`,
  `kiosk-engine`, `plugin-menu-read`, `i18n`, `help-topics`,
  `docs-shots`) — all green.

## Deferred (separate Backlog card)

The `de`/`es` external language-pack drift (finding #2) is filed as
universaltill/ut-docs#579, since the actual fix lives in two separate
repos (`ut-plugin-language-de`, `ut-plugin-language-es`) this session
does not have checked out.
