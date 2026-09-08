# ut-docs#1765 — TR ÖKC: kiosk actor breaks the fiscal audit marker

Date: 2026-09-08
Branch: `fix/1765-okc-kiosk-actor-audit-marker-test`
Reviewer: independent subagent, fresh context, Sonnet (`complexity:easy`
per the scrum-master skill's model routing).

## Why this card existed

Split from ut-docs#1750 / universal-till#750. The independent review of that
merge (`docs/code-reviews/2026-09-07-turkey-okc-merge-and-country-gate-1750.md`,
"Explicitly NOT fixed" finding 4) claimed: *"the kiosk actor breaks the audit
marker: `audit_log.actor_id` FKs to `users(id)` and self-order passes the
literal `"kiosk"`, so the flag flips with the marker silently dropped."*
Acceptance criteria: (1) a kiosk-originated sale records an actor that is
truthful and distinguishable, never blank or borrowed; (2) a test covers a
kiosk sale reaching the fiscal path.

## What investigation found: the claim does not reproduce on current `main`

- `internal/db/migrations/001_init.sql` line 1140 seeds
  `('kiosk', 'kiosk', 'Self-order kiosk', 'cashier', NULL, 1)` into `users`
  **unconditionally**, on every till — not a demo/opt-in seed. `actor_id =
  'kiosk'` therefore satisfies `audit_log`'s `FOREIGN KEY (actor_id)
  REFERENCES users (id)` without any special handling.
- `internal/pages/self_order_shop.go`'s kiosk checkout handler already
  builds `SaleInput{..., ActorID: "kiosk"}` and calls `completeTender(...,
  "kiosk")` — a literal, never a value derived from the anonymous request.
- `completeTender` (`internal/pages/pos_api.go`) threads that `actorID`
  parameter unmodified into `recordFiscalDeviceEvidence`
  (`internal/pages/fiscal_device_hook.go`), which writes the
  `fiscal_device_confirmed` audit marker under that same actor.
- Empirically verified (throwaway test, since removed) against the real
  migrated schema: `INSERT INTO audit_log (..., actor_id, ...) VALUES (...,
  'kiosk', ...)` succeeds, and a full `recordFiscalDeviceEvidence(ctx, d,
  repo, "sale-1", "kiosk", ev)` call records both the settings flag and the
  audit marker correctly.

No existing test exercised this combination: every test that reaches
`recordFiscalDeviceEvidence`'s audit-marker path (`fiscal_device_hook_test.go`,
`fiscal_device_page_test.go`) manually seeds a `"cashier"` actor instead —
so the specific kiosk-actor path this card is about had zero coverage,
consistent with the original finding never having been checked against the
actual seed data before being written down.

## Change

Test-only, no production code touched. Added
`TestSelfOrderShop_CheckoutViaFiscalDevice_RecordsAuditMarkerUnderKioskActor`
to `internal/pages/self_order_shop_test.go`: drives a real kiosk checkout
(`POST /api/self-order/scan` → `POST /api/self-order/checkout`) through a
fake `tax-tr` OKC plugin (fixture rows mirror `pos_api_test.go`'s
`TestTenderHandler_OKCPluginReceivesBasketDetail` byte-for-byte, plus the
`payment_methods` row that surface additionally requires), then asserts:

1. `HasAuditEntry(ctx, "fiscal_device", saleID, "fiscal_device_confirmed")`
   is true (the marker is not silently dropped), and
2. the audit row's `actor_id` column is exactly `"kiosk"` (not NULL, not a
   borrowed identity) — satisfying AC 1 and AC 2 as a permanent regression
   guard.

**TDD claim independently re-verified**: the reviewer (and, earlier,
the author) temporarily changed the production call site to pass `""`
instead of `"kiosk"`, reran the new test, and got a clear failure:
`fiscal_device_confirmed marker's actor_id = {String: Valid:false}, want
the truthful, distinguishable "kiosk" actor — never blank or a borrowed
user`. The file was then restored and confirmed byte-for-byte identical via
`git diff`.

## What the independent review found

Verdict: **SAFE TO MERGE**, no blockers.

1. **Verified** — every factual claim above, independently, by reading the
   cited files directly rather than trusting the author's summary.
2. **Verified** — the new test exercises the real HTTP handler path (not a
   shortcut), and correctly distinguishes three outcomes: marker absent,
   marker present with a wrong/blank actor, marker present with the
   correct actor.
3. **Verified, mutation-tested** — reproduced the same actorID="" revert
   independently and confirmed the same failure and a clean revert.
4. **Verified** — fixture rows match the established OKC-plugin test
   pattern; no cross-test pollution (`bus.ResetSubscribers()` before and
   via `t.Cleanup`, matching convention).
5. **Verified** — no repository-pattern/money/offline-first/i18n
   violations; raw SQL is confined to the test file (guard-exempted).
6. **Non-blocking observation** — the test's own comment (inherited from
   `self_order_shop.go:363`) cites a `018_kiosk_user.sql` file that no
   longer exists post-squash into `001_init.sql`; pre-existing elsewhere,
   not introduced here, not fixed in this PR.
7. **Verified, no gap** — found no evidence any production fix is still
   needed elsewhere; the original finding 4 appears to have been incorrect
   (or already resolved) rather than partially fixed.

## Verification

- `gofmt -l internal/pages/self_order_shop_test.go` — clean.
- `go build ./...` — clean.
- `go vet ./internal/pages/...` — clean.
- `go test ./internal/pages/... -count=1` — all pass (`pages`,
  `pages/catalog`, `pages/common`).
- `go test ./internal/pages/ -run TestSelfOrderShop -race -v` — all 28
  tests in the file pass, including the new one, no data races.
- `go test ./...` (full repo) — all pass.
- `golangci-lint run ./internal/pages/...` — 0 issues.
- `scripts/ci/guard-data-access.sh` — pass.
- `scripts/ci/guard-kiosk-engine.sh` — pass.
