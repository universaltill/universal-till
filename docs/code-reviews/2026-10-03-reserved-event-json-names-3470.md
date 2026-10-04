# Reserved event JSON names (`at`/`type`) rejected at shape-check time

**Card:** ut-docs#3470
**Branch:** `fix/3470-reserved-event-json-names`
**Author model:** Sonnet (complexity:easy)
**Reviewer model:** Opus 5.5, fresh context, isolated worktree

## What shipped

`internal/diagnostics/events.go`'s `marshal()` builds the wire JSON for a
diagnostic event by decoding the struct's own fields into a map, then
unconditionally setting `obj["type"]` and `obj["at"]`. A future event
struct with a field whose JSON name is `"type"` or `"at"` would have that
field's real value silently overwritten — data loss, not a crash, and not
caught anywhere.

`checkEventShape` (ADR-0092 §2's static/init-time allowlist check) now
rejects any field whose `jsonName(f)` is exactly `"at"` or `"type"`,
regardless of its `diag` kind, via one added check ahead of the existing
per-field checks:

```go
if name := jsonName(f); name == "at" || name == "type" {
    problems = append(problems, fmt.Sprintf("%s: json name %q is reserved by the event envelope (marshal writes it after decode) and cannot be used as a field name", where, name))
}
```

Two new table-driven cases (`badReservedAt`, `badReservedType`) were added
to `TestCheckEventShape_RejectsViolatingStructs`, following the file's
existing `bad*` pattern exactly.

**Diff:** `internal/diagnostics/events.go` (+3), `internal/diagnostics/events_test.go` (+10). No other files.

## TDD

Both new subtests were written first and confirmed failing ("the static
check is vacuous") against pre-fix code, then confirmed passing after the
fix. Independently re-verified by the reviewer via a `go test -overlay`
copy with the fix's three lines removed (no mutation of the real
checkout) — same fail/pass result.

## Review

Independent Opus 5.5 review (isolated worktree, fresh context — different
model from the Sonnet author, per `MODEL-ROUTING.md`).

**Verdict: safe to merge, no blockers.**

Three optional/informational findings, all considered and accepted as-is
(no code change):

1. **Nit (no fix):** the reserved-name check runs before the
   `!f.IsExported()` check, so an unexported field named `at`/`type`
   reports both "reserved" and "unexported". Both statements are true,
   fits the function's existing collect-everything style, and causes no
   functional harm — the struct is rejected either way. Not changed.
2. **Informational (no fix):** a field with no `json` tag, or an explicit
   `json:"Type"` (capitalized), passes the new check (case-sensitive
   comparison, matching how Go's `encoding/json` and this function's
   existing case-sensitive `jsonName` already behave everywhere else). A
   future `Type`/`type` collision on the wire is not the data-loss bug
   this card fixes, and would separately be caught by
   `TestEventFieldNamesMatchCloudAllowlist`'s cross-repo mirror. No change
   recommended.
3. **Cosmetic (no fix):** the block comment above the `bad*` structs
   wasn't extended to mention the new reserved-name class. Pre-existing
   drift (it already didn't mention `badUnexported` either); left as-is
   rather than touching unrelated lines.

## Verified beyond the automated tests

- `go build ./...`, `go vet ./internal/diagnostics/...`, `gofmt -l` on both
  changed files — all clean (both dev and reviewer, independently).
- `go test ./internal/diagnostics/... -count=1 -race` — pass (dev and
  reviewer, independently).
- Full `go test ./... -count=1` (whole repo, ~4m53s in the reviewer's run)
  — pass, 0 failures, including every package that imports `diagnostics`
  (`internal/cloudsync`, `internal/issuereport`, `internal/pages`,
  `mobile`).
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`
  — clean (sanity check; this diff touches no SQL and no locale strings).
- Reviewer's own edge-case probe (throwaway overlay test, not committed):
  confirmed every real registered event in `allEvents` still passes
  `checkEventShape`, and that an untagged field or an explicit
  `json:"Type"` is accepted as expected (see finding 2).
- No UI/runtime surface touched — no HTTP handler, no page, no locale key.
  Playwright e2e and the visual look-at-it gate don't apply; explicitly
  skipped, not silently omitted.

## Non-goals (confirmed, not silently dropped)

- `marshal()` itself is untouched — the fix is "a violating struct can
  never register," matching this file's own stated defensive pattern.
- No `ut-cloud` mirror change (`cloudEventFields`/`cloudAllowedTypes`) —
  no real event's field set changed.

## Safe-to-merge verdict

Yes. No deferred items.
