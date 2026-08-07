# WASM plugin-response log redaction now recurses into nested JSON (ut-docs#384)

## What shipped

`safeAskResultForLog` in `internal/plugins/wasm_runtime.go` redacts a
plugin's `.ask`/`.authorize` response before it reaches the log — any
field whose raw JSON value exceeds `maxAskFieldBytes` (200), or whose key
name looks sensitive (`b64`/`token`/`secret` — `looksLikeSensitiveFieldName`),
is replaced with an `<omitted: N bytes>` placeholder. It only inspected
**top-level** keys. A realistic payment-gateway response nests its
credential under another key —
`{"approved":true,"provider":{"auth_token":"tok_live_…"}}` — where the
top-level key `provider` doesn't match the sensitive-name check and its
raw JSON size can stay under the per-field cap, so the nested token
reached the log verbatim.

Fix: a new recursive helper, `redactField(key, value, depth)`, applies the
same size/name check to a field and, if not already redacted, recurses
into the value when it's a JSON object or array (bounded by
`maxAskNestingDepth`, 8), applying the same rule to nested keys/elements.
Array elements are checked with `key=""` (never matches the name check on
its own; their own nested fields are still walked). Flat-shape behavior —
the existing top-level redaction tests — is unchanged.

## Independent review (Opus subagent, complexity:medium card)

**First pass found two real issues, both fixed before merge; verdict was
initially NO, now YES.**

1. **BLOCKER (fixed):** the new end-to-end test's fixture
   (`bignestedauthorize_guest`) used a 5000-byte nested token. That makes
   the *parent* `"provider"` field's raw JSON exceed `maxAskFieldBytes` on
   its own, so the **pre-existing top-level size check** — not the new
   nested-by-name logic this fix adds — redacted it wholesale. The
   reviewer proved this by reverting `redactField` entirely and showing
   the test still passed. Fixed by shrinking the fixture token to a small,
   realistic value (`tok_live_abc123`) so the test is forced through the
   actual nested-by-name path, plus a setup guard (`rawProvider >
   maxAskFieldBytes` → fail loudly) so this can't silently regress again.
   Re-verified myself: the corrected test now genuinely fails against
   pre-fix `wasm_runtime.go` (`git show 320ec57:...` in an isolated copy)
   with "the real process log still contains the full nested auth_token
   payload verbatim", and passes with the fix restored.

2. **Security, real (fixed):** the depth cap (`maxAskNestingDepth`) failed
   **open** — past the cap, `redactField` returned the value unredacted
   rather than declining to trust it. The reviewer demonstrated a token
   nested 9 levels deep reaching the log verbatim despite the whole
   payload being well under both `maxAskFieldBytes` and `maxAskLogBytes` —
   any plugin could defeat redaction entirely by nesting deep enough.
   Fixed to redact any value still shaped like a JSON object/array at the
   cap (can't be inspected further, so it can't be trusted); a scalar at
   the cap — already proven safe by the size/name check that runs before
   the depth check — is left alone. Re-verified myself in an isolated
   copy: an isolated probe test leaked a 12-level-deep token against the
   pre-fix-2 code (commit `66ac1e1`) and no longer leaked once the
   fail-closed change was applied.

3. **Non-blocking, addressed:** `TestSafeAskResultForLog_RedactsOversizedNestedField`
   was mislabeled — a child's raw JSON is always a subset of its parent's,
   so an oversized *nested* field always pushes its parent over the cap
   first, meaning `redactField`'s own size check is structurally
   unreachable for this test's payload (the parent gets redacted wholesale
   by the pre-existing per-field check before recursion is ever relevant).
   The test still guards real, correct behavior (the payload never leaks
   either way) — renamed to
   `TestSafeAskResultForLog_RedactsParentWhoseNestedContentIsOversized`
   with a comment explaining why, instead of overclaiming which code path
   it isolates.

4. **Noted, not actioned (follow-up filed):** a credential embedded as a
   JSON **string** (not a nested object) — e.g.
   `"provider":"{\"auth_token\":\"tok_live_…\"}"` — still passes through
   unredacted, one encoding removed from this bug's shape. Pre-existing
   (leaked before this fix too), same class, out of scope for this card.
   Filed as universaltill/ut-docs#393 rather than silently expanding this
   diff.

5. **Nitpicks, not actioned:** arrays of bare scalars (e.g.
   `{"vals":["cs_live_…"]}`, no sensitive parent key) aren't covered —
   honestly documented in `redactField`'s own doc comment, and a real
   credential array would almost always have a sensitive parent key.
   `json.Marshal`'s HTML-escaping renders the placeholder as
   `<omitted: N bytes>` rather than literal angle brackets in
   the log — pre-existing (not introduced by this diff), cosmetic.

Checks that came back clean (both from the reviewer and independently
re-run by me after the follow-up fixes): no false positives on scalars
(`json.Unmarshal` of a number/bool/string into a map or slice always
errors); `null` handled correctly (unmarshals with `err == nil` into a
nil map/slice, guarded by the `obj != nil`/`arr != nil` checks); no
panic/nil-map risk; a differential 14-case corpus against the pre-fix
implementation showed **zero divergence** in flat-shape behavior; deep and
array-nested recursion (including array-nested-in-array) redacts
correctly; no filesystem I/O in the diff, so the two recurring bug classes
this pipeline watches for (missing `os.MkdirAll`, cwd-relative path
instead of `paths.Data(...)`) don't apply; no real secrets committed
(only obvious placeholders); no user-visible surface touched (diff is
exactly 4 Go files under `internal/plugins/`, nothing under `web/` or
`internal/pages/*.html` — no manual/help topic implicated).

## Tests

- Unit: `TestSafeAskResultForLog_RedactsNestedTokenFieldByName` (nested
  object, name match), `TestSafeAskResultForLog_RedactsParentWhoseNestedContentIsOversized`
  (nested content pushing parent over the size cap),
  `TestSafeAskResultForLog_RedactsTokenInArrayOfObjects` (array-of-objects
  name match), `TestSafeAskResultForLog_FlatShapeRedactionUnchanged`
  (explicit before/after guard for the pre-existing top-level behavior).
- End-to-end (real WASM module + real wazero runtime + real logging
  package, mirroring `TestHandleEvent_AuthorizeResultRedactedInRealLog`'s
  own precedent that a helper-only test can miss real wiring gaps):
  `TestHandleEvent_NestedAuthorizeTokenRedactedInRealLog`, new fixture
  `internal/plugins/testdata/bignestedauthorize_guest`.
- TDD claim re-verified personally, twice:
  - Reverted `wasm_runtime.go` to pre-`#384` (`git show 320ec57:...`) in an
    isolated copy: `TestSafeAskResultForLog_RedactsNestedTokenFieldByName`,
    `TestSafeAskResultForLog_RedactsTokenInArrayOfObjects`, and — after the
    fixture fix — `TestHandleEvent_NestedAuthorizeTokenRedactedInRealLog`
    all fail with the expected "logged verbatim" diagnostics. Restored,
    all pass.
  - Isolated probe against commit `66ac1e1` (fix #1 only, before the
    depth-cap fail-closed fix): a 12-level-deep token leaked verbatim.
    Same probe against the current tree: redacted.
- Full gate re-run after the follow-up fixes: `go build ./...`,
  `go vet ./...`, `go test ./...` (full suite), `guard-data-access.sh`,
  `guard-i18n.sh` — all clean except one confirmed pre-existing, unrelated
  failure: `TestSaveCleansUpDirectoryOnWriteFailure`
  (`internal/issuereport`) fails identically on a clean `main` checkout —
  root in this container bypasses the read-only-directory permission the
  test depends on, an environment artifact, not a regression.

## Safe to merge

Yes. Feature branch `fix/384-nested-token-redaction`, two commits
(`66ac1e1` the fix, `7a19b0b` the review follow-ups), merged via `merge`
(not squash/rebase, per this pipeline's standing merge-method rule).
