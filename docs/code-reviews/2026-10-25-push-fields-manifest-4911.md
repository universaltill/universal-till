# Review: `channel.push_fields` manifest field (ut-docs#4911)

**Date:** 2026-10-25 · **Lane:** `lane:cloud-41` · **Built by:** Sonnet · **Reviewed by:** Opus 5.5 (fresh subagent)
**Full record** (the cloud side carries the behaviour): `ut-cloud/docs/code-reviews/2026-10-25-push-fields-hash-4911.md`.

## What shipped here
- `plugins.ManifestChannel.PushFields` (`json:"push_fields,omitempty"`) is placed after `Limits`, the same position as ut-cloud's `signing.CanonicalChannel` mirror. Absent or empty means every field.
- `validateChannelPushFields` refuses an unknown value or a duplicate. The allowed values are `name`, `description`, `sku`, `barcodes`, `price`, `compare_at` and `category_path`, the same set and messages as ut-cloud's `pkg/manifest`.
- Tests: `TestParseManifest_ChannelPushFields` (absent, price only, all fields) and refusal cases in `TestParseManifest_ChannelRefusals`.
- The till never runs channel plugins (`RefuseOnTill`). The field matters here only so the signed manifest round-trips and the mirror test stays green.

## Findings
None in this repo. The cloud-side findings and their fixes are in the full record.

## Verification
These gates pass: `gofmt -l .`, `go build ./...`, `go vet ./internal/plugins/...`, `go test ./internal/plugins/ -run 'Channel|Manifest'` and `golangci-lint run ./...`. The `wasm_tcp` tests hang in the cloud sandbox because they use loopback TCP; CI runs them.
ut-cloud's `TestCanonicalManifestMirrorsPOS` passes against this branch.

**Verdict:** safe to merge, ahead of the ut-cloud PR.
