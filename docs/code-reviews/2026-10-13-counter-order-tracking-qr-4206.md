# Review — pay-at-counter kiosk orders get a tracking QR; tracking is Shop and up (ut-docs#4206)

**Shipped**
- A tracking QR is offered only when the till's cached plan allows `cloud_sync`, which means Shop and up (owner decision 2026-10-12; ADR-0070 amendment). This applies to both kiosk checkout paths. On Local no token is minted and no QR is shown. It is display only, never a sale gate.
- A pay-at-counter order mints its token at placement and carries it in the held payload (`BasketSnapshot.TrackingToken`, same lifecycle as `DisplayNo`). The counter confirmation shows the QR. On tender, the sale adopts the token (guarded `UPDATE … AND tracking_token IS NULL`, best-effort after commit).
- `/o/{token}` falls back to a held order carrying the token and shows "Waiting for payment at the counter" (`tracking.awaiting_payment`). That fallback is bounded to held rows written in the last 2 h. A `?w=` poll counter keeps the waiting state for up to 40 polls (10 min) while the cashier has the order resumed but not yet paid. Not-found and expired remain indistinguishable.
- A warning is logged whenever the QR is dropped; the token is never logged.
- Locale key in en/ar/fa/tr (de/es/pt via the pack PRs). The help topic is updated in 5 languages, including the cross-till limitation, and so is the README.

**Reviewer:** independent Fable subagent (code written by Opus 5.5), two rounds.
- Round 1 found two majors, both fixed:
  1. **Cross-till.** The link never follows a sale paid on another till, and a replica kiosk's stale held mirror showed "waiting" forever. Fixed by bounding the held lookup to 2 h, adding an honest help/README note, and moving the cross-till plumbing (token on the LAN sale journal, cloud page) to ut-docs#4238.
  2. **Stale docs-shots surface hash.** Refreshed with `update-docs-shots-surface-hash.sh`. The reviewer verified this is pixel-neutral: the topic's screenshot is the literal `/o/{token}` not-found page, and the kiosk confirmation is not screenshotted.
- Minors: German "Kasse"/"Theke" aligned to the pack's UI wording. Reloading the full page during the resume window shows not-found; accepted, because only the open page's poll carries the counter.
- Round 2 (fixes plus the ADR amendment) found no blockers. Its minors were applied: the ADR text covers cross-till and the 7-day grace; help step 5 now says an order unpaid for more than about 2 h stops showing until paid; the test uses the real timestamp layout.

**TDD re-verified by the reviewer** (revert → fail for the right reason → restore → pass):
- `pos_api.go` adoption.
- The `self_order_shop.go` plan gate and token minting.
- The `order_tracking.go` held fallback.
- Mutations of the `?w=` grace (disabled, off-by-one) and of the 2 h bound (tautology): each is caught.

**Verified beyond unit tests**
- e2e `kiosk-counter-order-held-2703` and `self-order-tracking-viewport-scale-1175`: 4/4 pass.
- Driven run on a real till with a held order carrying a token. `/o/{token}` showed "Order number: C-7 / Waiting for payment at the counter" with the `?w=1` poll. Looked at it at 360×740 in en (light) and fa (RTL): it wraps, there is no horizontal scroll, and `dir=rtl` is correct. de falls back to English until the pack ships.
- Not looked at: the kiosk confirmation with a QR on a real Shop-plan till. The template block is unchanged (#527).

**Gate**
- Passed: gofmt clean; `go build`, `go vet` and `golangci-lint` (0 issues) ok; `go test ./...` ok for every package except the untouched `internal/plugins`; every `ci.yml` guard passes except the two noted below.
- `internal/plugins` hit the local default 600 s timeout. The package is untouched, and CI runs it with `-timeout 20m`.
- `guard-deadcode-baseline.sh` cannot run here: the deadcode binary was built with go1.26 against a go1.27 module.
- `guard-shellcheck-version.sh` cannot run here: shellcheck is not installed. No shell scripts changed.

**Deferred:** ut-docs#4238 covers the cloud-hosted page, the QR pointing at it, held orders in the push, and the token on the LAN sale journal.

**Verdict:** safe to merge.
