# Code review — internal/pages coverage batch: the four stragglers (2026-07-30)

**Branch:** `test/pages-stragglers-batch`
**Scope:** the QUEUE.md "Test-coverage push, remainder" item's first batch —
the four named `internal/pages` stragglers left at the end of PR #82.
**Reviewer:** independent different-model subagent (opus), adversarial brief,
told to run everything itself. Pipeline model: fable.

## What shipped

Four new test files driving real HTTP requests through the real mux:

| File | Covers | Function coverage |
|---|---|---|
| `buttons_api_test.go` | `/ui/buttons`, `/api/buttons/add\|remove\|search`, reorder edges | `registerButtonsAPI` 24.2% → **87.1%** |
| `journal_page_test.go` | `/journal`, `/journal/{receipt}` (found/return-links/404/500), `/ui/journal` limit=5 vs full | `registerJournal` 34.3% → **94.3%** |
| `plugin_api_legacy_test.go` | the six legacy inline handlers: upload, catalog proxy, marketplace/install, permissions grant/revoke, trust | `registerPluginAPI` 22.9% → **80.2%** |
| `plugins_store_api_test.go` | store lifecycle API: 400s, installer-unconfigured 500s, download-fail 502, delete-download, full staged-signed-bundle install success | `registerPluginStoreAPI` 29.2% → **93.8%** |

Package: `internal/pages` **71.2% → 75.7%**.

Plus ONE production fix, found TDD-first (failing test before fix):

- **`plugin_api.go` — synthesized-manifest JSON injection/breakage + temp-file
  race (severity: medium).** Both legacy install endpoints built the plugin
  manifest via `fmt.Sprintf` into a JSON string literal: any legitimate
  name/version containing `"` produced invalid JSON (install 500), and a
  crafted catalog value could inject arbitrary manifest fields. The
  marketplace/install path additionally wrote to a FIXED `plugin.json` path in
  the shared `os.TempDir()` — concurrent installs raced on it. Fixed with a
  shared `writeSynthesizedManifest` helper: `json.Marshal` + `os.CreateTemp`.
  Red first: `parse manifest json: invalid character 'Q' after object
  key:value pair` (upload) / `invalid character 'x' ...` (install), green
  after. (The race itself isn't deterministically testable; the unique-file
  fix rides along with the injection fix and was reviewed as correct.)

## Independent review (different model): SAFE TO MERGE, no blocking findings

The reviewer ran build/vet/tests/guards itself (all green, plus `-race` on the
new tests — clean), and:

- **Re-proved the TDD claim from scratch**: stashed only `plugin_api.go`,
  confirmed both regression tests fail against the old code with the exact
  JSON parse errors, restored, confirmed green.
- **Mutation-checked 3 fresh tests** (different from the tester's own 3):
  broke the buttons image normalization, the journal 404 branch, and the
  store install status save — each named test failed; no false-passes found
  among 6 total probed mutations (tester's: reorder comma-split, journal
  limit=full, Sprintf revert on both endpoints).
- **Checked the two recurring bug classes explicitly**: neither applies — the
  synthesized manifest is a transient same-request temp file (deleted at both
  call sites), so `paths.Data()`/`MkdirAll` would be wrong here, not missing.
- **Checked the "blessing danger" question**: the fix does not add capability
  to the legacy ghost-install endpoints (DB row only, artifact deleted,
  nothing extracted or executed, manager-gated, same synthesized
  `runtime:"go"` as before) — pre-existing behavior pinned, remove-vs-finish
  logged as backlog.
- 2 NITPICKs, both accepted as established package precedent with verified
  isolation: legacy endpoints use the real `os.TempDir()` during tests (all
  paths `defer os.Remove`d, serial tests, unique names); `chdirRoot(t)` never
  restores cwd (pre-existing pattern; the `isolatePluginsDir` tests don't
  depend on cwd).

## Verified beyond automated tests

- Full gate: `go build`, `go vet`, full `go test ./...` (whole repo, not just
  the package), all 5 CI guards, Playwright e2e **19/19** against a really
  running till.
- The store-install success test exercises the REAL verification path: a
  fresh Ed25519 keypair, a genuinely signed asset-only bundle staged exactly
  where `DownloadToStore` puts one, installed through
  `/api/plugins/store/install` — asserting the DB row, manager visibility
  after reload, the `active` install-status record, and consumption of the
  staged bundle.
- No leftover test servers/processes (checked ports 8080/8081/4173).

## Honestly-untestable remainder (documented, not faked — no coverage theater)

- `writeSynthesizedManifest` 50%: the uncovered half is `os.CreateTemp`/
  `Write`/`Close` failure branches on the system temp dir — faking requires
  invasive FS mocking disproportionate to risk.
- `registerPluginAPI` remaining ~20%: same class — local IO error branches
  (`os.Create`, `io.Copy`, checksum-read failures on just-written temp
  files) plus `ParseForm` errors.
- Store download success over the real network protocol is covered at the
  installer layer (`internal/plugins/installer_marketplace_test.go`); the
  pages handler's parse/gate/error-mapping and the staged-bundle install ARE
  covered here.

## Follow-ups logged to ut-docs/QUEUE.md (not this batch's scope)

- Legacy `plugin_api.go` endpoints are half-implemented ghost-installs
  (manifest row persisted, artifact deleted, nothing extracted, no Ed25519
  verify) and unreferenced by any UI template — decide remove vs finish.

## Verdict

Merged after review. Next coverage batches (same queue item): `internal/server`,
`selfupdate`, `updates`, `plugins/storage`, `ui`.
