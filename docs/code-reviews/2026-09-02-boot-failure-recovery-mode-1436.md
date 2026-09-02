# Code review: boot-failure recovery mode (ut-docs#1436)

- **Card**: universaltill/ut-docs#1436 — server-hosted recovery page/API on
  a startup failure, the buildable-this-cycle slice split from the
  boot-failure-recovery epic (#1415).
- **Design**: ADR-0075 (`ut-docs/adr/0075-boot-failure-recovery-mode-is-server-hosted.md`).
- **Reviewer**: independent Opus subagent (complexity:medium → Sonnet
  built, Opus reviewed), run in an isolated `git worktree` so its
  revert-then-restore TDD re-verification never touched this session's
  shared checkout (ut-docs#386's own mitigation).
- **Dev/Tester**: this session (Sonnet), including real-process testing —
  built the actual `unitill-pos` binary and ran it against a genuinely
  corrupted database and, separately, a temporarily-injected broken
  migration file (both removed before commit) to prove the recovery page,
  safe mode, RTL rendering, locale fallback, and the retry flow end-to-end,
  not just via `go test`.

## What shipped

`internal/app.Run`'s boot sequence now loops: on a startup failure it can
categorize as safe to retry in place — a migration error, a failed
staged-restore apply, a generic DB-open/corrupt-file error, or disk-full —
it calls `recovery.Serve` instead of returning the error. `Serve` binds the
till's normal `cfg.ListenAddr` (never a fallback port) and serves a minimal,
translated (en/tr/ar/fa) recovery page, `GET /healthz` → 503, and
`POST /api/recovery/retry`; on Retry it re-attempts the SAME boot sequence
in the SAME process — no restart. `db.ErrDataDirLocked` is deliberately
excluded from this — it stays the existing fatal-refusal path #1097
established. A migration-failure whose read-only reconnection can actually
serve `ListSalesJournal` gets a safe-mode section: read-only today's-sales
view + CSV export.

Because every shell (Android WebView, desktop native webview, Pi kiosk
Chromium) already just points a browser view at the till's own address,
this is the entire recovery UI for all three, with zero shell-native code —
those shells' own timing/kiosk-lock fixes are separate sibling cards
(#1437/#1438), deliberately out of scope here.

## Independent review — findings and disposition

The Opus review ran the full gate itself (build/vet/test/guards, all
CI-blocking guards, not just the three asked for), independently
re-verified all four TDD claims by reverting each fix on disk and
re-running the specific test, and did NOT find any blocking defect in the
core mechanism: the data-dir-lock stays outside the retry loop and fatal,
`net.Listen` is used directly with no port fallback, safe mode is genuinely
read-only, template escaping is correct (checked via an XSS probe), and no
file-write/path-construction bug of the two classes this pipeline
repeatedly finds. It found seven real should-fix issues and nine
nitpicks. All seven should-fixes were fixed in this same session, each
re-verified with its own new or extended test (some via honest
revert-then-restore red→green, matching this pipeline's TDD discipline):

| # | Finding | Fix | Verified |
|---|---|---|---|
| 1 | CSV formula/injection: `ReceiptNo`/`TillName` (both operator/setting-editable free text) written to the safe-mode CSV export unescaped — this repo has an established `csvSafe()` convention at 5 other call sites (ut-docs#195/#1020) that this one skipped | Added a local `csvSafe` (duplicated, not imported — see its doc comment for why not a cross-package refactor in this PR) and applied it to both fields | `TestCSVSafe_PrefixesFormulaLeadingCharacters` |
| 2 | Safe mode was advertised the moment `db.OpenReadOnly` succeeded, but `ListSalesJournal` joins `tills` (migration 014) and reads `sales.till_id` (015) of 78 — a migration failure anywhere before those leaves a readable file the query still can't run against; reviewer reproduced a live 500 with the raw SQL error echoed to the client | New `probeSafeMode` runs the real query once at `Serve` time; safe mode (page section + routes) is only offered if it actually succeeds. Failures now log server-side and return a generic message, never the raw driver error | `TestProbeSafeMode_FailsAgainstPreTillsSchema` / `…SucceedsAgainstAFullyMigratedDatabase`, plus the `Serve`-level `TestServe_SafeModeNotOfferedWhenTheQueryCantRunAgainstTheSchema` (revert→red→restore→green confirmed) |
| 3 | Recovery mode had no authentication at all: `cfg.ListenAddr` defaults to `:8080` (every interface), so an unauthenticated device anywhere on the shop LAN could read today's full sales journal and download the CSV, unlike normal operation's `/journal` (behind `auth.Middleware`) | New `requireLoopback` middleware refuses any request whose remote address isn't the local machine, applied to both safe-mode routes. Every legitimate caller (desktop shell, in-process Android `TillService`, the Pi kiosk launcher's `UT_KIOSK_URL` default) already connects via `127.0.0.1` — verified by reading each shell's own connection code, not assumed | `TestRequireLoopback_RefusesNonLoopbackRemoteAddr` / `…AllowsLoopback` |
| 4 | `web/help/{en,tr,ar,fa}/recovery.md` described a "Reset data" action that doesn't exist in this card (deferred to #1440) — the manual would send an operator hunting for a button that isn't there, mid-outage | Corrected the wording in all four locales to describe only what actually ships (Retry + read-only safe mode) | `guard-help-topics.sh` (structural) + manual re-read (prose) |
| 5 | The retry-poll JS gave up after 15s and silently left the button re-enabled with no explanation — exactly the case (a real migration re-run) most likely to legitimately take longer than that | Extended the poll budget (~500ms × 30, then 2s-interval up to ~4 minutes total) and added a "still working" message (`recovery.taking_longer`, new key) shown once the fast phase elapses, instead of silently going idle | Read/reasoned verification (JS, not covered by Go tests); manual real-process check of the page's script logic |
| 6 | ADR-0075's own Decision text (already merged into `ut-docs` main before review landed) listed "data-directory lock held" among the recoverable causes, contradicting what the code actually — correctly — does | Follow-up PR (`ut-docs` `docs/adr-0075-fix-recoverable-list`) correcting the Decision section; the Scope-boundary paragraph already delegated this correctly, only the earlier list was stale | N/A (doc-only) |
| 7 | `POST /api/recovery/retry` is necessarily unauthenticated (same reasoning as #3) and was unthrottled — each retry re-runs real file-system work (legacy-migrate check, restore-apply, `db.Open`) | Added a 2-second minimum interval between accepted retries (429 otherwise) | `TestRetryHandler_ThrottlesRapidRequests` |

**Nitpicks accepted as-is, not fixed in this PR** (reviewer's own list,
items 8–16): `GET /{$}`-only routing (every shell points at root today);
`cw.Error()` after `Flush()` in the CSV handler — now actually checked as
part of item 1's rewrite; templates parsed per-request instead of cached
(negligible traffic here); `guard-i18n.sh` doesn't scan
`internal/recovery/templates/*.html` (outside its `web/ui/**` glob) — all
14 keys verified correct by hand, but nothing enforces it going forward,
worth a Backlog card if this pattern grows; `KindDiskFull` gets no safe
mode (deliberate, per the card's AC); `Classify`'s default-to-`KindDBOpen`
catch-all; port TOCTOU in test helpers (standard, low flake risk); the
`lang-pack-drift` advisory for the 15 new `en.json` keys (expected, not
blocking per `CLAUDE.md`).

## Verified beyond automated tests

- Real `unitill-pos` binary run against a genuinely corrupted database file
  (SQLite header overwritten) → recovery page confirmed via `curl`, not
  just Go test assertions; `/healthz` 503 confirmed live.
- A temporarily-injected broken migration (`999_tester_deliberately_broken.sql`,
  removed before commit, never part of any commit) → `Kind: migration`
  correctly classified, safe-mode section shown and functional (page + CSV
  export), retry-without-fixing-the-cause confirmed to fail again cleanly
  with a fresh ref code, no crash/hang.
- RTL rendering confirmed live for `ar`/`fa` (`dir="rtl"`, translated
  strings); unknown-locale fallback confirmed (`dir="ltr"`, English text,
  no crash).
- Currency-defaults-to-GBP gap found via this same real-process testing
  (not by reading code) and fixed same-session (see the currency row below).
- Independent review's own real-process check of the `probeSafeMode` gap
  (dropped `tills` table, confirmed live 500) — now a permanent regression
  test.

## Also fixed this session (found via real-process Tester testing, before Review)

- `httpx.ActiveCurrency()` silently defaults to GBP in safe mode because
  `pages.Init`'s normal `InitCurrency` call never runs in recovery mode —
  `Serve` now reads the shop's real `store.currency` setting from the
  read-only DB first. `TestServe_SafeModeReadsTheShopsRealCurrency`,
  revert→red→restore→green confirmed.

## Gate (final, after all fixes)

```
gofmt -l .                                                    → (no output)
go build ./...                                                → (no output)
go vet ./...                                                  → (no output)
go test ./...                                                 → all packages ok, zero FAIL
go test -race -count=1 ./internal/recovery/ ./internal/db/ ./internal/app/
                                                                → all ok
guard-data-access.sh / guard-i18n.sh / guard-help-topics.sh   → ✓ / ✓ / ✓
```

## Explicitly deferred (separate sub-cards, not this PR)

- #1437 — Android kiosk-lock-only-when-healthy + WebView wiring to this
  page (flagged `blocked:env`: Android SDK unreachable in this session).
- #1438 — Desktop shells opening the window on this page instead of
  exiting silently (flagged `blocked:env`: GTK/webkit2gtk unreachable in
  this session).
- #1439 — wiring the "Send diagnostics" action once #1391 (diagnostic
  mode) ships.
- #1440 — Restore-backup/Reset-data actions + Pi-kiosk exit tying into
  #399.
- A `web/ui/**`-only `guard-i18n.sh` gap for `internal/recovery/templates/*`
  (nitpick #11 above) — worth its own Backlog card if more standalone
  (non-`web/ui`) templates appear; not filed separately this cycle since
  it's a single, currently-correct-by-hand instance.

## Verdict

**Safe to merge.** No blocking issues remain; all seven should-fix findings
from independent review are fixed and verified, the full gate is clean, and
the change stays within its declared scope (server-side only, per ADR-0075).
