# Code review — locale date+time formatting + Indian grouping (ut-docs#1632)

**Date:** 2026-09-09
**Branch:** `feat/1632-locale-datetime-lakh-crore`
**Reviewer:** independent fresh-context Opus subagent, isolated git worktree
(different model from the implementer, per the `complexity:medium` routing
in the `scrum-master` skill's "Model routing by complexity")
**Verdict:** SAFE TO MERGE (one blocker found and fixed before merge)

## What shipped

ut-docs#1632 was split from #1130's own review into two remaining gaps:

1. **Remaining raw-timestamp display sites.** Journal list
   (`web/ui/partials/journal.html`), journal receipt detail
   (`web/ui/pages/journal_detail.html`), audit trail
   (`web/ui/pages/audit.html`), shifts current/history
   (`web/ui/pages/shifts.html`), and my-reports
   (`internal/pages/my_reports_page.go`, Go-side) previously rendered a raw
   RFC3339 string directly instead of going through #1130's `date`/
   `FormatDate` convention. Adds `httpx.FormatDateTime`/
   `FormatDateTimeLatin` (`FormatDate` plus a 24-hour clock,
   space-separated, same digit-shape substitution) and matching
   `datetime`/`datetimeUTC` template funcs mirroring `date`/`dateUTC`
   exactly (same accepted-value contract: `time.Time` or an RFC3339
   string, degrades to the raw string on a parse failure rather than
   rendering `""`). Wires all five sites through the new funcs/helper.
2. **Indian-style (lakh/crore) number grouping** for `en-IN`/`ur-PK` — real,
   shipped country defaults (`internal/data.BuiltinCountryDefaults`
   IN→en-IN, PK→ur-PK). `formatGrouped`'s existing `groupThousands` only
   does uniform 3-digit grouping; adds `indianGrouping(locale)` +
   `groupIndianStyle` (last 3 digits as one group, then every 2 digits
   leftward — `"123456789"` → `"12,34,56,789"`, the issue's own
   `"123456"` → `"1,23,456"` example) and wires it into `formatGrouped`
   alongside the existing path. Every other locale's grouping is
   untouched.

Out of scope, explicitly (title said "shift/journal/audit/my-reports";
body explicitly excluded shift-close/EOD-report pages, which use
`<input type="date">`, nothing server-rendered to localize): a broader
grep also found `settings.html` (backups list), `tills.html`
(`EnrolledAt`/`LastSeenAt`), `sync_quarantine.html`, `orders_list.html`,
and `reports_tab_tips.html` with the same raw-timestamp shape. Filed as
**ut-docs#1894** rather than folded in here — same reasoning
`BUILD-CYCLE.md`'s "what one item is allowed to cost" gives for not
ballooning a scoped card.

No new i18n keys (pure display-formatting reuse of an existing
mechanism) — `guard-i18n.sh`'s key count (1528) is unchanged, so
`lang-pack-drift`/the `ut-plugin-language-{de,es}` packs are unaffected.

## What the independent reviewer verified, not just read

Ran in an isolated git worktree (`isolation: "worktree"`), branch checked
out fresh from the WIP commit on `feat/1632-locale-datetime-lakh-crore`:

- `gofmt -l .` clean, `go build ./...` clean, `go vet ./...` clean,
  `golangci-lint run ./internal/httpx/... ./internal/pages/...` → **0
  issues**, `go test ./internal/httpx/... ./internal/pages/...` and
  `go test ./...` → fully green.
- Guards: `guard-i18n.sh`, `guard-data-access.sh`, `guard-help-topics.sh`,
  `guard-compliance-claims.sh`, `guard-kiosk-engine.sh` — all pass.
- **Manually re-verified the Indian-grouping arithmetic** beyond what the
  tests cover: a throwaway in-package probe against `1000000000` (10
  digits, → `1,00,00,00,000`, correct — 100 crore), 11/12-digit inputs,
  and the empty/`"0"`/`"000"`/`"0000"` and 3↔4-digit boundary cases. All
  correct, no off-by-one. Confirmed sign is stripped by `formatGrouped`
  before `groupIndianStyle` is called, so the "unsigned" precondition in
  its own doc comment genuinely holds. Probe file deleted, worktree
  confirmed clean afterward.
- **Traced `my_reports_page.go`'s locale-resolution move**: `locale` is
  now resolved once via `httpx.ResolveLocale` and used consistently by
  both the sent-report and pending-bundle code paths (previously computed
  conditionally, only inside the `moreNotShownText` branch). Confirmed
  `CapturedAt` has exactly one template consumer (plain display) and that
  sorting uses the separate `capturedAtTime` field, so this move doesn't
  affect ordering. Confirmed the earlier `ResolveLocale` call is itself a
  **fix**, not a regression: `/my-reports?lang=x` now sets the `ut_lang`
  cookie unconditionally like every other page, not only when
  `totalSent > rowLimit`; the resulting duplicate `Set-Cookie` (`Render`
  calls `ResolveLocale` again) is a pre-existing, repo-wide pattern
  (`backup_api.go`, `data_api.go`, `eod_api.go`), harmless.
- **Traced every render path that gained a `datetime` call** back to
  `httpx.FuncsFor`, specifically checking the sale-completion path
  (`ui.NewJournalView(funcs)` in `pos_api.go:1823`, re-rendered on every
  completed sale) — a `FuncMap` missing `datetime` there would break
  checkout. Every `funcs` value in `pos_api.go` traces back to
  `httpx.FuncsFor`. Safe.
- **Verified production write paths actually produce RFC3339** (not just
  the test fixtures): `internal/pos/shifts.go`'s `InsertShift`/
  `UpdateShiftClose`/`insertAudit` all pass explicit
  `time.Now().UTC().Format(time.RFC3339)` timestamps; noted the schema's
  `DEFAULT (datetime('now'))` would produce a non-RFC3339 string that
  `datetime` degrades to unchanged rather than a blank cell, but no
  production insert on these columns relies on that default.
- Confirmed the diff performs **zero filesystem I/O** — the two recurring
  bug classes this pipeline keeps finding (a missing `os.MkdirAll`, a
  cwd-relative path where `paths.Data(...)` belongs) don't apply.
- Confirmed scope: exactly the 16 files this record lists touched;
  `settings.html`/`tills.html`/`sync_quarantine.html`/`orders_list.html`/
  `reports_tab_tips.html` (ut-docs#1894), shift-close/EOD print pages,
  `web/locales/**`, and `internal/data/**`/`internal/db/**` are
  untouched.
- Authorship: `Farshid Mirza <4035824+farshidmirza@users.noreply.github.com>`
  on every commit, no AI identity, no secrets, no real shop/client names.

## Finding and what was done about it

1. **`guard-deadcode-baseline.sh` would fail on a new unreachable
   `FormatDateTimeLatin` — Blocker, fixed.** The guard's whole-program
   `deadcode` analysis (`-test=false`, so a test-only call site is
   invisible to it) would flag the new `FormatDateTimeLatin` as
   unreachable, since its only pre-fix references were its own
   definition and `dateformat_test.go`. It runs as a required step in
   `ci.yml`'s `desktop-shell` job — a separate job from `build`, which is
   why `CLAUDE.md`'s guard list doesn't mention it, and why this didn't
   show up in the guards actually run during Dev/Tester. The guard
   couldn't be run verbatim in review either (needs
   `libgtk-3-dev`/`libwebkit2gtk-4.1-dev`, absent in this environment) —
   confirmed instead via `go run golang.org/x/tools/cmd/deadcode@latest .`
   against the `.` root, which reproduces the same unreachable-function
   detection (minus the `desktop`-tagged root) and already matched the
   guard's own shape by independently reproducing a pre-existing baseline
   entry (`FormatQty`).

   **First fix attempt was wrong and caught by re-running the same
   check.** The obvious fix — two existing sites (`print_api.go:209`,
   `eod_api.go:273`) hand-rolled the exact same
   `FormatDateLatin(t.Local(), locale) + " " + t.Local().Format("15:04")`
   expression `FormatDateTimeLatin` now replaces — was to migrate both.
   Doing that made `FormatDateLatin` **itself** newly unreachable: those
   two call sites were its *only* production callers, so migrating both
   just traded one guard violation for another rather than fixing
   anything. Caught by re-running the same `deadcode .` check after the
   two-site migration, before pushing. **Actual fix:** migrate only
   `print_api.go`'s call site to `FormatDateTimeLatin`; leave
   `eod_api.go`'s as `FormatDateLatin` + concatenation. Re-ran
   `deadcode .` — neither function appears in the unreachable list
   afterward, and the added comment on both call sites explains why only
   one was migrated so a future edit doesn't "finish the job" and
   reintroduce the problem. Re-ran `go build ./...`, `go vet ./...`,
   `golangci-lint run ./internal/pages/...` (0 issues), and the affected
   test subset (`go test ./internal/pages/... -run
   'PrintApi|Print_|Receipt|EOD|Eod'`) plus a full `go test ./...` —
   all green — after this correction.

## Verified clean, explicitly

- `datetimeUTC` has zero template call sites today (`dateUTC` has one,
  `invoices.html:32`) — it's a closure value in a `FuncMap`, so it does
  **not** trip `deadcode` (unlike a package-level func), and exists for
  symmetry with `dateUTC`/`date`. Not a finding, noted for whoever next
  audits `FuncsFor`.
- `indianGrouping` compares the full locale tag exactly, so `en_IN`
  (underscore) would return `false` where `en-IN` returns `true`. Matches
  `dateLayout`'s own pre-existing `"en-us"` special case's identical
  limitation, and `BuiltinCountryDefaults` only ever emits hyphenated
  tags — consistent with existing precedent, not changed here.
- Every added SQL is in `_test.go` seed helpers only — `guard-data-access.sh`
  confirms, reviewer's own read agrees.

## TDD re-verification

Picked the Indian-grouping change (the most novel logic in this diff —
new grouping-width arithmetic, not just wiring an existing helper into
more call sites) for a real revert-then-restore, done inside the isolated
worktree:

- Reverted `indianGrouping`/`groupIndianStyle`/the `formatGrouped` branch
  in `internal/httpx/currency.go` to the merge-base version, leaving
  `currency_test.go` untouched.
- `go test ./internal/httpx/... -run TestFormatMoney_IndianGrouping` and
  `TestFormatQty_IndianGrouping` **failed with real, specific assertion
  messages** (not a compile error), e.g.
  `FormatMoney(12345678900, en-IN) = "₹123,456,789.00", want "₹12,34,56,789.00"`.
  The `1,234`/`123`/`en-GB` control cases in the same test correctly did
  **not** fail (identical under both groupings) — confirms the test
  discriminates the new behavior precisely rather than over-asserting.
- Restored the fix — both tests pass again. Worktree confirmed
  byte-identical to the pre-revert `HEAD` afterward.

## Documentation

No manual (`web/help/`) topic needed updating — this is a pure display-
formatting change to pages whose manual entries don't document exact
timestamp formatting, and no new page/route was added
(`guard-help-topics.sh` confirms route coverage is unaffected). No
README changes apply.

## Explicitly deferred (not this card)

- ut-docs#1894 — the remaining raw-timestamp sites outside this card's
  named scope (settings backups, tills, sync-quarantine, orders,
  reports tips).
