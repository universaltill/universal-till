# Code review: WASM plugin-response log redaction misses a credential embedded as a JSON string (ut-docs#393)

**Date:** 2026-08-07
**Author (Dev):** scrum-master pipeline, Sonnet (complexity:easy)
**Reviewer:** independent fresh-context Sonnet subagent
**Card:** universaltill/ut-docs#393

## What shipped

`internal/plugins/wasm_runtime.go`'s `redactField` (added by ut-docs#384)
recursively redacts sensitive fields in WASM plugin `.ask`/`.authorize`
event responses before they reach the log — by field name (`b64`/`token`/
`secret`), by size (>200 bytes), and recursively into nested JSON objects
and arrays. It did not handle a field whose raw JSON value is itself a
JSON-encoded **string** containing an embedded object — e.g.
`{"provider":"{\"auth_token\":\"…\"}"}`, one encoding layer removed from
the object case. `json.Unmarshal(v, &obj)` fails for a string value, so
recursion never triggered and the embedded credential reached the log
verbatim. Some payment/gateway SDKs proxy a raw upstream response by
stashing it exactly this way.

`redactField` now has a third branch: after the existing object/array
checks fail, it tries unmarshaling the value as a Go string; if that
string's own content is valid JSON, it recurses into it and re-encodes the
redacted result back into a JSON string, preserving the field's outer
type.

## Independent review — one real finding, fixed same-round

The first draft gated the new recursion on the *decoded shape* of the
embedded string's content — only descending if it unmarshaled to a
`map`/`[]interface{}`, bailing on anything else (a bare scalar or, crucially,
*another* JSON string). The independent reviewer verified empirically that
this stops after exactly one unwrap: a credential wrapped in **two** layers
of JSON-string encoding (a string containing a string containing an
object) still leaked, and measured that each extra escaping layer only
costs a few dozen bytes (32B raw → 38B once-encoded → 50B twice-encoded →
~218B at five layers) — comfortably under `maxAskFieldBytes` (200) for
several layers, so the size cap wouldn't catch it either. Given the card's
own premise ("some payment/gateway SDKs proxy a raw upstream response...
one encoding layer removed"), more than one layer of proxying/
serialization is a real, not contrived, possibility.

**Severity: real-but-minor** (narrow, still-easy-to-fix gap in a
credential-redaction path — not a design flaw, and the literal acceptance
criteria as written only asked for one layer) — **fixed in this same
round**, per the reviewer's own suggested minimal correction: the
decoded-shape pre-check was removed. `redactField` now recurses into a
valid embedded JSON string unconditionally; each layer's own
object/array/string/scalar branch decides what to do with its own content,
so redaction cascades through however many encoding layers actually exist,
still bounded by the pre-existing `maxAskNestingDepth` (8) the same way
object/array recursion already was. A scalar payload two layers down
(`"42"`, `"\"x\""`) still self-terminates as a no-op, verified by the
existing `TestSafeAskResultForLog_LeavesPlainJSONStringFieldAlone`.

Other findings, all accepted as non-issues / nitpicks, no action needed:
- Double-decoding cost (shape probe + real decode) — bounded by the size/
  depth caps, not worth optimizing. (Moot after the fix: the shape probe
  was removed entirely.)
- A "Fall through" comment on an empty `switch` case (not a real
  `fallthrough`) — cosmetic; this code path no longer exists post-fix.
- Re-encoding correctness (outer string type preserved) — verified by both
  the shipped unit test and the reviewer's own inspection.
- Empty/whitespace-only string, and scalar (`"42"`) content — confirmed
  handled correctly, no panics, no false positives.
- Depth/size bound consistency — confirmed the `depth >= maxAskNestingDepth`
  guard runs before all three branches (object/array/string), so the new
  path is bounded exactly like the pre-existing ones.

## What was verified beyond automated tests

- **Independent TDD re-verification, twice** (once by the reviewer subagent
  on the first draft, once by me on the corrected fix): reverted the fix,
  confirmed the new tests fail with the exact reported symptom (credential
  visible verbatim in the real process log via `captureRealStdout`),
  restored the fix, confirmed green again.
- Full existing `internal/plugins` suite re-run after the correction —
  ut-docs#384's flat/nested-object/nested-array redaction behavior is
  unchanged (`TestSafeAskResultForLog_FlatShapeRedactionUnchanged`,
  `_RedactsNestedTokenFieldByName`, `_RedactsTokenInArrayOfObjects`, etc.
  all still pass).
- `go build ./...`, `go vet ./internal/plugins/...`, full `go test ./... -race`
  (whole repo), and `scripts/ci/guard-data-access.sh` all green. The one
  pre-existing failure (`TestSaveCleansUpDirectoryOnWriteFailure`,
  `internal/issuereport` — fails under a root-run sandbox) is unrelated,
  already tracked (ut-docs#258 / #415), and untouched by this change.
- No file-write handlers or cwd-relative paths in this diff (pure in-memory
  JSON transform) — the two recurring bug classes this pipeline watches
  for don't apply.
- No real client/shop name anywhere; `tok_live_abc123` is the same
  placeholder credential already used by ut-docs#384's own test fixtures,
  reused here for consistency.
- No `web/help/` manual topic needed — this is a backend logging-only
  change with zero shop-owner-visible surface (confirmed: the diff touches
  only `internal/plugins/**`, nothing under `web/` or `internal/pages/`).

## Regression coverage

- `TestSafeAskResultForLog_RedactsTokenInEmbeddedJSONString` — one layer of
  JSON-string encoding.
- `TestSafeAskResultForLog_RedactsTokenInDoublyEmbeddedJSONString` — two
  layers, added in response to the review finding above; also asserts the
  re-encoded shape preserves both string layers.
- `TestSafeAskResultForLog_LeavesPlainJSONStringFieldAlone` — negative
  case, a plain (non-nested) string field is left untouched.
- `TestHandleEvent_StringEmbeddedNestedAuthorizeTokenRedactedInRealLog` +
  `testdata/stringnestedauthorize_guest` — true end-to-end proof with a
  real compiled WASM module and the real logging package, mirroring the
  existing `TestHandleEvent_NestedAuthorizeTokenRedactedInRealLog`
  precedent (a helper-only test can miss real wiring gaps).

## Safe-to-merge verdict

**Yes.** The independent review's one real finding was fixed in the same
round and re-verified; nothing else was blocking. No items deferred.
