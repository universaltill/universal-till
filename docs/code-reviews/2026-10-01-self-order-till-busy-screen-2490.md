# Review: self-order "till busy" screen, separate from "table in use" (ut-docs#2490)

- **Date:** 2026-10-01
- **Lane:** lane:cloud-54
- **Author:** Opus 5.5. **Reviewer:** Fable (independent, fresh context, isolated worktree). The card is `complexity:easy`, but Opus wrote it, so the review used a different model, per MODEL-ROUTING.md.
- **Card:** universaltill/ut-docs#2490 (follow-up from the #2432 review, finding 2)

## The gap

`bindSelfOrderTableSession` reported one `busy` bool for three refusals, and
the guest page showed "This table is already in use" for all of them:

1. another phone's live session holds this table (really "table in use");
2. the per-source mint limiter (20/min) refused a new session;
3. `SessionBasketManager.BindTable` refused a mint because the manager is at
   `MaxLiveSelfOrderSessions` with no empty session to evict.

(2) and (3) mean the till is busy, so the guest should wait. They do not mean
someone else is at the table. On NAT'd guest Wi-Fi, (2) can hit a real
guest's first scan at a free table.

## What shipped

- `internal/pos/session_manager.go`: `BindTable` now returns a typed
  `BindRefusal` (`BindOK`, `BindRefusedTableInUse`, `BindRefusedTillBusy`,
  with `String()`) instead of `busy bool`. The return values are the only
  change. Lock scope is untouched, and `m.mu` is still released before every
  `SetTable`/`factory()` call.
- `internal/pages/self_order_page.go`: the limiter refusal maps to
  `BindRefusedTillBusy`. The page passes `TillBusy` to the template, and the
  doc comments now describe both screens.
- `web/ui/pages/self_order.html`: a `TillBusy` branch inside the busy screen,
  with new keys `selforder.till_busy.{title,hint}`. The retry button keeps
  `selforder.table_busy.retry` ("Try again").
- `web/locales/{en,ar,fa,tr}.json`: the two keys, placed next to
  `selforder.table_busy.*`.
- The manual: one sentence in the `tables` topic's QR bullet (en/de/ar/fa/tr)
  says the guest's phone tells them when the table is in use or the till is
  busy, and offers Try again. `make docs-shots` was regenerated. Every PNG
  came out byte-identical, so only `web/help/img/manifest.json` changed.
- `README.md`: the table-side ordering line now names both screens. It
  previously called the same-table refusal "till busy" and said "already
  has items", which had been stale since #2434.
- Language packs: `ut-plugin-language-{de,es,pt}` get follow-up PRs with the
  two keys translated (core merges first, because these are brand-new keys).

## Tests

- New `TestSessionBasketManager_BindTable_RefusalSaysWhy`. It fills the
  manager to the cap with item-holding sessions, then asserts all three
  outcomes:
  - another phone on the same table → `TableInUse`;
  - a free table while the manager is full → `TillBusy`;
  - the owner's own resume → `BindOK`.
- `TestSelfOrder_SessionCapAlone_ShowsBusyNotPanic` now requires the
  till-busy title and forbids the table-in-use one.
- `TestSelfOrder_RepeatedCookielessScans_RateLimitedInsteadOfUnboundedMinting`
  now requires every hit past the limiter's 20 to show till-busy, never
  table-in-use. Hits 2–20 are a real "table in use", because the first hit's
  empty session holds the table.
- The other pos tests assert only "refused or not", through a small
  `bindBusy` helper. The page-layer tests that call
  `bindSelfOrderTableSession` directly now compare against the typed
  refusal.

## TDD evidence

With `web/ui/pages/self_order.html` reverted to `origin/main`, both page
tests fail:
- the rate-limit test fails because attempt 20 shows table-in-use;
- the cap test fails because there is no till-busy title.

Restored, both pass. The author checked this, and the reviewer re-checked it
independently in its own worktree.

## Gate

`gofmt -l` is empty, and `go build ./...` and `go vet` pass. `go test ./...`
passes. One earlier run had spurious `internal/pages` failures ("unexpected
end of JSON input") because the locale files were rewritten mid-run; the
re-run on the settled tree was green. `golangci-lint` reports 0 issues.

Every guard in `ci.yml`'s `build` job passes, including `guard-i18n`,
`guard-kiosk-engine`, `guard-help-topics`, `guard-help-drift` and
`guard-docs-shots` with its self-tests, apart from these two:
- `shellcheck` / `guard-shellcheck-version` need a binary this container
  lacks. No shell script is touched, so CI runs them.
- `retry-with-backoff.sh` is a helper, not a guard.

The pack repos pass `validate.sh` and `check-key-drift.sh` against this
branch's `en.json`.

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | README still called the same-table refusal "till busy" | **Fixed:** both screens named |
| 2 | nit | The limiter answers before the table is looked at, so a source over its mint budget sees till-busy even at a held table | **Accepted, documented** at the limiter call. Both screens say "wait and try again", and the next retry shows the right one. The suggested `TableOwnerActive` probe ignores BindTable's shorter empty-session window, so it could misreport a freeable table as in use |
| 3 | nit | fa hint grammar (`رسیدگی کردن` takes `به`, not `را`); ar used `نقطة البيع` where ar.json mostly uses `الصندوق` | **Fixed** |
| 4 | nit | Manual documents neither guest screen | **Fixed:** one sentence in `tables` (all 5 locales) plus docs-shots |
| 5 | nit | Stale "busy=true" test message; no `String()` on `BindRefusal` | **Fixed** |

No findings on locking, callers (`BindTable` has one production caller), RTL,
offline-first, or the kiosk-engine isolation. The two recurring bugs
(`os.MkdirAll`, `paths.Data`) do not apply, because the diff writes no files.

## Verdict

Safe to merge.
