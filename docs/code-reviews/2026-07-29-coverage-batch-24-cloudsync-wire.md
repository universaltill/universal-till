# Test coverage batch 24: cloudsync_wire.go — one real bug found and fixed

2026-07-29

`internal/pages/cloudsync_wire.go` — the ADR-0018 cloud-directive wiring
(install/remove plugin, adjust stock, create item, and the heartbeat's
"problems" digest sent to the cloud). Had zero test coverage: no
`cloudsync_wire_test.go` existed before this batch.

## Bug: `collectProblems` could split a multi-byte UTF-8 rune when truncating

`msg[:200]` was a raw byte-index slice. For a log message where a
multi-byte character (e.g. a non-ASCII shop/operator name — this product
is multilingual, see `docs/reference/i18n.md`) straddles byte offset 200,
the slice keeps only the lead byte of that character and drops its
continuation byte(s), emitting invalid UTF-8 into the cloud's Problems
feed.

**Impact**: cosmetic-to-moderate — a malformed byte sequence in a
diagnostic log digest, not a crash (Go's `encoding/json` replaces invalid
sequences with U+FFFD on marshal), but garbled/lossy operator-facing
Problems output for any shop running non-ASCII locale content near the
truncation boundary.

**TDD**: `TestCollectProblems_TruncationIsUTF8Safe` constructs a message
with 199 ASCII bytes followed by a 2-byte `'é'` (0xC3 0xA9) at byte
indices 199–200, confirmed failing against the pre-fix code
(`utf8.ValidString` false, dangling lead byte `\xc3` at the end), then
passing after the fix.

**Fix**: walk `cut` back from 200 to the nearest rune boundary via
`utf8.RuneStart` before slicing, so a broken trailing rune is dropped
entirely rather than half-emitted.

## Independent review (sonnet)

Re-verified the TDD byte-math claim independently (recomputed the index
arithmetic rather than trusting the commit description) and confirmed the
fix is bounded (`cut > 0` guard — no infinite loop or negative index) and
correct for the empty-string, all-continuation-byte, and sub-200-byte
cases. Flagged two non-blocking notes, addressed/accepted:

1. `utf8.RuneStart` only checks the top-bit pattern, so if `msg` already
   contained an invalid lead byte the fix wouldn't sanitize it — it only
   guarantees it won't *introduce* a new split of an otherwise-valid rune.
   Documented this scope assumption with a comment rather than expanding
   the fix, since upstream `msg` always originates from `fmt`-formatted
   Go log calls (always valid UTF-8 in practice).
2. Doesn't protect base+combining-mark grapheme clusters from being
   split — cosmetically possible but still valid UTF-8, consistent with
   the bug's actual scope (invalid UTF-8, not "never splits a visual
   character"). Not fixed — out of scope.

Also confirmed no false-pass tests (all assertions check raw SQL state or
a different repo method than the one under test) and no other latent bugs
in the file. Noted but left untouched (pre-existing, not part of this
diff): `cloudInstallPlugin` calls `d.Pm.Reload` without the nil-guard that
`cloudRemovePlugin` uses — unreachable by the new tests since they only
exercise the pre-installer configuration-failure path.

## Coverage added

- `cloudInstallPlugin`: configuration-failure path (missing marketplace
  signing key) and its persisted operator-visible install status — mirrors
  the existing `handleInstallFromMarketplace` HTTP-level test
  (`plugins_status_test.go`), driven directly since a cloud directive
  install goes through this function, not the HTTP handler. Success path
  (full signed-bundle download) intentionally not duplicated here — it
  would require rebuilding the fixture machinery
  `internal/plugins/installer_marketplace_test.go` already owns, for
  coverage of little beyond a status-store save + `Pm.Reload` this file
  already shares with the tested failure path.
- `cloudRemovePlugin`: path-traversal id rejection (`../`, `/`, `\`) and a
  full successful removal (plugin row gone, install status cleared).
- `collectProblems`: recent warn/error log lines, failed plugin installs,
  and the UTF-8 truncation regression above.
- `cloudAdjustStock`: existing tracked stock location, fallback to the
  shop's only stock location when the item isn't yet tracked, the
  no-stock-location-configured error, and the empty-reason default
  ("cloud adjustment").
- `cloudCreateItem`: idempotent retry on an existing active item name (no
  duplicate created), barcode-already-in-use failure (no orphan item
  created), and a full success create with barcode attached.

`StartCloudSync` itself (closures wiring `cloudsync.Hooks` to
`cloudsync.Start`) remains uncovered — pure wiring with no logic of its
own beyond what's already exercised through the functions above; the
directive-loop network behavior belongs to `internal/cloudsync`'s own
test suite.

## Verification

`go build ./...`, `go test ./...`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass.
