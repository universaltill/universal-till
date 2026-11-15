# Code review — `channel` plugin type (ut-docs#4759, ADR-0169)

Date: 2026-10-22 · Lane: `lane:local` (unattended dev) · Card: universaltill/ut-docs#4759
(sales channels 1; tracking card #4187). ADR-0169 is still **Proposed**: the owner's six
business questions (#4187/#4758) are open, so only account-free, channel-neutral parts are built.

## What shipped (three repos, one change)
- **universal-till** `internal/plugins`: `channel` is the 23rd `CanonicalTypes` entry;
  migration `089_plugin_entries_type_channel.sql` rebuilds `plugin_entries` to widen the
  `type` CHECK (018's recipe; checksum pinned; row/index preservation test). The manifest
  gains `hosts` (default `["till"]`), `channel_contract` and the `channel` block
  (`manifest.go` structs, `manifest_channel.go` validation at both install paths).
  Refused: an unknown capability or direction, any permission other than `net:{account}`
  (and that one on any other type), `hosts` other than `["cloud"]` on a channel, the channel
  fields on a non-channel, a token/authorize URL that is not `https://{account}/<path>`,
  a weak `account_suffix` (single label, ICANN public suffix, numeric) or an `account`
  pattern that accepts `evil.example/x`, `a@b`, ... in front of the suffix, and every
  till-plugin field (`entries`, `settings`, `hooks`, `limits`, `db`, ...) on a channel.
- A till **lists but never runs or installs** a cloud-hosted plugin:
  `Manifest.RefuseOnTill` in `PersistManifest`, `installBundleFile`, `Importer.Import` and
  `Rollback`; `WasmRuntime.Sync` drops such rows before any load/subscribe/schedule;
  `ClassifyInstallError` maps it to the existing "not compatible with this till" key
  (no new string); the plugin-sync follower treats it as "nothing to follow".
- `sdk/plugin/channel` (Go guest SDK): contract types, the seven host-function wrappers on the
  **`channel`** wasm module only (never the till's `ut` module; a test enforces it), `Run`/
  `Dispatch`, a native `FakeHost` (with the buffer-retry cache). CI builds it for wasip1.
- **ut-cloud** (own PR): signing mirror of the new fields (contract test pairs), `channel` in
  `pkg/manifest` types + openapi enum + 10 locale labels, and the upload gate
  (`pkg/manifest/channel.go`) that ports the same rules so ut-cloud never signs a channel
  manifest the rules refuse.
- **ut-docs** (own PR): ADR-0002 line/title and the count mentions to 23,
  `reference/plugin-manifest.md`.

## Sequencing (the card's own, with one correction)
The two taxonomy guards compare the *sets*, so the till PR cannot be green against the old
line: with ut-docs `main` unchanged they are red, with the ut-docs change they are green
(run both ways locally; `DOCS_DIR=<ut-docs worktree>` green, `DOCS_DIR=<ut-docs main>` red).
All other checks are green without it. Merge order that keeps every `main` honest:
ut-docs (the guard job is path-scoped to `CanonicalTypes`, no other till PR goes red) ->
universal-till -> ut-cloud signing mirror (its contract test reads the till's `main`) ->
ut-cloud channel host (its ABI mirror reads the SDK on the till's `main`).

## Independent review (Opus subagent, three worktrees) — findings and outcomes
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | ut-cloud signs a channel manifest although every channel rule lives only in the till (publisher could upload `net:*`, `token_url` elsewhere). | **Fixed**: rules ported to `pkg/manifest` (`Validate`) and re-run by the host on every release load. |
| 2 | should-fix | `account_suffix` accepted `.co.uk`/`.github.io`/`.127.0.0.1`; `account` accepted `^.*$`, so `evil.example/x`+suffix could steer the token exchange. | **Fixed** (public-suffix, numeric and pattern-probe checks, in both repos; the host also requires a plain DNS name). Tests fail without them. |
| 3 | should-fix | A channel manifest could still carry `entries`, `settings`, `limits` (4 GiB), `hooks`, `db`, ... | **Fixed**: refused on both sides, one test per field. |
| 4 | should-fix | The SDK's oversize retry re-sent a request; contract silent; FakeHost did not cache. | **Fixed**: contract 3.1 states the one-use retry cache; FakeHost caches (http, submit, outbox_next, cursor_get); test asserts the hook ran once. |
| 5 | should-fix | "Green against the old line" cannot hold. | **Corrected** in ADR-0169 12.2 (merge order above). |
| 6 | should-fix | ADR-0002 now lists `channel` while ADR-0169 is Proposed. | **Open, by instruction**: the card/parent asked for the flip; the ut-docs PR says so and must not merge before the parent accepts that. |
| 7 | should-fix | An explicit `hosts:["till"]` on a till plugin would break the signature on older tills. | **Fixed**: the upload gate refuses `hosts`/`channel*` on non-channel types. |
| 8 | nit | `Rollback` did not call `RefuseOnTill`; importer refusal and the bundle test passed for the wrong reason; SDK no-personal-data test was weak; forbidden header list short. | **Fixed** (each with a test that fails on the mutation; importer mutation verified). |
| 9 | nit | `sync_admin` branch unreachable in practice; `entries[].type` may be `channel`. | **Accepted**: documented; `TestPersistManifest_EveryCanonicalEntryTypeInserts` needs every type insertable. |

## Verification
`go build ./...`, `go vet`, `go test` for `internal/plugins`, `internal/db`, `internal/pages`,
`internal/cloudsync`, `sdk/plugin/...` (native + `GOOS=wasip1 GOARCH=wasm` vet/build);
`guard-sdk-hostfns.sh` and both taxonomy guards (with the ut-docs worktree); ut-cloud
`TestCanonicalManifestMirrorsPOS` with `POS_DIR` on this tree. Not run locally: golangci-lint
(local build is older than the module's Go), shellcheck 0.9 (0.11 installed).
