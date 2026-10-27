# Review: SDK `ConnectTestResult.LocationList` (ut-docs#5008)

**Scope:** `sdk/plugin/channel/types.go`, `sdk/plugin/channel/channel_test.go`.
Additive optional field for contract sales-channel-v1 §4.1 (`location_list`,
ut-docs#4921), so a channel adapter can answer the connect test with its own
locations for my.'s location dropdown (ut-cloud `channel-locations`, merged in
ut-cloud#656). Consumer: ut-plugin-channel-shopify (same card).

## What shipped
- `ConnectTestResult.LocationList []Location` (`json:"location_list,omitempty"`).
- `Location{ID, Name, Active}` (`id`, `name`, `active`). Adapters always set
  `active`; the host treats an absent one as `true`.
- A wire test: snake_case keys, the round trip, omitted when empty (an
  adapter without it gets the host's `not_supported`).

## Review
Independent review in a fresh context by a different model from the author.
- No findings on the SDK side. The change is additive, and no mirror contract
  covers these types: ut-cloud's `channelhost.Functions` mirrors imports
  only, and ut-cloud parses the answer itself in
  `channelconn.filterChannelLocations`.
- **TDD re-verified by the orchestrator:** without the type change the test
  fails to build (`back.LocationList undefined`); restored, it passes.

## Verified
`gofmt -l`, `go vet ./...` and `go test ./...` in `sdk/plugin`.

**Safe to merge.** There are no SDK tags: plugins require a pseudo-version of
this `main` commit.
