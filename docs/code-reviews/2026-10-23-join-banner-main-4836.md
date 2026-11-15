# Review — join banner hidden on a till paired with a my. shop as main (ut-docs#4836)

## Bug
A Mac till that joined my. as the MAIN till (setup wizard "Join as Main
till", #4610) with no replica yet showed the ambient "Found a main till on
this network … Link this till to it?" banner. `GET /ui/join-notice` only
hid itself for a replica (`sync.primary_url`) or a till with enrolled
replicas, so a fresh main looked standalone.

## Signal
A dedicated marker, `marketplace.paired_at` (RFC3339), plus `store_id` and
`token`. `adoptPaired` (the wizard's `Commit` and Settings pairing) writes
it; `resetIdentityForPair` and the replica identity repair clear it.
`discovery.PairedWithCloudShop` requires all three. The first version used
store_id + token alone, but lazy anonymous registration (ADR-0013) writes
those too, so a standalone till that registered anonymously lost the banner
(review finding 1); the marker is written only by a my. shop pairing.

## Change
- `internal/enroll`: `keyPairedAt` (= `discovery.CloudPairedAtSettingKey`;
  enroll imports discovery, so the shared constant lives there), set in
  `adoptPaired`, cleared in `resetIdentityForPair` and `repairCopiedIdentity`.
- `internal/discovery/join_watch.go`: `PairedWithCloudShop`; `JoinWatch.Tick`
  clears the candidate and stops browsing for such a till.
- `internal/pages/join_notice.go`: the handler returns an empty 200 as well
  (covers a candidate held from before pairing).
- Tests (written first, red before the fix):
  `TestJoinNoticeUI_EmptyOnTillPairedWithMyShopAsMain` (cloud keys set after
  the candidate is held, so only the handler check can hide the notice),
  `TestJoinWatch_NoCandidateOnTillPairedWithMyShop`,
  `TestJoinNoticeUI_ShownOnAnonymouslyRegisteredStandaloneTill`,
  `TestPairedWithCloudShop` (discovery), enroll:
  `TestPairMainTillAdoptsPairedIdentity` (paired_at set),
  `TestPairRefusedLeavesIdentityCleared` (cleared),
  `TestRegisterAnonymousDoesNotSetPairedAt`, `TestPairedAtKeyMatchesDiscovery`.
- No new `en.json` keys. Help: the earlier claim "no help text mentions this
  gate" was wrong. `web/help/*/multitill.md` step 3 states the rule for when
  a till looks for a main till ("not joined to a main till, and with no other
  tills joined to it"); it now adds "and not set up as a shop's main till
  from my.universaltill.com" in en, de, tr, fa, ar (no es/pt help exists).

## Checks
gofmt, go build ./..., go test ./internal/pages/... ./internal/discovery/...,
golangci-lint, CI guards: see the PR.

## Review (Opus, request changes) and outcomes
1. Signal too broad (anonymous registration writes store_id + token):
   fixed with the `paired_at` marker above; tests cover anonymous = banner
   shown, paired = hidden, adopt sets, reset clears.
2. Help text out of date: fixed in all five locales; help-drift and
   help-topics guards pass.
3. Handler test did not isolate the handler check (keys set before the
   candidate, so the watch already suppressed it): reordered. Verified by
   disabling only the `join_notice.go` check: the test fails; restored: passes.
4. Overstated comment in `join_watch.go` (claimed the test catches key
   drift): rewritten; drift is now covered by `TestPairedAtKeyMatchesDiscovery`
   in enroll.
Not covered by a dedicated test: the replica identity repair clearing
`paired_at` (one added line in `repairCopiedIdentity`; a replica has
`sync.primary_url` so the banner is hidden regardless).

Verdict: approve after these fixes. gofmt, go build ./..., go vet and
go test ./internal/enroll/... ./internal/discovery/... ./internal/pages/...
pass; guard-help-drift.sh and guard-help-topics.sh pass.
