# Review record: till channel order actions (ut-docs#4768, slice D)

Branch `feat/4768-channel-order-actions`. Contract: `ut-docs/reference/contracts/channel-order-action.md`
1.0.1 + `channel-order-action-v1.json`; ADR-0169 §9. Slice C (read-only inbox) is
`2026-10-23-till-channel-orders-4768.md`. The "Online channels" report section is split to ut-docs#5427.

## What this adds

- Migration `096_channel_order_actions.sql`: the till's action outbox (till-minted `action_id`,
  order key, `base_revision`, staff id, shipment / cancel fields, `state` `pending|refused|noted|blocked`,
  result code, write-back). Not LAN-synced (`sync_admin_repo.go`); checksum pinned.
- `internal/data/channel_order_actions_repo.go`: `Enqueue` (action row + audit row, one transaction;
  re-checks the status machine; a double tap is a no-op), `SendableBatch` (oldest pending row per order,
  ≤ 50), applied/refused recording with `base_revision` rebase of the order's next row in the same
  transaction, block / dismiss. `ChannelOrderAllowedActions` mirrors contract rules 1–3 on the
  fulfilment rank.
- `internal/channelactions`: validator ported from ut-cloud `internal/channelorder/writeback.go`
  (vendored schema in `testdata`, byte-identical to ut-docs; a node-run test proves the Go validator is
  never looser than the schema's own patterns), wire types, `Run` (answer count / id order checked
  before anything is recorded; `stale` decided at each send), HTTP poster.
- `internal/cloudsync`: the upload runs in `tick()` on the main till after the check-in and before the
  directive loop (mutation-checked); an answer's `current` goes through the existing decoder →
  `ApplyChannelOrder` → `ChannelOrdersRepo.Apply` path, so the next `channel_order_update` of the same
  revision is ignored and stock moves only by applying a revision.
- Handlers `POST /channel-orders/{id}/actions`, `…/actions/{action_id}/dismiss`,
  `/channel-orders/blocked/dismiss`: same-origin, `reports` + `stock_management`, main till, enrolled;
  denied in demo mode. Inline `<details>` Ship / Cancel panels (no dialog, no `showModal`), pending and
  "Not accepted" chips, refusal reasons until dismissed, write-back-skipped note, blocked banner.
  Tracking values are escaped text, never a link, never fetched.
- Locales en/tr/ar/fa + `en.meta.json`; help topic `channel-orders` in en/de/tr/ar/fa; docs-shots
  regenerated.

## Review

Independent review: Opus 5.5 (built by Sonnet). Not Fable: the slice moves no money and no stock by
itself (optimistic status is display-only), adds no credential and no auth-exempt route.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | blocker (CI) | docs-shots hashes stale for the changed help topic | fixed: `make docs-shots` regenerated (Tester), guards pass |
| 2 | medium | a till that stops being the main till (handover / un-enrolled) strands pending rows: no upload, no Problem, no dismiss | fixed: rows blocked + one Problem outside the main-till gate; replica-tick test |
| 3 | medium | an `ApplyChannelOrder` failure stops `Run` before recording verdicts → re-sent forever, refusals never shown | fixed: verdict recorded, apply failure logged, batch continues; test |
| 4 | low | buttons come back after a `main_till_required` block is dismissed | deferred → ut-docs#5443 |
| 5 | low | rebase uses the local revision when `current` fails to decode → rare false `order_changed` | deferred → ut-docs#5443 |
| 6 | low | dismiss for an unknown order answered 500 | fixed: 404; test |
| 7 | nit | restock unticked after a Ship field error | fixed |
| 8 | nit | intro promised actions on an additional till | fixed: reworded in en/tr/ar/fa |

Tester (driven run, real till + fake cloud, Playwright): one bug found and fixed — both `<details>` share
a `name=` and Chromium closed the re-rendered open panel, hiding the field error; `name=` is now omitted
while a form is reopened after an error (test asserts it). Verified: buttons per state, offline taps queue
and a cash sale still completes, one action per order per call with `base_revision` rebased, refusal +
dismiss, list chips, 403 block banner; screenshots en / fa (RTL) / tr at 1024×600 and 360 px looked at.
Not verified: dark theme, kiosk class, real touch hardware / on-screen keyboard.

Deliberate differences from the contract text: refused and `noted` rows are kept until staff dismiss
them (the contract says answered rows are dropped and a refusal shown "once"; UX decision D3), and a
`400` logs a warning on every retry.

Pre-flight: help topic `updated` (5 languages) + docs-shots `updated`; `reference/` page n/a (the
contract is the reference and is unchanged); README n/a (no claim changed); `en.meta.json` `updated`;
every `web/locales/*.json` `updated`; language packs (de, es, pt) `deferred → follow-up pack PRs after
merge`; follow-ups as Backlog cards ut-docs#5427, ut-docs#5443.

Verdict: ship once CI is green.
