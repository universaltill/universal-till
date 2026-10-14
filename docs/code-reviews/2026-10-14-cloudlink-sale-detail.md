# Review — till answers `sale_detail_request` on the cloud link (ut-docs#4150)

- **Date:** 2026-10-14
- **Card:** universaltill/ut-docs#4150 (ADR-0155 task 1; part of #3933)
- **Branch:** `feat/4150-cloudlink-sale-detail`
- **Built by:** Opus 5.5 (pipeline lane `lane:cloud-24`); **reviewed by:** Fable (independent, different model, fresh worktree)

## What shipped

- `internal/fleetlink`: `SessionHandlers.Requests` — the cloud-link
  `Session` can now answer declared inbound request types (until now its
  `handler` returned nil); `fleetlink.Reply{Type, Payload}` lets a handler
  name a reply type that differs from the request's
  (`sale_detail_request` → `sale_detail`). A plain result keeps ADR-0114's
  "reply has the request's type" — the LAN link is unchanged.
- `internal/cloudlink`: hello carries `caps: ["sale_detail"]` (only when a
  lookup is wired); `RequestTimeout` set to 3 s (ADR-0155 §4);
  `sale_detail.go` — the §3 allow-list wire types, the handler
  (`bad_message` → `rate_limited` 5/s burst 10 → lookup → `not_found` /
  `too_large`), and the trim: longest prefix of lines whose **encoded
  envelope** (64-byte allowance each for `id`/`reply_to`) is ≤ 15 KiB, the
  rest counted in `lines_omitted`; header and payments never dropped.
- `internal/pages/cloud_link.go`: `cloudLinkSaleDetailOf` maps
  `data.SaleDetail` field by field (never marshalled); lookup =
  `GetSaleDetailByID` + new `POSRepo.SaleProvenance` (sales.till_id,
  register name, cashier display name — one query, LEFT JOINs, SQL in
  `internal/data`).
- Return sign convention: header totals negative (as in the `sale`
  summary), lines and payments the stored positive magnitudes —
  documented on `cloudlink.SaleDetail` for the my. consumer (#4152).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| M1 | minor | Return: header totals negated, lines/payments not — ambiguous for the my. consumer | Kept (ADR §3 "a return's totals are negative"); the convention is now spelled out on `cloudlink.SaleDetail`'s doc comment so #4152 reads it there |
| M2 | minor | Rate-limit token taken before the request was validated — malformed requests burned the budget | **Fixed**: validate first; `TestAnswerSaleDetailCodes` now asserts every request inside the burst succeeds (fails on the old order: "request 10 inside the burst: … rate_limited") |
| N1 | nit | `reply_to` isn't visible to the handler; the 64-byte allowance + 1 KiB headroom covers ids up to ~740 B (cloud mints 23 B) | Accepted, documented on `saleDetailIDAllowance` |
| N2 | nit | Binary-search monotonicity undocumented | **Fixed**: comment states the ≥ ~170 B/line bound |
| N3 | nit | ADR §1 says "two codes of its own" but lists three | ADR text nit; code maps all three correctly |

No blockers, no majors. Allow-list/never-list compliance (incl. ADR-0127
card data), the SQL joins against `001_init.sql`, error codes and
`retryable` flags, and concurrency (token buckets under a mutex, handler on
fleetlink's goroutine under the in-flight cap and 3 s deadline) were all
checked by the reviewer.

## Verified beyond the unit tests

- **TDD re-verified by the reviewer** in an isolated worktree: (a) making
  the trim never drop lines fails `TestFitSaleDetailTrimsLinesToTheFrameBudget`
  (frame 134 723 B > 15 360), `…RefusesWhenPaymentsAloneAreTooLarge`,
  `TestAnswerSaleDetailCodes` and the end-to-end `TestSaleDetailRequestOverTheLink`
  (fleetlink silently drops the oversized reply: "no reply to c-q2");
  (b) leaking `reference` / `cashier_id` fails `TestCloudLinkSaleDetailNeverList`
  and `TestCloudLinkSaleDetailMapping`. Restored → green.
- Reviewer probe (not committed): 600 lines with quote/HTML/é/CJK/U+2028
  names and tiny float quantities → 34 lines kept, 566 omitted, frame
  14 952 B; fit predicate strictly monotone over all 601 prefixes.
- End to end over a real websocket against the fake cloud: hello carries
  the cap, reply is `sale_detail` with `reply_to`, a 500-line sale arrives
  under 15 KiB with `lines_omitted > 0`, unknown sale → `error{not_found}`.
- A replica sale applied through the real `applyJournal` on a migrated DB
  is answerable by its id with `till_id` provenance and empty register.
- Offline-first: nothing on the sale path calls into this; the read runs
  on the link's handler goroutine only.

## Gate

`gofmt -l .` clean; `go build ./...`; `go vet ./internal/...`;
`golangci-lint run ./...` 0 issues; `go test -race` on `internal/cloudlink`,
`internal/fleetlink`, `internal/data` green; `internal/pages` full `-race`
run green (one earlier full `-race` run failed on a test that was not
captured — a non-race run and a second `-race` run passed; the
cloud-link/journal subset is green; left to CI); every guard in `ci.yml`
green except `guard-deadcode-baseline.sh` (local tool built with go1.26 —
fails identically on `main`) and `guard-shellcheck-version.sh` (no
shellcheck in this container; no shell changes) — both run in CI.

## Not covered here

- No UI, i18n key, help topic or migration: the till shows nothing new.
  The user-facing surface is my. (#4152); the cloud side is #4151 — until
  it ships the cap is never exercised in production.
- 2+ cloud pods: ADR-0155 Consequences (ut-docs#2828's job).

**Verdict: safe to merge.**
