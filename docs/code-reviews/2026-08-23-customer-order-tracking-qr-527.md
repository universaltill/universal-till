# Code review — ut-docs#527: customer order tracking via QR

**Date:** 2026-08-23
**Card:** [ut-docs#527](https://github.com/universaltill/ut-docs/issues/527)
**Complexity:** hard
**Build model:** Fable (subagent) — **Review model:** Opus (independent worktree-isolated subagent, deliberately not Fable, per the hard-tier routing rule)

## What shipped

After a self-order kiosk checkout, the confirmation screen shows a QR code
linking to `/o/{token}` — an anonymous, no-login, status-only tracking page
for that one order (new → preparing → ready → collected/cancelled), polling
its status every 15s via HTMX and stopping automatically once the order
reaches a terminal state. The token behaves identically whether it is
unknown, malformed, or has simply expired: a 3h-old collected/cancelled
order's token stops resolving (404 on the full page; the poll fragment
degrades to a static "link expired" state with no re-armed trigger),
computed in Go from the timestamp already returned by the lookup — no cron,
no extra writes.

**Scope, decided at Architect**: v1 is the self-order kiosk checkout flow
only, over the shop's own LAN/WiFi (the QR encodes the till's own
`advertisableHost`-derived URL). Off-LAN/mobile-data reachability, a
cashier-lane receipt QR, and browser push notifications are explicit
non-goals, filed as follow-ups (ut-docs#907/#908/#909) rather than bolted
onto this card.

New surface:
- Migration 058 — `sales.tracking_token` (nullable TEXT) + a partial unique
  index (`WHERE tracking_token IS NOT NULL`), so untracked/cashier sales are
  unaffected.
- `internal/data/order_tracking_repo.go` — `EnsureOrderTrackingToken`
  (idempotent: a re-rendered confirmation screen gets the same token back,
  never a second URL for the same order; the mint is a guarded
  `UPDATE ... WHERE tracking_token IS NULL` + re-read, so a concurrent first
  call can't produce two tokens for one sale) and
  `LookupOrderByTrackingToken` (unknown/malformed/empty all return
  `found=false, err=nil` — an anonymous surface must never distinguish a
  guess from a typo).
- `internal/pages/order_tracking.go` — `GET /o/{token}` (full page) +
  `GET /o/{token}/status` (poll fragment), `/o/` added to
  `internal/auth/middleware.go`'s `exempt()` prefix list. Deliberately a
  new, separate status renderer from the operator-facing
  `writeOrderStatusFragment` — the anonymous page must never show who
  changed the status.
- QR wiring in `self_order_shop.go`/`self_order_confirmation.html` —
  best-effort, strictly after the sale commits, typed `template.URL` (a
  plain `string` data: URI gets filtered to `#ZgotmplZ` by `html/template`'s
  URL sanitizer — confirmed empirically, see "Found along the way" below).
  Confirmation's auto-return timer bumped 6s→20s, a deliberate change (6s
  isn't enough time to notice the QR, get a phone out, and scan it — "Done"
  still exits immediately).
- i18n keys in all four locales (en/ar/fa/tr) and a new help topic
  (`web/help/{en,ar,fa,tr}/customer-order-tracking.md`) with regenerated
  screenshots.

## What the independent review found

Spawned an Opus subagent, worktree-isolated (`isolation: "worktree"`, per
ut-docs#386 — safe to revert-then-restore a file for TDD re-verification
without racing a stop-hook-forced commit on a shared checkout), briefed
with the full diff, the security angle this surface specifically needs
(new anonymous/unauthenticated attack surface), and told to run everything
itself rather than trust the report handed to it. Verdict: **safe to merge
with fixes** — one real blocker, one required test-infra fix, one
product-copy defect fixed at review, several nits.

1. **Blocker, fixed.** Migration 058 added `sales.tracking_token` but never
   mirrored it onto `sales_archive`, and `ResetTransactionHistory`'s `sales`
   entry in `internal/data/reset_archive_repo.go` copies an **explicit
   column list** — so "Settings → Data → Clear transaction history" silently
   drops the token on archive, and a restored batch comes back with
   `tracking_token = NULL`, permanently dead customer QR links. This is
   exactly the class of miss two earlier migrations were independently
   caught on (055, and 056's own header: *"Mirror it onto sales_archive in
   the same migration this time, rather than leaving [a follow-up]"*), and
   contradicts ADR-0042's "destroys nothing." Migration 058 was unreleased
   (`origin/main` tops out at 057), so fixing it in-place is correct, not an
   append-only violation. Fixed: `sales_archive` gets the column too (no
   unique index — 040's own stated archive relaxation), the column added to
   `reset_archive_repo.go`'s copy list, a new
   `TestResetThenRestoreRoundTrip_SaleTrackingToken` pinning the round-trip
   (mirroring the existing `_SaleTableID` test), and `rewindTracking058`
   (the migration-replay test helper) updated to also undo the archive
   column.
2. **Required fix, surfaced by #1.** `internal/pages/ui_smoke_test.go`'s
   hand-maintained `seedForPages` fixture schema — which that file's own
   comments say must never drift from production — was missing
   `tracking_token` on both `sales` and `sales_archive`. Effect in the
   original diff: every `internal/pages` test using that fixture hit a
   swallowed `no such column` inside the checkout's best-effort QR path, so
   tests stayed green while exercising an impossible schema. Fixed on both
   tables.
3. **Copy defect, fixed.** The tracking page shipped a line —
   *"Want to track orders anywhere? Install the Universal Till app."* — that
   is factually wrong: per `android/README.md` the Android app **is the
   till itself** (the same Go server embedded in a WebView), not an
   order-tracking companion app. A customer who installs it gets a
   point-of-sale, not order tracking. Removed the line, its locale key in
   all four files, and the test assertion that pinned it
   (`order_tracking_test.go`) — sitting directly under the page's own
   "status only, no other data" disclosure made the mismatch worse, not
   better. This was a factual-accuracy fix, not a business/pricing call, so
   made directly rather than escalated.
4. **Nits, fixed.** `.tracking-fineprint`'s CSS variable fallback used an
   off-palette color (`#6b7280`; the real design token is `#64748b`,
   `app.css:16`) — corrected. `.order-confirmation-tracking-url` (the
   plain-text fallback URL under the QR) had no CSS rule anywhere, so a long
   LAN URL + 32-hex token had no guaranteed wrap point inside the kiosk
   modal — added a scoped `overflow-wrap: anywhere` inline (this partial has
   no stylesheet of its own, matching `receipt.html`'s own layout-only
   `<style>` convention at smaller scale).
5. **Nits, not fixed (recorded, not blocking).** `TrackedOrder.CreatedAt` is
   selected and populated but never rendered — dead field, harmless. The
   6s→20s auto-return quadruples the kiosk's idle time between customers at
   peak — defensible (documented in the help topic, "Done" exits
   immediately) but worth a product-owner nod if queue throughput becomes a
   complaint.

## Independent TDD re-verification (by the review subagent, in its own worktree)

`TestOrderTracking_TerminalPastCutoffExpires`, sabotaging
`orderTrackingVisible` to always `return true`:

```
BEFORE (unmodified):  --- PASS: TestOrderTracking_TerminalPastCutoffExpires (0.14s)
SABOTAGED:             --- FAIL: TestOrderTracking_TerminalPastCutoffExpires (0.13s)
    want 404 for a 3h-old collected order, got 200: <status="collected"/"Collected"/"Last updated: …">
RESTORED:              --- PASS: TestOrderTracking_TerminalPastCutoffExpires (0.14s)
```

And its own new archive round-trip test, by stashing just the production
fix (the `sales_archive` column):

```
WITH FIX:        --- PASS: TestResetThenRestoreRoundTrip_SaleTrackingToken (0.15s)
FIX REVERTED:    --- FAIL: reset_test.go:338: read archived tracking_token: SQL logic error: no such column: tracking_token (1)
FIX RESTORED:    --- PASS
```

Both are real behavioural failures with meaningful messages, not compile
errors — proof the tests actually exercise the thing they claim to.

## What was verified beyond automated tests (Tester phase, before Review)

- **Real driven end-to-end run**, not just handler-level httptest: built the
  binary, ran it against a real seeded item, drove the actual self-order
  checkout through headless Chromium end to end (`/self-order/shop` → tap
  item → checkout → pick "Card"), captured the real confirmation screen
  with its live QR, extracted the real persisted token from the database,
  loaded `/o/{token}` for real. Drove real status transitions through
  `preparing` → `ready` → `collected` via direct DB writes (the
  write-path itself, `ApplyOrderStatus`, already has its own dedicated
  unit/handler coverage from #526) and watched the live poll fragment
  update correctly at each step; confirmed `hx-trigger` drops exactly on
  the terminal write, and that a 3h-backdated collected order 404s on the
  full page while its fragment degrades to the static expired state with no
  re-armed poll.
- Confirmed `/o/{token}` and `/o/{token}/status` are reachable through the
  **real** `auth.Middleware` with a completely fresh browser context (zero
  cookies), while `/orders` (the operator board) correctly redirects to
  `/login` in the same run.
- **Visual review**, not just text assertions: screenshotted the
  confirmation-with-QR modal, the tracking page in English (light theme),
  the tracking page in Farsi with `?lang=fa` (confirmed `dir="rtl"` on
  `<html>`, correct right-to-left text flow, no literal left/right leakage
  — the CSS only uses `width`/`text-overflow`/`margin-block-start`), and
  both the not-found state (fresh English) and the cookie-carried Farsi
  not-found state (both correctly localized, confirming `ResolveLocale`'s
  cookie fallback works as designed, not a bug). All visually clean — no
  overlap, cut-off, or misalignment.
- Confirmed the token is a real 128-bit `crypto/rand` value (32 lowercase
  hex chars), independently regenerated and distinct per sale, never equal
  to or derivable from the receipt number.
- Diffed the "unrelated" screenshot regenerations (`alerts.png`,
  `translations.png`, `users.png`, `sell.png` across locales) byte-level
  against their pre-diff versions: the only difference in each is a
  wall-clock timestamp baked into a live "Recent problems" log line the app
  renders — genuine `make docs-shots` run-to-run noise, unrelated to this
  change, not a regression (the already-known `designer.png` nondeterminism
  is separately documented).
- **Found, independently confirmed by empirical repro (not just read), and
  filed separately**: `receipt.html:142`'s `TSESignature.QRDataURI` is a
  plain `string` rendered into an `<img src>` — the exact same
  `html/template` URL-sanitizer trap this diff's own QR code had to type
  around with `template.URL`. Reproduced in isolation
  (`template.Must(...).Execute` with a plain-string data: URI →
  `<img src="#ZgotmplZ">`), meaning the TSE fiscal evidence QR on
  German-market receipts likely renders as a broken image today. Filed as
  **ut-docs#906** (p1/compliance/pilot:germany) — deliberately **not**
  fixed here: untouched file, pre-existing, unrelated to this diff's scope.
- `gofmt -l .` clean; `go build ./...` clean; `go vet ./...` clean.
- Fresh (`-count=1`) `go test ./...` — full suite green, zero failures,
  after the review's fixes were merged in.
- All 16 CI-blocking guards from `.github/workflows/ci.yml`'s `build` job
  re-run fresh, twice (once before the review's fixes, once after,
  following `make docs-shots` to refresh the manual's screenshot manifest
  for the `tracking.promo` removal) — all pass both times.
- No real client/shop name anywhere in test/seed data ("Task Runner"-style
  generic fixtures throughout — "Flat White", "R-0001", `example.com`); no
  secret-shaped literal in the diff.

## Security review (the sharpest angle — a new anonymous, internet-shaped surface)

- Token minting and lookup are fully parameterized SQL — zero string
  concatenation.
- Unknown, malformed, and empty tokens are indistinguishable at every
  layer: same `found=false, nil` from the repo, same 404/expired response
  from both handlers. No timing-relevant branch on partial matches (a
  single indexed equality lookup).
- `TrackedOrder` carries only `ReceiptNo`, `Status`, `StatusUpdatedAt`,
  `CreatedAt` — no basket lines, no totals, no payment data, no
  customer/staff identity. Verified by reading the actual rendered page
  body during the driven run, not by trusting the struct definition alone.
- The `/o/` exempt-prefix addition in `middleware.go` is
  `strings.HasPrefix(path, "/o/")` — confirmed `/o`, `/o-not-really`,
  `/order`, and `/orders` all correctly fail the match (test-pinned in
  `middleware_test.go`, and independently re-read as source, not trusted
  from the test name alone).
- Neither `order_tracking.go` nor the `self_order_shop.go` changes reference
  `common.Deps.Engine` or `KioskEngine` anywhere — grepped and read line by
  line. All reads go through `data.NewPOSRepo(d.Db)`.
- No token appears in any log output (neither new file logs anything), and
  the page has no outbound links, so no Referer-based leak vector.
- All raw SQL for this feature lives only in `internal/data`/
  `internal/db/migrations` — read `order_tracking.go` and
  `self_order_shop.go` line by line to confirm, not just trusted
  `guard-data-access.sh`'s pass.

## Verdict

**Safe to merge.** One real blocker (archive-mirror data loss) found and
fixed by the independent review, one required test-fixture fix that
surfaced alongside it, one factual-copy defect fixed, a couple of small
CSS/nit fixes. The tracking design itself — token generation, exposure
surface, expiry, auth-exemption boundary — was sound going into review, and
stayed sound coming out.

## Explicitly deferred / follow-up cards filed

- **ut-docs#906** — pre-existing, unrelated `receipt.html` TSE QR rendering
  bug (`#ZgotmplZ`), found while building/reviewing this card's own QR code.
- **ut-docs#907** — off-LAN/mobile-data reachability for the tracking link.
- **ut-docs#908** — cashier-lane (non-self-order) receipt tracking QR.
- **ut-docs#909** — browser push notifications on the tracking page.
- `ar`/`fa`/`tr` locale values for this feature were translated directly by
  the implementing agent (the homelab NAS Ollama endpoint at
  `192.168.1.231:11434` is unreachable from this cloud sandbox's network) —
  every key exists in every locale (guard-i18n passes), but these want a
  native-speaker spot-check per the project's normal translation-quality
  bar, and the `ut-plugin-language-{de,es}` packs need the new keys as a
  standard `lang-pack-drift` follow-up.
