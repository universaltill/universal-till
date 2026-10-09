# Review: item_image_open / item_image_read host functions (ut-docs#4005)

- **Date:** 2026-10-09 · **Lane:** lane:local · **Card:** ut-docs#4005 (ADR-0121 R1)
- **Built by:** Opus 5.5 (dev subagent) · **Reviewed by:** Fable 5.1 (independent, read-only)

## What shipped
- `imaging.RefJPEG`: the bounded decode → 160 px → JPEG q70 that `loadRefJPEG` inlined. `loadRefJPEG` is removed, not copied.
- New neutral package `internal/itemimages`: `AssetDir`, `AIRefDir`, `ValidID`, `LatestAIRef`, `PruneAIRefs`, `Ref(id, role)`, moved out of `internal/pages`. The built-in identify (`loadReferenceImages`) and the confirm handler now call it.
- `internal/plugins/wasm_itemimage.go`: `item_image_open` / `item_image_read` with per-event state (64 opens, 4 handles, 256 KiB per read, release on the `0` read and at event end via `handleEvent`). `view:inventory` is checked on every open, and a denial is audited.
- SDK `sdk/plugin`: `ItemImageOpen`, role constants and FakeHost support. Test guest `testdata/itemimage_guest` reads 60 `ref` images in one event.
- ut-docs reference entry (`reference/plugin-host-functions.md`, `architecture/wasm-runtime.md`), separate ut-docs PR.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | `refFile` used an unbounded `os.ReadFile`. A huge file in the items tree (sync/import) became guest-repeatable, 64× per event. | **Fixed:** `MaxFileBytes` = 16 MiB, checked with Stat and then a `LimitReader`, so an oversized file is refused unread (`-1`). Test pads a valid PNG past the cap, and it failed before the fix. |
| 2 | minor | `ValidID` let through NUL/control bytes, `:`, Windows device names and a trailing space, which gave `-3` plus a guest-controlled log line. | **Fixed:** these are now refused (`-4`). Test failed first. The ADR's `/ \ .` rule is still a subset, documented in the reference. |
| 3 | minor | FakeHost honoured `Deny["item_image_read"]`, but the real host never checks on a read. | **Fixed:** the deny is ignored on read, with a comment. |
| 4 | minor | Built-in behaviour change: an undecodable newest `ai_ref` now falls back to `thumb` (old code sent nothing). | **Accepted.** This is the ADR R1 `ref` definition, and it is tested. The ADR's "exactly loadReferenceImages' choice" described the intent, not the old code. |
| 5 | nit | Stale `itemAssetDir`/`loadRefJPEG` comments in `ai_api_test.go`. | **Fixed.** |
| 6 | nit | The "handles never outlive the event" guest test can't fail, because hostState is per event; `closeAll` is covered by its unit test. | **Accepted.** |
| 7 | nit | A denied plugin can write 64 audit rows per event. | **Accepted:** same shape as `view_query` and blobs. |
| 8 | nit | Symlinks inside the items tree are followed. | **Accepted:** unchanged from before, and needs write access to the data dir. |

## Verified beyond the automated tests
- Mutation checks run by the orchestrator, each caught by a test: the permission check removed, the handle cap raised, the open cap raised to 65, and the `ref`→`thumb` fallback removed.
- Full `go test ./...` passed before the review fixes. After them, the affected packages (`itemimages`, `imaging`, `pages` Ref/AI/Identify, `plugins` ItemImage, `sdk/plugin`) plus `go vet`, `gofmt` and `guard-sdk-hostfns.sh` pass.
- No UI surface, locale keys or help topic. Backend host functions only, so there is no e2e or visual check.
- Pre-existing and unrelated: `guard-deadcode-baseline.sh` fails locally on `main` too (GTK headers missing). CI runs it.

## Verdict
Safe to merge. The ut-docs reference PR merges first (`guard-sdk-hostfns.sh`).
