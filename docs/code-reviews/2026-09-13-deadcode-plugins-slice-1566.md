# Code review: `internal/plugins` deadcode-baseline slice (ut-docs#1566)

**Date:** 2026-09-13
**Branch:** `fix/1566-deadcode-internal-plugins-slice`
**Card:** universaltill/ut-docs#1566 ("Burn down 97 unreachable functions in
universal-till"), this cycle's per-package slice: `internal/plugins` (15 of
the 78 remaining `scripts/ci/deadcode-baseline.txt` entries — `ipc.go` x4,
`manifest.go` x1, `manifest_verifier.go` x1, `permissions.go` x3,
`plugins.go` x2, `revocation.go` x1, `rollback.go` x1, `update_checker.go`
x1, `wasm_tcp.go` x1). Third slice after `internal/httpx`
(universal-till#1148) and `internal/data` (universal-till#1149).
**Complexity:** hard (card-level; security/trust-chain-adjacent package per
`CLAUDE.md` — plugin manifest verification, revocation enforcement,
permission gating).
**Review model:** Opus (independent subagent, fresh context, isolated
worktree), per `MODEL-ROUTING.md`'s hard-tier routing. Dev ran on Fable per
the same routing.

## What this slice does

Each of the 15 `internal/plugins` baseline entries was individually
investigated (repo-wide caller search across production code, tests,
`scripts/`, `e2e/`, `cmd/`) before deciding its disposition — matching the
bar the `internal/httpx` and `internal/data` slices set. Given this
package's role in the plugin trust chain, anything security/audit-adjacent
was deliberately kept (never deleted) unless its "dead" status was
unambiguous and its replacement path was independently confirmed live.

**Deleted (6), each verified to have zero remaining callers and no lost
capability:**

- `EventBus.Acknowledge` (+ `TestEventBus_AcknowledgeError`, and the
  incidental `Acknowledge` call inside `TestEventBus_SubscribePublish`) —
  wrote `event_acknowledged` to `audit_log`, but no host-function export
  exposes an "ack" to plugins and nothing reads that audit action back.
  Per-plugin dispatch outcomes are already audited, more richly, by the
  live `auditDispatch`/`auditDispatchWithDB` path.
- `CheckMultiplePermissions`, `HasAnyPermission` (permissions.go, + their
  own tests) — unused wrappers over the live `CheckPermission`; deleting
  them weakens no check.
- `Manager.CatalogPage` (+ `TestCatalogPage`) — DB-paginated sibling of the
  kept `Manager.CatalogList`; its data-layer half,
  `internal/data.PluginRepo.CatalogPage`, stays on the baseline (flagged as
  a future `internal/data` slice candidate — see the fix below).
- `UpdateChecker.GetUpdateInfo` (+ its test block) — trivial filter over
  the live `CheckForUpdates`, no production caller.
- `tcpConnRegistry.Get` — production uses only `GetWithAddr` (the
  single-locked, address-returning form, ut-docs#606); 11 test call sites
  migrated to assert through the same lookup production uses.

**Kept with a doc comment explaining the no-caller state (9):**
`EventBus.GetEventMode`, `EventBus.Subscribe`, `EventBus.Unsubscribe`,
`ComputeSHA256`, `ManifestVerifier.VerifyArtifact`, `ListPluginPermissions`,
`Manager.CatalogList`, `RevocationChecker.GetRevokedPlugins`,
`RollbackManager.GetVersionHistory`. Four of these (`VerifyArtifact`,
`ListPluginPermissions`, `CatalogList`, `GetVersionHistory`) are flagged for
a follow-up issue rather than resolved here — each is the unwired read half
of a live write path, and wiring vs. deleting is a product/UX call outside
this slice's scope. `GetRevokedPlugins`'s comment points at
universaltill/ut-docs#2225 (filed by the prior `internal/data` slice, which
already owns the wire-vs-delete decision for that pair).

Baseline file: 78 → 72 lines (exactly the 6 deletions above; `sort -u`
clean, no reordering, no duplicates).

## Independent review — Opus (fresh subagent, isolated worktree)

**Verdict: APPROVE**, with two comment-only corrections applied directly
(no round 2 needed — unlike the `internal/data` slice, no factual claim
required reverting).

**Decisive extra check the reviewer ran beyond re-reading the diff:** the
real `deadcode@v0.48.0 -test=false . ./cmd/unitill-uninstall` tool (skipping
the `desktop`-tagged root — no GTK/WebKit headers in this sandbox, same gap
as the prior slice). Result: the 6 deleted entries are gone, **no new
unreachable functions anywhere in the module**, and the tool's output
differs from the new baseline by exactly the one entry outside this
rootset's reach (`cmd/unitill-desktop/control.go: controlServer.LastInputAt`,
pre-existing, untouched). That's a stronger local substitute for the guard
than either prior slice ran.

**Security-specific verification on the trust-chain-adjacent kept
functions** (the reason this slice was told to be extra careful):

- `ManifestVerifier.VerifyArtifact` — confirmed `DownloadManager.Download`
  requires a non-empty checksum and fails closed
  (`errChecksumMismatch`) before extraction, for both marketplace
  installers, so nothing regresses by leaving this superseded duplicate in
  place. The reviewer found one real nuance the doc comment doesn't
  capture: `InstallFromStore`'s staged path (`DownloadToStore` →
  `GetStoreDownload` → `installBundleFile`) never re-hashes the file
  against `spec.Checksum` at install time, only at download time — a swap
  of the staged file in between isn't caught by either the deleted
  `VerifyArtifact` or the live path. Practical severity is judged low
  (`wasm_runtime.go` does no load-time hash/signature check either, so
  anyone who can write into `pluginBaseDir` can already tamper
  post-install) — carried into the follow-up issue as "delete *or* wire at
  `InstallFromStore`", not simply "delete."
- `RevocationChecker.GetRevokedPlugins` — confirmed `SyncRevocations`/
  `processRevocation` disable a revoked plugin directly
  (`GetPlugin → StopPlugin → SetPluginState → InsertAuditRaw`) with no
  read-back through this accessor, so revocation enforcement is genuinely
  independent of the dead code here.
- `EventBus.Acknowledge`'s audit-write removal: confirmed no reader of
  `event_acknowledged` exists anywhere (code or `ut-docs`), and that
  dispatch-outcome auditing is unaffected (it goes through a different,
  still-live path).

**Mechanical checks:** `git diff -U0` filtered to non-comment lines showed
only the 13 `GetWithAddr` test-call-site rewrites — zero production-body
changes to any kept function. Baseline `sort -c`/`uniq -d` clean.

**Fixes applied** (comment-only, both re-verified against the real code
before landing):

1. `internal/plugins/ipc.go`'s new `GetEventMode` comment overstated its
   test coverage — `wasm_sync_test.go` asserts `.ask`/`.refund` are marked
   Blocking through it, not `.authorize` too (that hook is marked Blocking
   by `Sync` but never read back by any test). Reworded to say exactly
   that.
2. `internal/data/plugin_repo.go`'s `PluginRepo.CatalogPage` doc comment
   (added by the prior `internal/data` slice) went stale the moment *this*
   slice deleted its only caller, `Manager.CatalogPage` — it still said
   removing `CatalogPage` was "out of this PR's package scope" and named
   the wrong source table (`plugins` instead of `plugin_catalog`, a
   pre-existing error in the same sentence). Rewritten to state the
   post-deletion facts and flag it as a deletion candidate for the next
   `internal/data` slice. **This is the one file touched outside
   `internal/plugins/*.go`** — a deliberate, reviewed exception: leaving it
   would ship a comment pointing at a function that no longer exists.

## Non-blocking findings (not fixed here, filed as follow-ups)

- **Latent double-close in `EventBus.Unsubscribe`** (`ipc.go`, pre-existing,
  not introduced by this diff): `subscribe()` registers the same
  `EventSubscriber`/channel under each event type in a multi-type
  subscription, but `Unsubscribe` closes per occurrence without
  `ResetSubscribers`'s dedupe-by-channel guard — a plugin subscribing to
  ≥2 event types in one call would panic on "close of closed channel" if
  ever unsubscribed as a single plugin (today's only caller path always
  passes one event type, so this is dormant). Worth a fix or at least an
  inline warning now that this function is formally a kept, documented
  helper rather than silently dead. Filed as a follow-up (see below)
  rather than fixed in this no-behaviour-change slice.
- Two historical docs (`specs/007-plugin-host/COMPLETION.md`,
  `docs/code-reviews/2026-08-09-plugin-reload-eventbus-publish-race-504.md`)
  still name `Acknowledge`/`PluginHost.Acknowledge`. Left alone
  deliberately — they're point-in-time records, not living documentation,
  and `PluginHost` hasn't existed in the tree for some time already.

## Verified beyond automated tests

- Read `internal/server/server.go`'s revocation-ticker wiring and
  `internal/plugins/revocation.go`'s `processRevocation` directly to
  confirm enforcement never reads back through `GetRevokedPlugins`.
- Read `internal/plugins/wasm_hostfns.go`'s host-function export table to
  confirm no "ack" is exposed to plugins.
- Read `download_manager.go`, `installer_marketplace.go` and
  `installer_store.go` directly to trace the live checksum-verification
  path `VerifyArtifact` is superseded by, including the staged-install gap
  noted above.
- Counted `EventBus.Subscribe`'s claimed "25 call sites across 10 test
  files" by hand against the real grep result (exact match).
- Re-ran `gofmt`/`go vet`/`go build ./...`/`go test ./internal/plugins/...`
  after the two comment fixes above, not just before them.

## No `CLAUDE.md` rule at risk

No SQL query text added outside `internal/data`/`internal/db` (this slice
touches `internal/plugins`/`internal/data` doc comments only — no new
queries). No money type touched. No i18n-visible string touched. No
kiosk-engine surface touched. No plugin verification WEAKENED — the one
deletion in the trust-chain file (`VerifyArtifact`) was independently
confirmed superseded, not removed on the diff's own say-so, and the
staged-install gap it surfaced is carried forward as a follow-up rather
than silently dropped.

## Gate (re-run after the fix commit, in the review worktree)

```
gofmt -l internal/plugins/ internal/data/                    clean
go vet ./internal/plugins/... ./internal/data/...             OK
go build ./...                                                OK
go test ./internal/plugins/...                                ok (4 packages, 86.7s)
go test ./...  (full repo, run again after the fix commit)     exit=0, 0 FAIL
golangci-lint run ./...                                        0 issues
```

`scripts/ci/guard-deadcode-baseline.sh` could not be run in this sandbox
(needs GTK/WebKit dev headers for the `desktop`-tagged whole-program
analysis, same gap as the prior slice) — real CI (which has the headers) is
the actual gate; the rootless `deadcode` run described above is the closest
local substitute and came back clean (zero new unreachable functions).

## Follow-ups filed on ut-docs#1566's parent card

- universaltill/ut-docs#2238 — `internal/plugins` local `plugin_catalog`
  read path (`Manager.CatalogList`/`Manager.Catalog`/`loadCatalog`, plus
  `internal/data.PluginRepo.ListCatalog`/`CatalogPage`) is dead; decide
  delete-the-chain vs. wire-it-up.
- universaltill/ut-docs#2239 — plugin rollback has no UI/route to discover
  which versions are available (`RollbackManager.GetVersionHistory`
  unwired).
- universaltill/ut-docs#2240 — plugin permission grant/revoke API has no
  listing UI (`ListPluginPermissions` unwired).
- universaltill/ut-docs#2241 — delete-or-wire `ManifestVerifier.VerifyArtifact`
  at `InstallFromStore` (security-aware follow-up; frames the staged-install
  checksum gap the independent review surfaced).
- universaltill/ut-docs#2242 — latent double-close in `EventBus.Unsubscribe`
  for a plugin subscribed to multiple event types in one call (dormant
  today, no current caller triggers it).

Moving universaltill/ut-docs#1566 back to **Ready** (not Done) — its own
acceptance criteria call for splitting the full burn-down into several
small per-package PRs, and baseline entries remain outside
`internal/plugins`/`internal/httpx`/`internal/data`, concentrated in
`internal/pos` (16, tax code — triage carefully per the card's own body)
and the `internal/plugins/marketplace` subpackage (not touched by this
slice). Releasing this cycle's `lane:cloud-54` claim so any lane can pick
up the next slice.
