# Code review: per-table self-order baskets / session-scoped guest ordering (ut-docs#2261)

**Date:** 2026-09-19
**Author (Dev):** Scrum Master pipeline, Fable via Dev subagent
**Tester:** Sonnet, same pipeline (found and fixed the cookie-Path blocker, see below)
**Reviewer:** independent Opus subagent, isolated worktree, fresh context
**PR:** universal-till (branch `feat/2261-session-scoped-guest-basket`, `9e2a217..a3c77d7`)

## What shipped

ut-docs#815 built table-QR guest ordering (`/self-order?table=<id>`) on top of
the ONE till-process-global kiosk basket (`common.Deps.KioskEngine`), so only
one table could order at a time process-wide; a second table's guest was
fail-safed onto a "till busy" interstitial. This card gives every table its
own `*pos.Service`:

- New `internal/pos.TableSessions` — an in-memory, mutex-guarded store keyed by
  a 16-byte `crypto/rand` session token (`byToken`) with a `tableID -> token`
  secondary index (`byTable`), an idle TTL (4h) and a `Sweep`.
- `internal/pages/self_order_page.go` sets a `self_order_session` cookie on a
  valid `?table=` landing; `resolveOrderEngine(d, r)` resolves it back to that
  table's engine on ~28 call sites in `self_order_shop.go`, falling back to the
  bare `d.KioskEngine` when there is no live session (unknown token, idle-
  evicted, or a till restart — the store is deliberately not persisted, matching
  `KioskEngine`'s own long-standing durability contract).
- Peripheral broadcast/scan sites updated: settings ×2, setup, `newRederiveSettings`,
  `update_api.go`'s mid-order auto-update guard, `data_api.go`'s `cleanupInLiveBasket`.
- Same-table rule: any device visiting `?table=X` JOINS X's live session (one
  shared cart per table, the Toast/Square/Lightspeed model), surfaced as a
  non-blocking `?joined=1` banner. The cross-table busy interstitial is deleted —
  cross-table collision is now structurally impossible.
- Locale key `selforder.table_joined` in ar/en/fa/tr, `web/help/*/tables.md`
  updated in all five locales, README roadmap item flipped to done.

## Known history (not re-discovered here)

The Tester found a **critical bug in Dev's original implementation**: the session
cookie was scoped `Path: "/self-order"`, which by RFC 6265 is NOT a path-prefix of
`/api/self-order/*` — so every real scan/cart/checkout request carried no cookie
and silently fell back to the shared bare `KioskEngine`, reproducing the exact
cross-table collision this card exists to remove. Every Go test still passed,
because the existing `selfOrderClient.do` helper calls `req.AddCookie()`
unconditionally and bypasses path-matching. Fixed to `Path: "/"` (matching
`auth.CookieName`'s own convention) and covered by
`TestSelfOrder_SessionCookieReachesAPIEndpoints_RealCookieJar`, which drives a
real `net/http/cookiejar` against a real `httptest.Server`.

## Independent verification of the Tester's TDD claim

Re-verified personally rather than taken on trust — reverted both `Path: "/"`
lines to `Path: "/self-order"` in the isolated worktree:

```
=== RUN   TestSelfOrder_SessionCookieReachesAPIEndpoints_RealCookieJar
    self_order_session_test.go:414: no session cookie set after table landing
--- FAIL: TestSelfOrder_SessionCookieReachesAPIEndpoints_RealCookieJar (0.18s)
```

and restored:

```
=== RUN   TestSelfOrder_SessionCookieReachesAPIEndpoints_RealCookieJar
--- PASS: TestSelfOrder_SessionCookieReachesAPIEndpoints_RealCookieJar (0.19s)
```

Confirmed genuinely red-before / green-after. (It trips at the jar-emptiness
assertion rather than the deeper "where did the scan land" one — the cookie jar
refuses to return a `Path=/self-order` cookie for the server root either. Still
a true RFC 6265 path-matching failure; the test does its job.)

## Independent review findings

Verdict: **safe to merge after the fixes below**, all applied in this branch.

### 1. BLOCKER (fixed) — a table's second sitting lost its table binding

`internal/pages/self_order_page.go`. `pos.Service.Reset()` (`resetLocked`,
`internal/pos/service.go:1485-1486`) clears `tableID`/`tableLabel`, and every
completed checkout calls it — `completeCounterOrderCheckout`'s explicit
`engine.Reset()` and `completeTender`'s own. But the *session* stays live in
`TableSessions` for the whole 4h idle TTL. The handler only called
`engine.SetTable(...)` on `!existed`, with the comment "A joined session already
carries it" — which is false after any checkout.

So the normal restaurant case (a table orders a second round; or a new party is
seated and scans the same printed QR within the TTL) ran with **no table bound
at all**. Reproduced with a probe test:

```
AFTER CHECKOUT: session still live, engine.TableID()="" engine.TableLabel()=""
SECOND SCAN: sameSession=true engine.TableID()=""
SECOND CHECKOUT PICKER contains name="method" (card picker): true
BUG CONFIRMED: second sitting at table "01aaf67b-…" has TableID()="" — table binding lost
```

Two user-visible consequences, both of them the ut-docs#815 bug class re-entering
through the new store:

- `selfOrderForcesCounterCheckout` went false, so the guest was offered the
  **card payment picker on their own phone**, which has no terminal — contradicting
  ut-docs#815's explicit Architect decision and what `web/help/*/tables.md` now
  promises in all five locales ("That order always goes to the Pay at counter board
  … a guest's own phone has no card terminal attached to it").
- The resulting `kiosk_counter_orders` row carried an empty `table_id`, so the
  order never reached the table's floor-plan tile.

**Fix:** re-assert the binding on every `?table=` entry, not only on a freshly
minted session, clearing a leftover takeaway choice first so ADR-0073 Decision 5
doesn't make `SetTable` a no-op. A no-op when the binding is already correct.
The label is compared alongside the id, so a table renamed on the floor plan
mid-session also refreshes the cached label the counter order is filed under
(that staleness was latent in the original `!existed` version too).
Covered by a new regression test,
`TestSelfOrder_TableRebindsAfterCheckout_SecondSittingStaysTableBound`, verified
red before the fix (`second sitting engine lost its table binding: TableID=""`)
and green after.

### 2. MEDIUM (fixed) — one peripheral call site was missed

`internal/pages/settings_page.go:1901`. `POST /api/settings/remove-demo-catalogue`
guards against deleting a demo item that is sitting in a LIVE basket (ut-docs#633:
a live basket has no `held_sales` row for the removal SQL's own safety check, so
the delete would succeed and a later tender would FK-fail with no clear recovery).
`demoDataInLiveBasket(d.Engine, d.KioskEngine)` scanned the cashier and bare-kiosk
baskets only — not table sessions.

This is not a judgement call: `data_api.go`'s `cleanupInLiveBasket` is the
documented sibling of this exact guard ("Same guard shape as
remove-demo-catalogue's `demoDataInLiveBasket`", `data_api.go:441-442`), and Dev
*did* update that one. **Fix:** `demoDataInLiveBasket` now takes the
`*pos.TableSessions` and appends every live session's engine under
`kioskBasketMatch`, mirroring `cleanupInLiveBasket` exactly.

I enumerated every other `KioskEngine` reference under `internal/` to confirm the
peripheral list is now complete: `data_api.go:804`, `init.go:675`,
`setup_page.go:620`, `settings_page.go:1901/2593/2912`, `update_api.go:125`. Seven
sites; Dev covered six.

### 3. COSMETIC (fixed) — "0 item(s) already added"

A guest joining a live-but-empty session (nobody has added anything yet, or the
table just checked out — which is now the common case after finding 1) was shown
`Ordering for this table — 0 item(s) already added`. The `?joined=1` flag and the
shop page's `joinedCount` are now both gated on the basket actually holding lines.

### 4. DOC (fixed) — inaccurate nil-safety claim

`internal/pos/table_sessions.go`'s type comment said "A nil `*TableSessions` is an
inert no-op for every method". `Lookup`/`All`/`SetConfigAll`/`AnyHasItems`/`Sweep`
are; `SessionForTable` takes `s.mu` unguarded and panics. Its one caller already
checks `d.KioskSessions != nil`, so this was a comment trap, not a live bug —
comment corrected to state the exception and why (it mints state; there is no
sensible nil answer).

### 5. PROCESS (done) — docs-shots surface hash

My own edits to `internal/pages/**.go` tripped `guard-docs-shots.sh`
(whole-file surface hashing). No docs topic screenshots `/self-order` or
`/self-order/shop` — the routed topics are admin pages — and the
`settings_page.go` change is a function signature with byte-identical markup, so
this alters no rendered pixel. Refreshed via the documented escape hatch,
`scripts/ci/update-docs-shots-surface-hash.sh` (one-field manifest diff, confirmed).
**The commit needs a `Docs-Shots-Unchanged: true` trailer.**

## Concurrency review (`internal/pos/table_sessions.go`)

Traced rather than assumed. The locking is sound:

- Every access to both maps is under `s.mu`. `SessionForTable`'s
  check-then-create is one uninterrupted critical section — no TOCTOU gap
  between "is there a live session for this table" and minting one.
- A `*Service` returned by `Lookup`/`SessionForTable` outlives the store's lock,
  but that is safe: evicting from the maps does not destroy the `*Service`, and
  `*Service` self-serializes every exported method under its own `mu`. A
  concurrent `Sweep` cannot corrupt a caller mid-use — worst case the request
  mutates a basket that has just become unreachable.
- There is no lost-eviction race either, because both `Lookup` and `Sweep` take
  the same mutex: if `Lookup` wins it stamps `lastActivity`, so the following
  `Sweep` sees a fresh session; if `Sweep` wins, `Lookup` misses and the request
  degrades to the bare kiosk.
- `evictLocked` only drops the `byTable` entry when it still points at *this*
  token, so a replacement session for the same table cannot be clobbered.
- `Sweep` deletes from the map it is ranging — well-defined in Go.
- No unbounded growth even if `Sweep` never ran: `byTable` is keyed by table ID
  and `SessionForTable` evicts the stale token before minting a replacement, so
  both maps are bounded by the number of enabled tables. `Sweep` is genuinely
  hygiene-only, as its own comment claims.

## Security review

No gap found versus the single-global-engine design it replaces.

- Session token is 16 bytes of `crypto/rand`, hex — unguessable.
- Table IDs are UUIDs (`uuid.NewString()`, `internal/data/tables_repo.go:129`), so
  `?table=` is not enumerable by a LAN client; joining a table's cart needs its
  printed QR, i.e. physical presence. The table must also be `Enabled`.
- Cookie is `HttpOnly`, `SameSite=Lax`, no `Secure` (a LAN till is plain HTTP —
  matching `auth.CookieName`). `Path: "/"` is required, per the Tester finding.
- A stale or forged cookie resolves to nothing and degrades to the bare kiosk; it
  can never reach a *different* table's basket, since the token is the only index.
- `resolveOrderEngine` is referenced only from the two self-order files (verified
  by grep), so the `Path=/`-scoped cookie is inert on every cashier/admin route.
- The design does mean anyone who can read a table's QR can add to or check out
  that table's cart. That is the intended product decision (shared cart per table)
  and strictly *less* exposed than the previous single process-global basket that
  any anonymous LAN client could mutate.

## Other checks

- **ADR:** agreed, no new ADR needed. ADR-0020 requires the kiosk basket to be a
  separate instance from the cashier's; this creates *more* kiosk-side instances,
  strengthening rather than contradicting it, and reuses ADR-0054/ut-docs#820's
  existing `TableID`/`TableLabel` fields unchanged. Nothing in the diff contradicts
  an accepted ADR. An amendment note on ADR-0020 would be documentation polish, not
  a gate.
- **Recurring pipeline bug classes:** N/A and confirmed — no file-write handler and
  no cwd-relative path anywhere in the changed files (the store is in-memory only;
  grep for `os.WriteFile`/`os.Create`/`os.MkdirAll`/`paths.Data` returns nothing).
- **Demo/seed naming:** clean. No seed data added; new test fixtures are `T1`/`T2`/`T5`
  and "Flat White"/"Earl Grey". No real client or shop name anywhere in the diff.
- **`guard-kiosk-engine.sh`:** passes, and I confirmed *why* rather than trusting the
  exit code — the guard greps `<ident>.Engine` as code (comments stripped) in files
  registering a `/self-order` route; `.KioskEngine` cannot match (no dot directly
  before `Engine`), and neither self-order file names `d.Engine` at all.
- **i18n:** `selforder.table_joined` present and semantically correct in ar/en/fa/tr
  (checked each translation, not just key presence); the guard's own format-verb
  check covers the `%d`. `web/help/*/tables.md` gained a real, equivalent sentence in
  all five locales — not a token gesture. RTL-safe: the new `.selforder-joined` rule
  uses only logical properties (`margin-block-end`, `padding-inline`, `text-align:start`).

## Gate re-run after the fixes

`gofmt -l .` (no output), `go build ./...`, `go vet ./...`,
`golangci-lint run ./...` (**0 issues**), `go test ./internal/pos/... -race -count=1`
(**ok, 106.9s**), `go test -count=1` over every package except `internal/pages`
(**59 ok / 17 no-test-files / 0 FAIL**), and
`go test -race -timeout 60m -count=1 ./internal/pages/...`:

```
ok  github.com/universaltill/universal-till/internal/pages               1260.272s
ok  github.com/universaltill/universal-till/internal/pages/catalog        106.469s
ok  github.com/universaltill/universal-till/internal/pages/common         151.689s
ok  github.com/universaltill/universal-till/internal/pages/itemsnav         1.340s
ok  github.com/universaltill/universal-till/internal/pages/settingsnav      1.315s
```

No data races. (`internal/pages` needs the explicit `-timeout 60m`; at 1260s it
blows past `go test`'s 10-minute default, which reads as a spurious FAIL.)

Guards: `guard-data-access`, `guard-kiosk-engine`,
`guard-page-http-error`, `guard-i18n`, `guard-compliance-claims`,
`guard-docs-shots`, `guard-help-topics`, `guard-help-drift`, `guard-htmx-loaded`,
`guard-osk-loaded`, `guard-emoji-font`, `guard-autofill-suppression` — all green.

`guard-deadcode-baseline.sh` cannot run in this sandbox: it needs the GTK/WebKit
dev headers that its own CI job installs (`.github/workflows/ci.yml`, the
`deadcode` job's "Install GTK/WebKit dev headers" step) to type-check
`cmd/unitill-desktop`'s cgo files, and fails with `could not import C` without
them. An environment limitation, not a finding — same class as the documented
`cmd/unitill-desktop` golangci-lint exclusion (ut-docs#1581).

## Merge-time note: language-pack drift

`selforder.table_joined` is new in `web/locales/en.json`, and the shipped locales
here are only ar/en/fa/tr — de and es live in the external
`ut-plugin-language-{de,es}` packs. Per this repo's CLAUDE.md, `lang-pack-drift`
is **advisory on the PR but blocking on `push` to `main`**, so merging this
without a matching pack PR turns `main` red. Land the two pack follow-ups
alongside the merge, or expect to chase a red `main` immediately after.

## Non-blocking follow-ups (out of scope for this card)

1. **An abandoned table basket lingers for the full 4h TTL and the next party joins it.**
   Post-checkout is clean (`Reset` empties it), but a party that scans, adds items and
   leaves *without* ordering leaves those lines for the next sitting, which JOINs them.
   The "joined — N item(s) already added" banner makes it visible rather than silent and
   the lines are removable, but there is no staff-side way to clear a table's session.
   Suggest wiring the floor plan's existing **Free table** action to also drop that
   table's `KioskSessions` entry. This, not the TTL value, is the right lever — 4h and
   the 15-minute sweep are otherwise sane.
2. **`guard-kiosk-engine.sh` is now slightly narrower than the risk.** The acting engine
   arrives as a `*pos.Service` parameter (`renderKioskCart(w, r, d, engine)`,
   `selfOrderForcesCounterCheckout(d, engine)`, …), so a future caller could in principle
   hand those helpers `d.Engine` from a file the guard does not scan. Nothing does today.
   Worth extending the guard to flag `d.Engine` passed into the kiosk helpers.
3. **Orphaned `selforder.table_busy.{title,hint,retry}`** (3 keys × 5 locales) left behind
   by the deleted interstitial. Deliberate and harmless — `guard-i18n.sh` checks parity,
   not orphans — and removing them from `en.json` needs a coordinated follow-up in
   `ut-plugin-language-{de,es}`. Worth a cleanup card.
