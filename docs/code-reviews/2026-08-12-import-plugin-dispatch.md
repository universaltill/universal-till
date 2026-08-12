# Code review: import plugin dispatch — the `import` canonical type

**Card:** universaltill/ut-docs#599 — wire the `import` canonical plugin
type into a real dispatch path, mirroring the proven `export.requested.ask`
pattern; foundation piece of the plugin-based import/export epic (#522).
**Branch:** `feat/599-import-plugin-dispatch`
**Reviewer:** independent, fresh-context, model `opus` (did not write the
implementation) — complexity:hard routing per the pipeline's model table
(Dev: `fable`, Review: `opus`, deliberately not `fable` reviewing its own
work).
**Design spec:** `docs/adr/0001-plugin-runtime-wasm.md`'s "Amendment
(2026-08-12) — staged-file host functions for large import payloads",
written by the Architect step before Dev started.

## What shipped (Dev)

- `internal/plugins/wasm_import_file.go` (new) — `importFileRegistry`, a
  per-plugin `(pluginID, handle)` staged-file registry mirroring
  `wasm_tcp.go`'s `tcpConnRegistry` (sequential never-reused handles, cap 4
  per plugin, `Close`/`CloseAll` delete the temp file from disk), plus the
  three new `ut` host functions: `import_file_size` / `import_file_read` /
  `import_file_close`.
- `internal/pages/import_dispatch.go` (new) — `POST /api/data/import`:
  manager-gated, structurally mirrors `/api/data/export`'s entry_key
  resolution (single/none/multiple/not-found), stages the upload to a temp
  file (three-layer size cap: `MaxBytesReader` body bound → declared-size
  fast reject → streamed byte-count authoritative check, same shape
  `catimport.ParseBkp` proved for `.bkp`, ut-docs#594), opens it in the new
  registry, dispatches `import.requested.ask` via `AskPlugin` with a
  handle-only payload (never file bytes).
- `internal/plugins/manifest.go` — `ManifestEntry.Entities`/`FileFormats`
  (optional, folded into the existing `plugin_entries.config_json` column —
  no migration needed, since the column already exists).
- `internal/data/plugin_repo.go` — `ImportEntryRow`/`ListImportEntries`.
- `internal/plugins/wasm_hostfns.go`/`wasm_runtime.go` — host fn
  registration; `Sync` wires `importFiles.CloseAll` alongside the existing
  `tcpConns.CloseAll`.
- Real compiled WASM guest fixture (`testdata/import_guest`) + end-to-end
  test proving data reaches a real module through real chunked reads.

## Verification performed

Full gate re-run fresh by both Tester and Reviewer, independently:
`go build ./...`, `go test ./...` (zero FAIL across every package,
including the real-WASM end-to-end test — 512000 bytes read in 8× 64KB
chunks, SHA256-verified against the source file), `gofmt -l` (the 6 files
it flags are pre-existing, untouched by this branch — verified by
stashing the diff), `guard-data-access.sh`, `guard-kiosk-engine.sh`,
`guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-help-topics.sh` all
green.

Reviewer additionally: traced every return path in the dispatch handler
for temp-file leaks (none found — `defer CloseImportFile` registered
immediately after the handle is confirmed valid, idempotent alongside a
well-behaved guest's own close); verified handle scoping cannot let one
plugin address another's staged file (`(pluginID, handle)` keying,
specifically proven by `TestHostImportFileSizeScoping`); verified the size
cap cannot be bypassed (`header.Size` is host-computed, not
client-controlled; the streamed check is genuinely authoritative);
verified the permission model fails closed on every edge case tried
(case mismatch, empty/nil requested list, undeclared entity); confirmed
the `rollback.go` refactor is a genuine no-behavior-change extraction; and
measured the actual real-hardware-relevant bug described in Finding F1
below by running the real WASM guest against 5MB/20MB/50MB staged files.

## Findings

### F1 (HIGH — fixed). The WASM deadline made the mechanism unusable at the size the card was built for.

`import.requested.ask` was dispatched under the blanket 2s `WasmRuntime`
deadline (10s only if the plugin also held `net:`/`tcp:`) — it was never
added to the widened-deadline event class `export.requested.ask` already
gets. `NewWasmRuntime` runs with `WithCloseOnContextDone(true)`, so the
guest module is force-killed at the deadline, not given a chance to return
a clean error.

Reviewer measured this against the real compiled guest fixture: 5MB ok
(0.50s), 20MB ok but at the edge (1.97s), **50MB force-killed** with
`context deadline exceeded`. The one real input this card exists for is a
270MB speedy kasse `.bkp` — at the measured ~10MB/s chunked-read
throughput, that's ~27s on fast dev hardware and worse on the Pi-class
till hardware this targets. The transport mechanism (staged file + chunked
host reads) was correct; the deadline around it made AC #3 ("a large
200MB+ file reaches the plugin") unachievable as shipped.

**Fixed:** added `importTimeout = 5 * time.Minute` as its own floor
(`isImportClassEvent`), separate from `exportTimeout` (30s) — the
reviewer's own math showed even 30s is too tight for the real input this
card targets, so import gets a much larger, fixed floor rather than a
size-derived deadline (the size cap, `maxImportFileSize`, is what actually
bounds worst case, not this timeout). Pinned with a new regression test,
`TestWasmRuntimeTimeoutFor_ImportClass`, asserting `import.requested.ask`
gets `importTimeout` and that a net-permitted plugin doesn't get capped
down to the shorter `netTimeout`.

### F2 (LOW/MEDIUM — not fixed, follow-up card filed). Upload is staged twice, not once.

`ParseMultipartForm` spools the file part to disk once; `import_dispatch.go`
then `io.Copy`s it a second time into its own `os.CreateTemp`. Peak disk
(or RAM, if `TMPDIR` is tmpfs) is 2× the upload, up to 2GB at the current
1GB cap — this undercuts the ADR amendment's "never buffered whole in
memory" intent on the low-power hardware it targets. Mitigating: the
pre-existing `/api/import` + `catimport.ParseBkp` path has the same
double-staging shape, so this is consistency with an existing pattern, not
a new regression. **Not fixed in this cycle** — filed as a follow-up
(ut-docs backlog) to switch to `r.MultipartReader()` and stream the file
part directly into the staged temp file, alongside the pre-existing
`/api/import` path if that one's worth the same fix.

### F3 (LOW — not fixed, follow-up card filed). A guest passing an out-of-range pointer silently loses bytes.

`hostImportFileRead` calls `f.Read(buf)` (advancing the file's cursor)
*before* attempting `m.Memory().Write(dstPtr, ...)`; if the guest's
`dstPtr` is invalid, those bytes are consumed and unrecoverable (no seek
function exists to rewind). Mitigating: `writeGuest`/`hostTCPRead` have the
exact same shape already, so this isn't a new class of bug introduced by
this card — it's the module's existing buffer-ABI risk profile, applied
consistently. **Not fixed in this cycle** — filed as a follow-up to
consider a memory-bounds check before the read across all three affected
host functions, as one cohesive change rather than fixing it only here.

### F4 (LOW — fixed). Unbounded, unduplicated `entities` form value.

`entities=items,items,…` (repeated many times) cost one
`CheckPermissionGranted` DB round-trip per repeat, plus duplicate entries
in the dispatched payload. Manager-gated, so severity was low, but the fix
was trivial. **Fixed:** dedupe via a `seen` set while parsing the
comma-separated list.

### F5 (LOW — not fixed, noted). `importFileReadBufCap` (256KB) clamp is untested.

The chunking proven by the real-WASM test is driven by the *guest's* own
64KB buffer choice, not the host-side 256KB clamp — nothing proves a guest
claiming e.g. `dstCap = 1GB` is actually clamped. **Not fixed in this
cycle**: this repo has no existing lightweight harness for calling a host
function directly against a fake `api.Module` (every host-fn test in this
package goes through a real compiled WASM guest), so a proper fix means
either extending a guest fixture's protocol or building that harness —
more than this finding's severity warrants on its own. Noted for whoever
next touches this file.

### F6/F7 — no action needed.

F6: the authoritative streamed size check is correct but structurally
unreachable in tests (`multipart.FileHeader.Size` isn't client-controlled)
— sound defense-in-depth, nothing to fix. F7: a docs overclaim
("hundreds of MB" in `plugin-host-functions.md`) that becomes literally
true once F1 is fixed — no separate action needed.

## Security/robustness — confirmed sound, no changes needed

Handle scoping, temp-file lifecycle (no leak on any traced path, including
panic-after-handle-is-valid), the three-layer size cap, and the permission
model (fails closed on every edge case) were all independently verified
and found correct. Handle exhaustion is not a DoS risk — the dispatcher's
unconditional `defer CloseImportFile` reclaims the handle on every request
outcome, so a misbehaving guest cannot accumulate handles across separate
HTTP requests; `Sync`'s `CloseAll` is a backstop, not the only reclaim
path.

## Verdict

One HIGH finding (F1), fixed and independently re-verified (build/test/
gofmt/guards all green after the fix, plus a new regression test). No
BLOCKER-class finding (money/tax, data loss, security) — a second full
review round was not earned; F1 was re-verified directly rather than
re-running the whole review. F2/F3/F5 filed as follow-up backlog cards
rather than expanding this cycle's scope further.
