# Code review: marketplace.Client.GetRevocations wire-format fix (ut-docs#2386)

**Card:** universaltill/ut-docs#2386
**Branch:** `fix/2386-marketplace-getrevocations-wire-format`
**Complexity:** easy — reviewed by a fresh-context Sonnet subagent per
`MODEL-ROUTING.md`.

## What shipped

`internal/plugins/marketplace/client.go`'s dead (zero production callers)
`Client.GetRevocations` method declared its `Revocation`/
`GetRevocationsResponse` structs with the till's own snake_case JSON tags
(`plugin_id`, `latest_version`) and `LatestVersion int64` — the same bug
class ut-docs#2380 already fixed on the LIVE revocation path
(`internal/plugins/revocation.go`'s `RevocationEntry`/`RevocationFeed`),
except worse here: ut-cloud's protojson encoder serializes the proto int64
`latestVersion` field as a JSON **string** (`"latestVersion":"43"`), so the
old `int64` type would hard-fail `json.Unmarshal` the moment this method
is ever wired up, not just silently decode as zero.

This method is being deliberately **kept**, not deleted (per its own
standing doc comment — the two implementations of the same feed are a
"consolidation question, not a mechanical cleanup"), so the fix corrects
the tags/type to match `RevocationEntry`/`RevocationFeed` field-for-field:

- `Revocation.PluginID`: `json:"plugin_id"` → `json:"pluginId"`
- `Revocation.Version`: `json:"version"` → `json:"version,omitempty"`
- `GetRevocationsResponse.LatestVersion`: `int64` / `json:"latest_version"`
  → `string` / `json:"latestVersion,omitempty"`
- Cross-referenced the reference comment in `revocation.go` to note the
  sibling fix.

`client_more_test.go`'s `TestGetRevocations` had the identical false-pass
shape ut-docs#2380 fixed in `revocation_test.go`: it built the mock
response by `json.NewEncoder(w).Encode(GetRevocationsResponse{...})` —
round-tripping through the client's own (buggy) struct, so it could never
catch a wire-format mismatch. Rewrote it to write ut-cloud's real wire
format by hand (literal JSON string, camelCase, `latestVersion` as a
quoted string), mirroring `revocation_test.go`'s `revocationFeedServer`
pattern, and widened the assertions to check `PluginID`/`LatestVersion`,
not just `Action`.

## Independent review

Fresh-context Sonnet subagent, same working tree (read-only investigation,
no commit) — **PASS, no findings**. Verified:

- The decode path is real: `client.go`'s `GetRevocations` method
  `json.NewDecoder(resp.Body).Decode(&result)`s directly into
  `GetRevocationsResponse`, so the fix changes actual runtime behavior,
  not just a comment.
- New tags match `RevocationEntry`/`RevocationFeed` field-for-field.
- The rewritten test is a genuine literal-JSON mock, not a round-trip, and
  its assertions exercise the fixed fields.
- **Mutation-tested the claim directly**: reverted just the struct
  definitions to the old snake_case/`int64` shape (keeping the new test)
  and got a **compile failure** (`mismatched types int64 and untyped
  string`) — confirming the new test would have caught this bug before it
  could ever ship, and reasoned separately that the pre-fix tag mismatch
  would have silently zero-valued the fields even without the type error.
  Restored the fix afterward; confirmed `go test` green again.
- Grepped the whole repo for any other construction site of `Revocation{}`
  / `GetRevocationsResponse{}` with the old field names — none found.
- Re-confirmed the dead-code claim: `.GetRevocations(` has exactly two
  call sites, both in this package's own tests.
- `go build ./...`, `go vet ./internal/plugins/...`,
  `gofmt -l internal/plugins/`, `go test ./internal/plugins/...` all
  clean.

## What was verified beyond the subagent's checks

- Full-repo `go build ./...` and `go test ./...` (all 60-odd packages)
  green on this branch before and after the fix.
- `golangci-lint run ./internal/plugins/...` — 0 issues.
- No production caller anywhere in the repo touches the changed structs
  (confirmed independently by both this review and the card's own filing).

## Not done / out of scope

- The "wire it up vs. delete" consolidation question the method's own doc
  comment names is unchanged by this card — this is a correctness fix for
  the method as it exists today, not a decision on its future.
- No ADR needed (no behavioral or architectural change — dead code stays
  dead, just no longer broken if it's ever called).

## Test plan

- [x] `gofmt -l internal/plugins/` — clean
- [x] `go vet ./internal/plugins/...` — clean
- [x] `go build ./...` — clean
- [x] `go test ./...` — full repo, 60+ packages, all green
- [x] `golangci-lint run ./internal/plugins/...` — 0 issues
- [x] Independent review (fresh-context Sonnet subagent): PASS, no
      findings, including a mutation test proving the fix is load-bearing
