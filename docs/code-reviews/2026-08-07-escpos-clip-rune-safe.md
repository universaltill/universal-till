# ESC/POS clip() truncates by rune, not byte (ut-docs#371)

## What shipped

`internal/print/escpos.go`'s `clip()` helper — used to fit receipt
header/meta/footer lines to the 42-column `Width` — sliced by **byte**
length (`s[:max]`), not rune count. A multi-byte UTF-8 character sitting
across the cut boundary (German `ä`/`ö`/`ü`/`ß`, or any `ar`/`fa`/`tr`
non-ASCII text) could be split mid-character, producing invalid UTF-8 on
the printer (mangled glyph, `�`, or a dropped trailing byte depending on
firmware). A concrete reachable call site: `invoice.bill_to` + a non-ASCII
customer name (`internal/pages/invoice_page.go:202`, appended onto
`Doc.Header`).

Fix: `clip()` now truncates by `utf8.RuneCountInString` / `[]rune`, which
matches the function's own `Width` framing ("character width," not byte
width). Every `clip()` call site in the package — `escpos.go` (store
name, header, meta, footer, `layoutLine`, `kvRow`'s re-clip, `RenderText`'s
`center`/meta, `RenderLabel`) and `kitchen.go` (station, order no/type,
table, timestamp, item labels/modifiers, its own `center`) — routes
through this one shared function, so the fix covers all of them; no other
truncation helper exists in the package (independently confirmed by the
reviewer).

Also closed while `clip()` was already open, from the independent review
(finding below): `max <= 0` now clips to `""` instead of panicking on a
negative slice index. `kvRow`'s own overflow re-clip
(`clip(label, Width-len(amount)-1)`) can legitimately compute a negative
`max` when `amount` alone is long enough — pre-existing on the old
`s[:max]` too (identical panic), not introduced by this diff, but free to
close since the function was already being touched for hardening.

## Tests

- `TestClip` (table-driven, `internal/print/escpos_test.go`): under/exact/
  over max, non-ASCII text under max, `max` zero/negative (empty result,
  not a panic), empty string. The core regression case: 41 ASCII bytes +
  `ä` (2 UTF-8 bytes) + trailing junk, `max=42` — a byte-based clip cuts at
  byte 42, landing on `ä`'s lead byte alone (invalid UTF-8); the fix keeps
  `ä` whole.
- `TestRenderMultiByteNameAtColumnBoundary_NoInvalidUTF8`: exercises the
  real `Render()` pipeline with a `Doc.Header` line shaped like the actual
  `invoice.bill_to` call site (label + non-ASCII name crossing the
  boundary), asserts `utf8.Valid(out)` on the full byte stream.

**Confirmed test-first**: both new tests fail against the pre-fix `s[:max]`
code (`git stash` on `escpos.go` only, keeping the new tests) — `TestClip`'s
boundary subtest fails with the literal split output
(`...aaaaa\xc3`, invalid UTF-8), and the render-level test fails with
"invalid UTF-8" — then pass clean after restoring the fix.

## Independent review (fresh-context Sonnet subagent, complexity:easy)

Verdict: **safe to merge as-is.** Independently re-derived call-site
coverage across both `escpos.go` and `kitchen.go` and confirmed nothing
else in the package does byte-based truncation on printer-bound text.

Findings, all addressed or explicitly deferred:

1. **real-but-minor, fixed in this diff** — no guard against `max <= 0`;
   `kvRow`'s re-clip can reach a negative `max` and the old code panicked
   identically, so not a regression, but cheap to close while `clip()` was
   already open. Added the guard + `TestClip` subtests for zero/negative
   `max`.
2. **real-but-minor, explicitly deferred, new backlog card filed** —
   `kvRow`'s padding math (`Width - len(label) - len(amount)`) and both
   `center` helpers (`escpos.go`'s `RenderText`, `kitchen.go`'s
   `RenderKitchenTicketText`) still count **bytes**, not runes, for
   alignment/centering. This cannot split a character or produce invalid
   UTF-8 (the bug class #371 targets) — it only under-counts padding for a
   multi-byte label, e.g. `TestRenderStructure`'s own `£2.80` row passes a
   `len([]byte(r)) >= Width` assertion at 42 bytes while the row is
   actually 41 visible columns. Correctly out of this issue's scope per
   the reviewer's own read; filed as ut-docs#376 rather than silently
   dropped.
3. **nitpick, fixed in this diff** — the render-level test's comment named
   `invoice.bill_to` as the real call site but set `Doc.Footer`, while the
   actual call site populates `Doc.Header`. Fixed the test to use
   `Doc.Header` (matching the real code path) and clarified the comment.

## Verified beyond automated tests

- Test-first red/green confirmed as above (stash/pop the source fix,
  re-run the two new tests each way).
- `go build ./...` clean.
- `gofmt -l internal/print/escpos.go internal/print/escpos_test.go` — no
  output.
- `go vet ./internal/print/...` clean.
- `bash scripts/ci/guard-data-access.sh` — passes (no SQL touched).
- No `web/locales/*` change — this fix touches no user-facing string, so
  `guard-i18n.sh` is unaffected.
- No template/UI/manual-surface change — `guard-help-topics.sh` /
  `guard-docs-shots.sh` unaffected.

## Safe to merge

Yes. `go test ./...` run in full: every package green except one **pre-
existing, unrelated** failure —
`internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure` — which
fails because this sandbox runs as root (root bypasses the read-only-
directory permission check the test relies on to force a write failure).
Already tracked as its own backlog card, ut-docs#258
("`TestSaveCleansUpDirectoryOnWriteFailure` fails under a root-run
sandbox"), filed independent of this change; `internal/issuereport` has no
relationship to `internal/print`. Not touched by, and not caused by, this
diff.
