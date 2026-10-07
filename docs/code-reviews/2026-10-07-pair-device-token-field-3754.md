# Review: "Pair with a shop" reads `data.device_token` (ut-docs#3754)

**Card:** ut-docs#3754 (p1, `complexity:hard`). Settings → "Pair with a shop"
failed on every attempt with `malformed credential (length 0)`: ut-cloud's
`POST /v1/stores/pair` handler has always answered the credential as
`data.device_token` (ADR-0116, the D4 rotate name; also
`ut-docs/architecture/marketplace-merchant-auth.md`), while `postPair` in
`internal/enroll/pair.go` decoded `data.token`. The till side was wrong.
Sibling PR in ut-cloud adds the cross-repo guard. No ADR needed.

## What shipped

- `internal/enroll/pair.go`: `postPair`'s decode struct field `Token`
  (`json:"token"`) → `DeviceToken` (`json:"device_token"`). No fallback for
  the old key.
- `internal/enroll/pair_test.go`: the shared fake cloud now answers the real
  handler envelope (`device_id` + `device_token`) — it was itself sending
  `token`, which is why the suite never caught the bug. New
  `TestPostPairAcceptsCloudHandlerEnvelope` (exact handler envelope, raw
  JSON) and `TestPostPairRejectsLegacyTokenKey` (an answer with only
  `data.token` is refused with `malformed credential (length 0)`).
- `internal/pages/settings_enrol_pair_test.go`: its fake cloud fixed the
  same way.

## Review

Author: Opus 5.5 (Dev). Tester: independent reproduce + full gates.
Reviewer: Fable (different model, `complexity:hard` row), read both repos'
diffs directly. Verdict: **safe to merge**, no blocker/high/medium.

| # | Sev | Where | Finding | Outcome |
|---|---|---|---|---|
| 1 | Info | design | Issue suggested a till-side fallback accepting both keys. ut-cloud never sent `token` on `/pair`, so no released till ever paired; a fallback would protect nothing and add a path no server produces. `TestPostPairRejectsLegacyTokenKey` makes the choice explicit. | Agreed: no fallback |
| 2 | Info | design | No ADR: ADR-0116 already names `device_token` and the architecture doc documents `/pair` answering `device_token`; this restores conformance, it is not a new decision. | Agreed |
| 3 | Info | `internal/enroll/enroll.go:644` | `/v1/stores/register` still decodes `data.token`; checked ut-cloud `stores.go:398` — that handler does send `token`. Consistent legacy pair, not another copy of this bug. | Nothing to change |
| 4 | Nit | `pair_test.go` | Comment says the fixture is the envelope "byte-for-byte in shape"; it is the same keys/shape, not byte-identical. | Accepted, cosmetic |

## Verified beyond automated tests

- Reviewer, in a detached worktree (never the shared checkout): reverted only
  `pair.go` to `origin/main` and ran `go test ./internal/enroll/
  ./internal/pages/ -run Pair`: 10 failures (7 enroll, 3 pages), every one
  `malformed credential (length 0)`, and `TestPostPairRejectsLegacyTokenKey`
  failing the other way (`err = <nil>`, the old key wrongly accepted).
  Restored: all pass. The new tests are not false-pass.
- Mutated the worktree's tag back to `json:"token"` and ran ut-cloud's new
  `internal/paircontract` guard with `POS_DIR` pointing at it: fails with
  the drift message. Restored.
- `go build ./...`, `go vet` on the touched packages, `gofmt -l` clean.
- Tester (before review): full `go test ./... -count=1` ok, zero FAIL/SKIP;
  `guard-data-access.sh` and `guard-i18n.sh` clean; ut-cloud's real HTTP
  handler test `TestPairIssuesPairCredentialAndRecordsFleetDevice` confirms
  the live envelope is `device_token`, 64 hex chars.
- No file I/O, no SQL, no new UI strings, no help-topic change (the
  Settings page's text is unchanged; only the decoded key moved). No real
  shop data or secrets in fixtures.
- Accepted gap, same as every existing cross-repo contract here
  (pricecontract, palettecontract, manifest): no live two-process
  integration run; each side has its own real-handler/real-wire test plus
  the static source-mirror guard.

## Safe-to-merge verdict

Yes. Merge this PR before ut-cloud's: its new guard reads this repo's
`main` and goes green only once `pair.go` carries `device_token` there.
