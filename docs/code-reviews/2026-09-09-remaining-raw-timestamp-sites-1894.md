# Code review — remaining raw-timestamp display sites (ut-docs#1894)

**Date:** 2026-09-09
**Branch:** `fix/1894-remaining-raw-timestamp-sites`
**Card:** ut-docs#1894 — Remaining raw-timestamp display sites (settings backups, tills, sync-quarantine, orders, tips) — follow-up to #1632
**Reviewer:** independent fresh-context Sonnet subagent (`complexity:easy`, per the model-routing table), no sight of the implementer's reasoning
**Verdict:** PASS — safe to merge. No correctness bugs found in the six fixed sites.

## What shipped

Six sites still rendered a raw stored RFC3339/ISO timestamp with no locale
formatting, despite the mechanism (`{{ date }}`/`{{ datetime }}` template
funcs, `httpx.FormatDate`/`FormatDateTime`) already existing per #1130/#1632:

- `web/ui/pages/settings.html` — reset-archives list `.CreatedAt` and
  `.RetainedUntilDisplay` (`internal/pages/settings_page.go`'s
  `resetBatchView`, switched from a hardcoded `2006-01-02[ 15:04]` Go
  layout to `httpx.FormatDateTime`/`FormatDate` with the handler's
  already-resolved `locale`)
- `web/ui/pages/tills.html` — `.EnrolledAt`/`.LastSeenAt`
- `web/ui/partials/journal.html` — the same till `.LastSeenAt`, inside a
  `printf`-composed status line
- `web/ui/pages/sync_quarantine.html` — `.QuarantinedAt`
- `web/ui/partials/orders_list.html` — `.CreatedAt`/`.StatusUpdatedAt`
- `web/ui/partials/reports_tab_tips.html` — `.AllocatedAt`

Five of the six were RFC3339 strings passed straight to the template, so
`{{ datetime .Field }}` wraps them directly; the settings one pre-formats
in Go. One regression test per site (7 total — journal's till-status line
and the tills roster both cover `LastSeenAt`), each pinning
`time.Local = time.UTC`, seeding a fixed timestamp via SQL, requesting
`?lang=de-DE`, and asserting the raw string is gone and the de-DE-formatted
string is present — same pattern #1632 established.

## Independent review — findings and disposition

### Non-blocker 1 (filed as a follow-up, not fixed here): `fiscal_device.html` has a 7th raw-timestamp site this card's own audit missed
`web/ui/pages/fiscal_device.html:66` renders `.latest.IssuedAt`/`.CreatedAt`
raw and unwrapped — the same defect class, just not one of the 6 sites
#1894 named. Filed as ut-docs#1938 (same shape as #1936 below) rather than
widening this diff past its stated scope.

### Non-blocker 2 (no code change, correctly out of scope): the settings `.backups` file-list `Date` field is a separate, still-broken site
Already known before this review — found while implementing this card
(the old code comment on the reset-archives list miscited
`backup_api.go`'s `listBackupsForUI` as the *already-correct* reference,
which is how it surfaced) and filed as ut-docs#1936. Independently
re-confirmed: `listBackupsForUI` is genuinely still a hardcoded
`"2006-01-02 15:04"` layout, not already fixed. Correctly left out of this
PR.

### Nit 3 (process only, not a code defect): the reviewing agent's own test-selection regex had a typo
The regex handed to the reviewer for one targeted test run
(`RenderLocaleFormattedTillLastSeenAt`) didn't match the actual test name
(`Renders...`), so that one `-run` invocation silently matched zero tests.
Caught and corrected mid-review; the real test (verified to exist, PASS
when run by full name) is `TestJournalUI_RendersLocaleFormattedTillLastSeenAt`.
No code change — noted here so the miscue doesn't read as a coverage gap.

## What was verified beyond automated tests

- `go build ./...`, `gofmt -l .`, `go vet ./...` all clean.
- The 6 new regression tests run individually by exact name: all PASS.
- Full `go test ./internal/pages/...` package suite: green.
- `bash scripts/ci/guard-i18n.sh` / `guard-data-access.sh` /
  `guard-docs-shots.sh` (regenerated after the `web/ui/**` template edits,
  28 topics × 4 locales, all fresh): all clean.
- **Mutation-checked two of the six sites independently** (not just read
  the diff): reverted the `tills.html` template fix and the
  `settings_page.go` Go-side fix in turn, confirmed each site's own new
  test then failed with a real assertion error (not a build error),
  restored both, confirmed green again.
- Confirmed the CSV export path (`reports_page.go:879`) intentionally
  still writes the raw `AllocatedAt` string — correct as-is, a CSV export
  should stay machine-parseable, not locale-formatted.
- Scope check: every non-screenshot file touched is exactly the 5
  templates + `settings_page.go` + 6 test files + `manifest.json`; the
  ~120 regenerated PNGs are explained by `guard-docs-shots.sh`'s
  whole-app-surface hash (any `web/ui/**` change invalidates every shot),
  not scope creep.

## Incident during review (pipeline hygiene, not a code defect)

Mid-review, a concurrent process on this same shared checkout
(`/home/user/universal-till`) switched branches and rebased
`fix/1924-memoize-receipt-policy-ask` — visible in `git reflog`. The
reviewer caught it via an unexpected test-not-found result, re-checked out
this branch's own commit, and redid every working-tree-dependent check
(build/vet/tests/mutation-checks/guards) cleanly afterward, confirming
`HEAD` before and after each command. Final state verified clean. Per
`BUILD-CYCLE.md` step 4's own `ut-docs#386` precedent, an independent
review should run with `Agent(isolation: "worktree")` rather than sharing
the orchestrator's own checkout when other branch work is happening
concurrently in the same cycle — noted for next time, not acted on
retroactively since no damage resulted here.

## Residual risk (explicitly not fully closed)

- ut-docs#1936 (settings `.backups` Date field) and ut-docs#1938
  (`fiscal_device.html`) are both filed but not fixed — deliberately, to
  keep this diff at its own stated 6-site scope rather than widen it.
