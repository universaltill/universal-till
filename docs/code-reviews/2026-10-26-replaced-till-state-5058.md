# Review: a replaced main till stops acting as main till (ut-docs#5058, UT-2)

- **Date**: 2026-10-26
- **Branch**: `feat/5058-ut2-replaced-state`
- **Design**: ut-docs `reference/design-5058-replace-main-till.md` §3.3 T2,
  §4.4, AC7, AC12; ADR-0117 amendment 2026-10-26; decision D4 (selling stays
  possible, ADR-0116 D6).
- **Contract**: ut-cloud `docs/api-reference.md`: a replaced till's calls
  answer `401 device_revoked` with `data {reason: "replaced", replaced_at,
  replaced_by}` (CL-5, not merged yet); its link socket closes 4003.
- **Reviewer**: independent read-only subagent on a different model (Fable).

## What shipped

- `enroll.MarkReplaced` / `ReplacedState`: the replaced state persisted under
  `till_identity.replaced*` (part of `db.TillCloudIdentityPrefixes`: never in
  a join snapshot; a fresh pairing clears it in `resetIdentityForPair`).
- `cloudsync`: the 401 `device_revoked` body's `data` is kept on
  `statusError.Replaced` (check-in GET and sync POST); `tick` records the
  state on the FIRST such answer (not after the 3-strike lock-out). A plain
  removal, `token_retired` or a proxy 401 carries no signal.
- `cloudlink`: a 4003 close kicks one check-in, so the reason arrives at once.
- `discovery.GateOnReplaced`: no mDNS main-till advert while replaced.
- LAN: a replaced main till answers `409 till_replaced` on pair-request,
  enrol, link, and the sales / no-sale / cleared-basket journals (the
  replicas' cursors do not move, so those stay queued for the re-join to the
  new main till, UT-5); `enroll-token` refuses with a translated message;
  `ping` advertises `link: 0`. A till joining a replaced main till says
  "That main till was replaced…".
- UI: status chip "Replaced by <name>" (with when and what to use instead in
  its title) from the persisted state, and a non-dismissable banner on every
  page (`/ui/replaced-banner`, `base.html`), never a modal. Selling continues.
- Help: `web/help/{en,de,ar,fa,tr}/multitill.md` "When the main till was
  replaced" (help-drift baseline updated deliberately for ar/fa/tr: same gap).

## Findings (independent review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Major | Comment claimed a restored backup keeps the live till's identity (#5069), which is not merged: restoring the old till's backup onto its replacement before #5069 carries the replaced flag. | Comment corrected; dependency stated here. #5069 (open, other lane) strips `TillCloudIdentityPrefixes` on restore, which covers these keys. Recovery meanwhile: Settings → Pair. |
| 2 | Major | Replicas get no signal that their main was replaced; the link client redials every 30 s against the 409. | Partly fixed: `ping` advertises `link: 0` when replaced, so replicas poll. The replica-side "join the new main till" hint is in UT-5 (Tills page). Proper cloud-vouched re-home: follow-up (design N3). |
| 3 | Minor | Turkish banner/hint ended with a bare name where an accusative is needed. | Fixed ("Bunun yerine şu kasayı kullanın: %s"). |
| 4 | Minor | Write-through routes (catalog, settings, users, vouchers, held sales) stay open on a replaced main. | Accepted (offline-first, the shop keeps working until re-join); help now says to make product, price and staff changes on the new main till. |
| 5 | Nit | `recordReplaced` used the tick's ctx. | Fixed: `context.WithoutCancel`. |
| 6 | Nit | Help-drift baseline. | Verified consistent. |

Verified by the reviewer: no false "replaced" (code and reason both
required), both 401 sources covered, the 4003 kick cannot loop, keys stripped
from join snapshots and cleared by both pairing paths, replica cursors only
advance on 200 (nothing quarantined), the replica's link dial treats 409 as
"not serving links", escaping and bidi isolates, kiosk customer pages never
see the banner.

## Verified beyond automated tests

TDD: enroll, cloudsync and discovery tests were written first and failed to
build/fail; the LAN refusal and chip/banner tests fail with
`refuseIfReplaced`/`tillReplaced` disabled (checked by mutation); the queue
test has a control (the same push lands on a main till that was not
replaced).

Driven run: throwaway till (temp `UT_DATA_DIR`, auth off, port 8198), the
replaced state written into its temp DB, Playwright Chromium on the sale
screen: en 1024×600 light and dark (dark theme stylesheet), ar 1024×600,
fa 360px, tr 360px dark. Banner full width, wraps at 360px, no horizontal
scroll; chip in the status bar reads "Replaced by Counter till 2" (ar/fa/tr
translated), its title gives the date and the successor. Screenshots read.

Not looked at: real touch hardware (tablet, Pi), the mDNS advert on a real
network (`UT_LAN_DISCOVERY=0` on the throwaway till; the gate is unit-tested),
de (language pack not installed), end-to-end against a real cloud (CL-5 not
merged).

## Gate

`go build`, `go vet`, `gofmt`, `go test` for enroll, cloudsync, cloudlink,
discovery, app, pages (full), web; every guard in `ci.yml`'s build job (local
failures only `guard-shellcheck-version` and `guard-store-private`, both
environment, both failing on `origin/main` too). Manual screenshots:
`make docs-shots` cannot run on macOS (font pin, ut-docs#4407): the
regeneration must run on Linux before merge.

## Verdict

Safe to merge once ut-cloud CL-5 (the 401 `data`) is merged and deployed, and
after a Linux `make docs-shots` on the rebased branch. Before CL-5 the code is
inert (no signal, no state).

## Deferred

- Replica-side signal of a replaced main (design N3, cloud-vouched re-home).
- New `en.json` keys need the language-pack PRs (orchestrator).
