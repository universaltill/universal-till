# 2026-08-28 — Off-LAN order tracking: cloud relay via the till-initiated push (ut-docs#907)

Combined record for a **three-repo** change. `universal-till` is the
review-record home per this repo's `CLAUDE.md`; the other two repos'
commits are cross-referenced here rather than duplicated.

| Repo | Branch | Commit reviewed |
| --- | --- | --- |
| `ut-docs` | `feat/907-order-tracking-cloud-relay-adr` | `ded6448` — ADR-0070 |
| `universal-till` | `feat/907-order-tracking-cloud-relay` | `f3d30f6d` (+ review fix) |
| `ut-cloud` | `feat/907-order-tracking-cloud-relay` | `aac3781` (+ review fix) |

## What shipped

ut-docs#527 gave customers a QR order-tracking page, but the QR encodes the
till's **LAN** address (`advertisableHost`), so a customer on mobile data
can't reach it. ADR-0018 decision 2 ("sync is till-initiated, never inbound")
forbids the obvious fix — nothing may dial into a shop.

[ADR-0070](../../../ut-docs/adr/0070-order-tracking-cloud-relay-via-till-initiated-push.md)
settles the wire mechanism as a **cloud-side read-through cache fed by the
existing 2-minute push tick**: no new outbound call, no new schedule, no new
direction of traffic. The customer-facing cloud HTML page and the QR/link UX
are explicitly deferred to a follow-up card; this slice is the endpoint pair
only.

**universal-till** (`f3d30f6d`)

- `internal/pos/order_tracking.go` (new): the ONE liveness rule —
  `OrderTrackingExpiry` (2h), `OrderTrackingTerminal`, `OrderTrackingVisible`
  — lifted out of `internal/pages/order_tracking.go` so `internal/cloudsync`
  can apply it too (`pages` wires `cloudsync`, so importing it back is a
  cycle). `internal/pages` now delegates; behaviour unchanged.
- `internal/data/order_tracking_repo.go`: `ListLiveTrackedOrders`, taking the
  liveness rule as a **callback** (same shape and same cycle reason as
  `ApplyOrderStatus`'s `allowed`), deterministically ordered so the caller's
  content hash never sees phantom churn.
- `internal/cloudsync/cloudsync.go`: `pushOrderTrackingIfChanged`, hash-gated
  in `settings` exactly like `pushSnapshotIfChanged`, called from `Tick`
  inside the existing primary-only block.

**ut-cloud** (`aac3781`)

- New ent entity `OrderTrackingSnapshot` (token unique + lookup key,
  `store_external_id` indexed, `receipt_no`, `status`, `status_updated_at` as
  RFC3339 **text** verbatim, `TimeMixin`, optional `merchant` edge per
  ADR-0046 §1) — auto-migrate, no hand-written SQL.
- `POST /api/v1/stores/order-tracking-snapshot` (store-bearer authenticated,
  `catalogSnapshot`'s pattern): replace-on-push — delete this store's rows
  whose token is not in the incoming array, then update-then-create each row.
- `GET /api/v1/order-tracking/{token}`: public, unauthenticated, by-token
  only, cross-store.

## Independent review

Opus, per this card's `complexity:hard` routing (Dev built at the Fable
tier). Reviewed in **detached `git worktree`s** off the pushed branches so
the orchestrator's own checkouts were never touched; both gates re-run from
scratch there, and both repos' TDD claims re-verified by hand.

**Verdict: safe to merge in all three repos**, after the two fixes below.

### The headline check: cross-repo liveness-rule duplication — VERIFIED CONSISTENT

The design knowingly duplicates the liveness rule across the two repos (the
cloud read must age a token out the same way the LAN page does, and there is
no shared package). A silent drift here is the exact bug this tradeoff
accepts, so it was checked term by term, not skimmed:

| | `universal-till` `internal/pos/order_tracking.go` | `ut-cloud` `internal/httpapi/handlers/stores.go` |
| --- | --- | --- |
| expiry | `OrderTrackingExpiry = 2 * time.Hour` | `orderTrackingExpiry = 2 * time.Hour` |
| terminal set | `OrderStatusCollected`/`OrderStatusCancelled`, which are literally `"collected"`/`"cancelled"` (`internal/pos/order_status.go:18,21`) | `"collected"`/`"cancelled"` |
| parse | `time.Parse(time.RFC3339, statusUpdatedAt)` | `time.Parse(time.RFC3339, snap.StatusUpdatedAt)` |
| parse error | `return false` (fail closed) | `perr != nil` → 404 (fail closed) |
| boundary | visible while `now.Sub(at) <= expiry` | gone when `now.Sub(at) > expiry` — exact complement, so the `== 2h` instant is visible on both sides |
| clock | callers pass `time.Now().UTC()` | `time.Now().UTC()` |

Same duration, same status strings, same fail-closed-on-unparseable
behaviour, same inclusive boundary. **No drift.** Both files carry a
"keep in sync with the other repo" comment naming the sibling.

Non-blocking follow-up recorded below: there is no *mechanical* guard on
this, unlike the `signing.CanonicalManifest` ↔ `plugins.Manifest` contract
guard that already exists in `ut-cloud`'s `scripts/ci/verify.sh`. Comments
are not CI.

### Findings and disposition

1. **BLOCKING — the "public" read endpoint was not public
   (`ut-cloud`). Fixed.** `orderTrackingByToken` was mounted correctly but
   never added to `internal/httpapi/router/router.go`'s
   `unauthenticatedPaths`, which is the exact list the file's own comment
   warns about: *"Registering a route in the mux is not enough on its own
   when auth is enabled — it also has to be named here, or it 401s for
   anyone who can't present a bearer token."* This is the ut-docs#506
   `/sitemap.xml` bug recurring, and it lands on the one caller that can
   never authenticate: a customer's phone off the shop's WiFi. It is latent
   rather than live only because `AUTH_DISABLED` still defaults to `true`
   (`internal/config/config.go:248`) and no deployment sets it — i.e. the
   endpoint works today and silently 401s the day auth is switched on,
   which is the worst possible failure timing.
   **Fixed**: added `"/api/v1/order-tracking/"` (prefix entry — the token is
   a path segment) with a comment stating why, plus two cases in
   `TestUISkipperSkipsWholeUITree` pinning that the READ skips and the
   store-authenticated WRITE does not. Verified as a real test: reverting
   the `router.go` line alone turns it red, restoring turns it green.
2. **BLOCKING (correctness of a stated justification) — the primary-only
   gate's in-code reasoning was factually wrong (`universal-till`).
   Fixed.** The new call site claimed: *"sales made on any till journal to
   the primary (ADR-0011 §2), so the primary's tracked-order set IS the
   shop's set."* ADR-0011 §2 does say sales journal to the primary, but the
   journal wire (`data.SaleDetail`, `internal/pages/sync_sales.go`) carries
   **neither `tracking_token` nor `order_status`**, and `applyJournalEntry`
   rebuilds the sale through `pos.SaleInput`, which has no field for either.
   There is also no LAN sync of order status at all. So the primary's
   `sales` rows for replica-originated sales have `tracking_token IS NULL`
   and are filtered out by `ListLiveTrackedOrders`' `WHERE` clause: a
   self-order sale taken on a **replica** is never relayed.
   The *gate itself is right* — but for a different reason: the cloud
   replaces a store's whole row set per push and all of a shop's tills share
   one `StoreID`, so a second pusher would delete the first's rows every
   tick. Exactly one till per shop may push, and the primary is it.
   **Fixed**: rewrote the comment to state the real (cloud-storage) reason
   and to name the replica gap as a known, deliberately-not-covered
   consequence rather than assuming it away. No behaviour change — the code
   was already doing the right thing for the wrong stated reason, and an
   unchallenged wrong reason is what the next person builds on.
3. **Replace-on-push is not transactional (`ut-cloud`) — accepted, not
   fixed.** `delete(store, token NOT IN …)` then a per-row update/create
   loop, outside a transaction. Two concurrent pushes *for the same store*
   could interleave such that one push's delete removes a row the other just
   created. Judged non-blocking: there is exactly one pusher per store
   (`cloudsync.Tick` runs sequentially in one goroutine, and `Tick` has no
   other production caller), so the interleaving needs a retry storm to
   occur at all. Worth knowing that it would **not** self-heal on the next
   tick if it did happen: the till's `cloudsync.order_tracking_hash` gate
   only re-pushes when the *content* changes, not when the cloud is wrong.
   A single `WithTx` around both steps would close it cheaply — recommended
   as a follow-up, out of scope for a targeted review fix.
4. **Cross-store token hijack — checked, does not exist.** The delete is
   `StoreExternalIDEQ(storeID)`-scoped, so store B can only ever delete its
   own rows. The update predicate is `TokenEQ AND StoreExternalIDEQ`, so B
   cannot overwrite A's row; the create fallback then hits `token`'s global
   unique index and errors. There *is* a TOCTOU window between the update
   returning 0 and the create — but it fails **closed** (unique violation →
   500), never open, and the only party harmed is the pusher that tried it,
   whose own push then fails every tick until it stops sending that token.
   Adversarial-only anyway: tokens are 128 bits of `crypto/rand`.
5. **Anonymous-surface data minimisation — verified, leaks nothing new.**
   The read returns exactly `{token, receipt_no, status, status_updated_at,
   poll}`. Against `data.TrackedOrder` (`ReceiptNo`, `Status`,
   `StatusUpdatedAt`, `CreatedAt`) that is a strict **subset** — `CreatedAt`
   isn't even echoed. No store name, no `store_external_id`, no merchant/
   tenant identifier, no operator attribution (the deliberate difference
   from `writeOrderStatusFragment`), no basket, no totals, no payment data.
   Unknown, malformed, not-yet-pushed and aged-out all return the identical
   404 body — the till page's "not-found and expired must be
   indistinguishable" invariant, and the test asserts byte equality of the
   two response bodies, not just equal status codes.
6. **ADR-0046 merchant edge does not scope the public read — verified.**
   `OrderTrackingSnapshot` is deliberately absent from
   `internal/data/tenant_scope.go`'s interceptor registrations, so the
   fail-closed tenant scoping never applies to it, and
   `orderTrackingByToken`'s query is `Where(ordertrackingsnapshot.TokenEQ(
   token)).Only(ctx)` with **no** merchant/tenant predicate. Cross-tenant by
   design, as decision 3 requires. The edge is set on create only, as the
   tenant FK.
7. **Write-side input validation — no bypass found.** Body capped at 1 MiB
   (`http.MaxBytesReader`), `maxOrders = 2000` **rejects** rather than
   truncates (truncating would silently delete the dropped tokens via
   replace-on-push — the right call, and the comment says so),
   `validTrackingToken` bounds length 8..128 and restricts to
   `[A-Za-z0-9_-]` (rejects `../`, NULs, and anything path- or
   injection-shaped), and `receipt_no`/`status`/`status_updated_at` are
   length-capped. Validation runs over the **whole** batch before any write,
   so a bad row can't leave a half-applied replace. `store_id` is checked and
   `authorizeStore`d before any DB work.
8. **Offline-first — confirmed clean.** `pushOrderTrackingIfChanged` is
   called only from `cloudsync.Tick`, which runs only in the detached
   goroutine `cloudsync.Start` spawns; `Tick` has no other production
   caller. Its error is logged at warn and swallowed, exactly like
   `pushSnapshotIfChanged`'s, so it cannot fail a tick, let alone a sale.
   Nothing on the checkout path calls it. The whole read is one `SELECT`
   against the local SQLite. Cloud unreachable → the LAN page is unaffected.
9. **Unbounded scan + an eventual payload cliff (`universal-till`) —
   follow-up, not fixed.** `ListLiveTrackedOrders` selects **every** sale
   that ever held a tracking token (`WHERE tracking_token IS NOT NULL`) and
   filters liveness in Go. Since `OrderTrackingVisible` treats every
   non-terminal status — including the untracked `""` — as live forever, a
   self-order sale that never reaches collected/cancelled stays in the
   payload permanently. Two consequences that only bite at long horizons:
   the query and payload grow without bound, and once the live set passes
   `maxOrders = 2000` the cloud 400s and **all** order-tracking relay for
   that shop stops (the till retries and warns every tick, forever). Not
   blocking for v1 — tokens are minted only by the kiosk confirmation screen
   (`orderTrackingQRView`, the sole `EnsureOrderTrackingToken` caller), so
   the population is small and mostly does reach a terminal status — but it
   should get a bound (a `created_at` floor, or pushing the liveness
   predicate into SQL) before this sees a year of real kiosk traffic.
10. **`/api/v1/stores/*` is itself missing from `unauthenticatedPaths` —
    pre-existing, out of scope, flagged.** The whole store-bearer API family
    (`sync`, `catalog-snapshot`, `directives/result`, issue reports, fiscal
    TSE) would 401 at the edge if `AUTH_DISABLED` were flipped to `false`,
    because those endpoints authenticate with an opaque store token, not a
    JWT. Deliberately **not** touched here: it predates this card and
    resolving it is a security-posture decision, not a review fix. Worth its
    own card before auth is ever enabled.
11. **No mechanical drift guard on the duplicated liveness rule — recommended
    follow-up.** `ut-cloud` already has the precedent
    (`internal/signing.CanonicalManifest` ↔ the POS `plugins.Manifest`
    struct, guarded by a cross-repo test in `scripts/ci/verify.sh`). The
    2h/`collected`/`cancelled`/fail-closed triple deserves the same
    treatment; today only the two comments hold it together.

### TDD re-verified independently (both repos)

- **`universal-till`** — `TestListLiveTrackedOrders`: replaced the
  `if visible(o.TrackedOrder)` filter with an unconditional append →
  `--- FAIL … got 2 rows (0 callback calls)`; restored → `ok`.
- **`ut-cloud`** — `TestOrderTrackingSnapshotReplaceOnPush`: inverted the
  delete predicate from `TokenNotIn` to `TokenIn` → `--- FAIL … rows after
  replace = 2, want 1`; restored → `ok`.

Both are real tests that fail for the right reason, not false passes.
The reviewer-added router test was verified the same way (see finding 1).

## Gate results

**`universal-till`** (worktree off `f3d30f6d`, re-run after the fix)

| Gate | Result |
| --- | --- |
| `gofmt -l .` | clean (no output) |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test -count=1 ./internal/pos/... ./internal/data/... ./internal/pages/... ./internal/cloudsync/...` | all `ok` (pos 14.1s, data 133.2s, pages 153.8s, pages/catalog 0.8s, pages/common 9.4s, cloudsync 8.3s) |
| `scripts/ci/guard-data-access.sh` | ✓ no inline SQL outside `internal/data` / `internal/db` |
| `scripts/ci/guard-kiosk-engine.sh` | ✓ no self-order handler references the cashier `Engine` |
| `scripts/ci/guard-i18n.sh` | ✓ 1301 template keys resolve; all locales match `en.json`; no hardcoded strings |

**`ut-cloud`** (worktree off `aac3781`, re-run after the fix)

`bash scripts/ci/verify.sh` → **exit 0**: gofmt clean, `go vet` clean,
`golangci-lint` (data-access/depguard + `tagliatelle`) clean, full test
suite `ok` (including `internal/httpapi/handlers` and
`internal/httpapi/router`), contract governance check clean,
`guard-runner-labels` pass. `gosec`/`trivy` skipped — not installed in this
environment (unchanged from `main`).

No `en.json` keys were touched by this slice, so no
`ut-plugin-language-{de,es}` follow-up and no `lang-pack-drift` exposure —
ADR-0070 states this explicitly, and it holds: the deferred cloud HTML page
is where locale keys will land.

No user manual change: the deferred follow-up card owns the only
user-visible surface (the cloud page + QR/link UX). This slice adds no page
route under `internal/pages/**`, so `guard-help-topics.sh`'s coverage check
is unaffected.

## Safe-to-merge verdict

**Yes — all three repos.**

- `ut-docs` `ded6448` — ADR-0070 is internally consistent, correctly scopes
  itself to the wire mechanism, and does not weaken ADR-0018 decision 2:
  this is an additive field on an existing outbound push plus one stateless
  read endpoint. No new inbound channel. Merge as-is.
- `universal-till` — merge after the finding-2 comment fix on this branch.
  The single most important cross-repo risk (liveness-rule drift) is
  verified consistent; offline-first is untouched.
- `ut-cloud` — merge after the finding-1 auth-skip fix on this branch.
  Without it the ADR's decision 3 ("public, unauthenticated") is not
  actually true of the shipped route the moment auth is enabled.

Findings 3, 9, 10 and 11 are recorded as follow-ups and are deliberately not
fixed here — none of them block this slice, and each is a scope expansion
(a transaction boundary, a query bound, a security-posture decision, and a
new cross-repo CI guard respectively).
