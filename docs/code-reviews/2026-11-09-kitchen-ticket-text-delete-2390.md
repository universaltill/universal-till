# 2026-11-09 — Delete `RenderKitchenTicketText` (ut-docs#2390)

**What shipped.** `internal/print/kitchen.go` loses `RenderKitchenTicketText`, the
plain-text kitchen-ticket renderer that only tests called. Its doc comment and `center`
closure go with it. `kitchen_test.go` drops the tests that only covered the text path
(`TestRenderKitchenTicketTextMirrors`,
`TestRenderKitchenTicketTextCentersByRuneCountNotByteCount` + `kitchenLeadingSpaces`,
`TestRenderKitchenTicketText_ControlRunesOnlyStructuralNewlines`). It also drops the text
halves of `TestRenderKitchenTicketBlankOrderTypePrintsNothing` and
`TestRenderKitchenTicket_LineModeMarkerOnlyWhenSet`, plus the unused `unicode/utf8`
import. `TestRenderKitchenTicketStructure` now asserts the station header (`KITCHEN`) and
the qty-1 row (`Fries`) on the byte path. The
`internal/print/kitchen.go: unreachable func: RenderKitchenTicketText` line is removed from
`scripts/ci/deadcode-baseline.txt`. `RenderKitchenTicket`, the bytes the printer receives,
is unchanged.

**Decision: delete, not wire.** The card offered two options: build a kitchen-ticket
preview or delete the function. No accepted ADR, `reference/` or `architecture/` page, spec
or `web/help/` topic promises a kitchen-ticket preview. Reviewer grepped ut-docs `adr/`,
`reference/`, `architecture/` and `web/help/**`. `printing.md` describes only the receipt
designer's preview. A preview would be a new UI feature with no page, no spec and no demand
signal. If product wants one, it gets its own card and can rebuild the renderer from git
history. Deleting it contradicts nothing binding (ADR-0007).

**Process.** BA → Architect (decision: delete) → Dev (Sonnet) → Tester (parallel, full
gate + deadcode guards) → independent review (Opus 5.5, different model from the author).

**Findings.**
1. Low — fixed. The new `Fries` assertion passed even if the qty-1 rule broke, because
   `1 x Fries` still contains `Fries`. Added `!bytes.Contains(out, "1 x Fries")`.
   Mutation-checked in a throwaway worktree: dropping `&& q != "1"` from
   `RenderKitchenTicket` turns `TestRenderKitchenTicketStructure` red, and restoring it
   turns the test green again.
2. Info — accepted. The deleted text-path guarantees have no byte-path equivalent to keep:
   - Centring by display width: the byte path centres with the printer's `ESC a 1` and
     computes no padding. Rune-safe clipping stays covered by the escpos `clip`/width tests.
   - "Caller's ticket not mutated": the byte path never writes into `t` or `t.Items`.
     `encodeText` sanitises a value copy.
   - The byte-path halves remain: a blank OrderType drops exactly one line, the Mode marker
     prints only when set (an empty Mode leaves the bytes identical), and
     `TestRenderKitchenTicket_ControlRunesNeverReachPrinter` keeps the control-rune checks.
3. Info — no new dead code. `orderLabel`, `clip`, `displayWidth` and `paperText` are still
   called from `RenderKitchenTicket`/`encodeText`, and `paperTexts` from
   `paperDoc`/`paperSlip`. The deadcode baseline only shrinks.
4. Info — no stale references. `grep RenderKitchenTicketText` finds hits only in historical
   `docs/code-reviews/*` records, which are left untouched as history. Nothing in Go code,
   templates, `web/help`, README, or ut-docs `reference/`/`architecture/`/`adr/` refers to
   it. The `papertext.go` "plain-text renderers" comment is generic and still true
   (receipt/voucher text).
5. Process note. During review, the shared checkout had an uncommitted mutation in
   `kitchen.go` (`if m := strings.TrimSpace(it.Mode); true {`). It looks like the parallel
   Tester's mutation check, and it made one of reviewer's `go test` runs fail. Reviewer did
   not touch it. The orchestrator must confirm that `git diff internal/print/kitchen.go` is
   empty before committing. Per reviewer/SKILL.md (#386), mutation checks belong in a
   worktree.

6. Low — fixed (Tester, pre-existing). Nothing failed if the Mode marker was ALWAYS
   printed: the "empty Mode" check compared two identical inputs. Added an assertion
   that a line with no Mode carries no `* ` marker. Mutation-checked: forcing
   `m != ""` to `true` in `RenderKitchenTicket` turns
   `TestRenderKitchenTicket_LineModeMarkerOnlyWhenSet` red; restored → green.
7. Info — fixed. `deadcode-baseline.txt` ended with a pasted guard error line
   (`❌ deadcode-baseline guard: …`), already on `main`; removed. The guard reported
   it as a burned-down entry, so removal is allowed by the growth guard.

**Verification (reviewer).** `go build ./...` ok. `go vet ./internal/print/` ok.
`go test -count=1 ./internal/print/` ok (six clean runs with the committed `kitchen.go`).
`gofmt -l internal/print` is clean. The mutation check for finding 1 is described above.
Tester gate (separate agent): `gofmt -l .` clean; `go build ./...`, `go vet ./...` ok;
`go test ./...` 109/109 packages ok (`internal/plugins` hit the default 10-min timeout
on a 4-core box once, then passed alone with `-timeout 30m` in 665 s — unrelated);
`golangci-lint run ./...` 0 issues; `shellcheck scripts/ci/*.sh` clean;
`guard-deadcode-baseline.sh`, its `_test.sh` and `guard-deadcode-baseline-growth.sh`
pass (run with `GOTOOLCHAIN=go1.27.1`; with the default toolchain `go run
deadcode@v0.48.0` is built by go1.26 and refuses the go1.27 module — local-only, CI uses
go.mod's version). Mutations of every kept byte-path guarantee were each caught. No UI
surface changed: no e2e, no screenshots.

**Pre-flight:**
- Review record: updated.
- Till user-visible behaviour (`web/help`, docs-shots, `reference/`, README): n/a. No
  user-visible change, because nothing in production called the function.
- `en.json` keys: n/a. No strings changed.
- ADR: n/a. Removing dead code sets no decision that future work must follow.
- Plugin/manifest mirrors: n/a.
- Follow-up work: none. A kitchen-ticket preview gets a card only if product asks for one.

**Verdict:** approve. Before commit `git diff internal/print/kitchen.go` was empty (finding 5 resolved).

**Deferred:** none.
