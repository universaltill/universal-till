# Review: shift-close count_protocol write gate (ut-docs#6159)

Date: 2026-11-13 · Lane: lane:cloud-41 · Complexity: easy · Built by Sonnet, reviewed by Opus 5.5. The orchestrator (Opus) wrote the review-fix delta, and Sonnet reviewed that delta.

## What shipped

`pos.ValidCountProtocol` (`internal/pos/shifts.go`) checks `count_protocol` at shift close, for both the form post and the JSON `/api/shifts/close` request. Before this change it accepted any flat `string → number` object. It now stores only what `ReadCountProtocol` (#1043) can read back without dropping anything:

- **Keys** must be canonical positive integer minor units. `parseDenomKey` is the same test the reader uses, now shared in `count_protocol.go`.
- **Values** must be bare non-negative integer number tokens. A quoted `"3"`, `3.5`, `1e2`, `-1`, `null`, `true` and nested values are refused.
- **Overflow:** a denomination × pieces product or a running sum that overflows int64 is refused.
- **Duplicates and trailing bytes:** a key that appears twice is refused, including one spelled with escapes. Anything but whitespace after the object is refused.
- **Top-level `null`** used to pass the gate. It is now refused.

A refusal returns the existing 400 message `count_protocol must be a flat JSON object of denomination:count`. There is no new string, and rows already stored still read through the tolerant `ReadCountProtocol`, which is unchanged.

Tests:
- `TestValidCountProtocol_Table`: 32 cases.
- `TestCloseShift_CountProtocolShapeIs400OnJSONAndForm`: both entry points, for `{"abc":3}` and `{"100":"3"}`.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Low (pre-existing) | Decode + `dec.More()` let a trailing `}` or `]` through (`{"100":1}]` was stored). | **Fixed**: the parser now walks tokens, with `}` then `io.EOF` required. The new table rows were red before the fix and green after. |
| 2 | Low | Duplicate keys were accepted with the last value winning. `{"100":"x","100":1}` left a quoted value in the stored text. | **Fixed**: a `seen` set during the token walk. The new rows went red → green, plus an escaped-duplicate row. |
| 3 | Nit | `-0` is accepted as 0 pieces. | **Accepted**: the reader treats it as nothing recorded. |
| 4 | Nit | The handler test used `Fatalf` in its loop, so only the first case proved red. | **Fixed**: changed to `Errorf`. |
| 5 | Nit (Sonnet, fix delta) | Pin an escaped duplicate key and a trailing comma. | **Fixed**: two table rows added. |

Fix-delta review (Sonnet): no defects. It ran about 28 adversarial inputs, covering truncation, missing colon, unquoted keys, a NUL byte, whitespace and newlines around tokens, and the exact 4096-byte boundary. It confirmed `dec.Decode` inside a token stream returns the value without leading whitespace, so the first-byte check is sound.

## Verification

- **TDD re-verified by the Opus reviewer and the Tester:** with `origin/main`'s `shifts.go` and `count_protocol.go` restored, 9 table subtests and the handler test fail. The handler test fails with `got 200 … success:true`, meaning the bad blob closed the shift. With the fix restored, everything passes.
- **Clients:** the shifts page JS builds keys from `[]int64` denominations and values with `parseInt`, so it always passes. The e2e spec `targeted-swap-users-shifts-2762` sends `{denom: 2}`, which passes. No other producer exists (`mobile/`, `android/`, `cmd/` and `scripts/` were grepped).
- **Driven run (Tester):** a seeded till was built from the branch, with a register and a shift opened over HTTP.
  - `{"abc":3}` and `{"100":"3"}` got 400 with the message on both the form and JSON paths, and the shift stayed open.
  - `{"5000":2,"100":13}` closed the shift.
  - `/ui/shifts/{id}/count` rendered £50.00 × 2, £1.00 × 13 and a total of £113.00.
- **Full gate (local, after the last edit):**
  - Every script under `scripts/ci/` that `ci.yml`'s build job names passed, with three exceptions that don't touch this diff:
    - `guard-shellcheck-version.sh`: no shellcheck binary in this container.
    - `prune-nightly-assets.sh` and `retry-with-backoff.sh`: release helpers that need arguments, not guards.
  - `golangci-lint run ./...`: 0 issues. `go build ./...`: clean.
  - `go test ./...`: green except `internal/plugins`, which hit the 10-minute default timeout. This diff doesn't touch it, and CI runs it as its own shard.
  - `internal/pos`, `internal/pages` and `internal/data` are green, `pos` and `pages` with `-race`.
  - shellcheck itself is left to CI.

Pre-flight: help topic n/a (no visible behaviour change; the browser never sends these shapes) · `reference/` n/a (no page documents the `count_protocol` shape; the doc comment is updated) · README n/a · en.json/meta n/a (no new key) · review record updated · follow-ups none.

## Verdict

Approve. Merge once CI is green.
