# Review — join banner hidden on a till paired with a my. shop as main (ut-docs#4836)

## Bug
A Mac till that joined my. as the MAIN till (setup wizard "Join as Main
till", #4610) with no replica yet showed the ambient "Found a main till on
this network … Link this till to it?" banner. `GET /ui/join-notice` only
hid itself for a replica (`sync.primary_url`) or a till with enrolled
replicas, so a fresh main looked standalone.

## Signal
`marketplace.store_id` and `marketplace.token` both set (the wizard's
`Commit` → `adoptPaired` writes them; Settings pairing does too).
`discovery.PairedWithCloudShop` reads them. Note: lazy anonymous
registration (ADR-0013) writes the same keys, so a till that already holds
any cloud store identity is no longer offered a replica join; it can still
join from Settings → Tills, which is a deliberate manager action.

## Change
- `internal/discovery/join_watch.go`: `PairedWithCloudShop`; `JoinWatch.Tick`
  clears the candidate and stops browsing for such a till.
- `internal/pages/join_notice.go`: the handler returns an empty 200 as well
  (covers a candidate held from before pairing).
- Tests (written first, red before the fix):
  `TestJoinNoticeUI_EmptyOnTillPairedWithMyShopAsMain`,
  `TestJoinWatch_NoCandidateOnTillPairedWithMyShop`.
- No new `en.json` keys, no help text mentions this gate.

## Checks
gofmt, go build ./..., go test ./internal/pages/... ./internal/discovery/...,
golangci-lint, CI guards: see the PR.
