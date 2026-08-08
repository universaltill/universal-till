# 2026-08-08 — Multi-till plugin install/uninstall propagation

**Card:** universaltill/ut-docs#460
**Branch:** `pipeline/460-multitill-plugin-propagation`
**Model routing:** complexity:hard — built by a Fable dev subagent (two passes),
independently reviewed by Opus (two rounds — a blocker-finding first round earned
a scoped second round, per this pipeline's process-depth rule).

## Requirement

A shop with multiple tills (ADR-0011: one primary, LAN replicas) expects one
shared set of installed plugins. Previously, installing/uninstalling a plugin
on any till only ever affected that till, in either direction — a cashier on
till 2 could be missing a tax plugin till 1 has, silently.

## Design

Extends the existing primary-authoritative LAN sync (the same mechanism
already carrying catalog/settings/users/translations, ADR-0011 D2b/D4) to
plugins:

- `GET /api/sync/plugins` on the primary: a fingerprinted bundle of the
  shop's active **marketplace-installed** plugins (rows carrying a
  `listing_id`). File-imported plugins (no listing_id) are intentionally not
  in scope — they can neither be propagated (nothing to re-verify against)
  nor pruned, so they stay per-till by design, unaffected either way.
- The replica's existing 30s pull tick diffs this against its local set and
  converges by **re-fetching and re-verifying from the marketplace itself**
  (the same Ed25519-verified `MarketplaceInstaller` path a manual install
  uses) — never a peer-to-peer binary copy. Convergence is version-pinned to
  the primary's recorded version, not "whatever the marketplace calls
  latest."
- Reconciliation runs every tick, not just when the primary's fingerprint
  moves — an `Unchanged` wire response still re-diffs against a locally
  cached copy of the last-applied bundle, so replica-local drift (a failed
  reload, a mutation that slipped past a guard) heals within one tick
  instead of hiding behind the fingerprint forever.
- A replica-initiated install/uninstall/enable/disable/update/rollback is
  **rejected** with a translated message pointing at the primary — matching
  this codebase's existing precedent for enrolment (`sync_api.go`'s
  enrol-token handler), not a new live reverse-proxy.
- Offline convergence is free: rides the existing pull tick, same as
  catalog/settings.

Full design reasoning and the amended decision are in
`ut-docs/adr/0011-multi-till-sync.md` (this is a genuine, documented reversal
of that ADR's prior "replicas install from the marketplace themselves,
independently" stance — amended, not silently changed).

## What changed (final, after both fix passes)

- `internal/data/sync_plugins_repo.go` (new) — `SyncPluginsRepo`: dumps the
  primary's active marketplace-installed set; reads a replica's locally
  installed version for a given plugin id.
- `internal/pages/sync_admin.go` — `GET /api/sync/plugins`; `syncPullPlugins`
  (called from the existing pull tick) does the diff/converge/prune, caches
  the last-applied bundle for every-tick reconciliation, and never panics
  the whole sync loop (`recover()` at the top).
- `internal/pages/plugin_api.go` — replica guards on every plugin-mutating
  handler: install-from-marketplace, enable, disable, update, rollback,
  uninstall, and (added in the final MINOR-fix pass) the two legacy
  `/api/plugins/upload` and `/api/plugins/marketplace/install` routes.
  `import-from-file` is deliberately **not** guarded (see design above).
- `internal/pages/plugins_store_page.go` — same guard on the plugin store
  page's install/download endpoints (this was the more prominent install
  surface and initially missed the guard entirely — see Review below).
- `internal/pages/cloudsync_wire.go` — `cloudInstallPluginVersion` (version-
  pinned installer call used by the sync path); `cloudInstallPlugin` stays a
  `""`-version wrapper so the existing cloud-directive path (ADR-0018) keeps
  its prior unpinned/latest behavior unchanged.
- `internal/pages/common/deps.go` — `PluginMu sync.Mutex` +
  `ReloadPlugins(ctx)`, serializing every `Pm.Reload()` + `Menu` rebuild
  call site (11 found and replaced) — see Review below for why.
- `internal/auth/middleware.go` — `/api/sync/plugins` added to the sync-path
  auth exemption switch (see Review — this was the first-round BLOCKER).
- `internal/plugins/download_manager.go` — `os.MkdirAll` before creating a
  `.part` file (found via TDD red run: a fresh replica's first-ever download
  can be this feature's own auto-install, and the tmp dir didn't exist yet).
- `web/locales/{en,ar,fa,tr}.json` + `web/help/{en,ar,fa,tr}/{multitill,plugins}.md`
  — new message keys and manual prose, genuinely translated in every locale,
  describing the new behavior and which actions are primary-only.
- `web/ui/pages/plugins.html`, `web/ui/pages/plugins_store.html` — map the
  new keys to localized text (template-populated lookup, per CLAUDE.md).
- `internal/pages/sync_plugins_test.go` (new) — two-till harness (each till
  with its own plugin data directory), a fake signing marketplace, and
  coverage for: install/uninstall propagation, replica-guard rejection on
  every mutating route (including the two legacy ones), version pinning,
  drift healing on an unchanged poll, and reload thread-safety under
  `-race`.
- `ut-docs/adr/0011-multi-till-sync.md` — amendment documenting the above.

Deliberately **not** in this card: ut-docs#368 (the separate join-time
snapshot gap — a plugin registered-but-binary-missing right after joining).
Confirmed no overlap in code paths.

## Independent review — round 1 (Opus, fresh context)

Found **2 BLOCKERs, 7 MAJORs**:

- **BLOCKER**: `/api/sync/plugins` was missing from the auth middleware's
  exemption list — every replica request 401'd before the handler ran. The
  feature was completely non-functional. (This is the exact failure class
  the middleware's own comment already documents from a real past incident
  with `/api/sync/stock`.)
- **BLOCKER**: the plugin store page's install/download endpoints bypassed
  the replica guard entirely — arguably the more prominent install surface
  still installed locally on a replica.
- **MAJOR**s: enable/disable/update/rollback still replica-reachable
  (permanent silent drift); reconciliation skipped entirely once converged
  on an unchanged poll (drift never healed); plugin version never pinned
  (replica could converge to marketplace-latest instead of the primary's
  actual version); a genuine data race on `Pm.Reload()`/`Menu` now firing
  routinely from a background goroutine; a replica-blocked file-import
  error message that was factually wrong plus matching wrong docs; the
  two-till test harness secretly shared one plugin directory between
  "tills".

## Fix pass (Fable, same tier as the build)

Fixed both BLOCKERs and 6 of 7 MAJORs, each proven red-then-green with a
new/extended test. Read-side locking of `d.Menu`/`d.Pm.Installed` (handlers
reading without a lock) was deliberately deferred — the write-side race
(concurrent reloads corrupting the map) is the acute, newly-routine risk this
card introduces; making every read call site lock-aware is a separate,
pre-existing, wider-scope architectural task. Filed as a follow-up (see
below), not silently dropped.

## Independent review — round 2 (Opus, scoped to the fix)

Verified all 8 claimed fixes as **real** — for the four load-bearing ones
(unchanged-poll reconciliation, version pinning, the reload mutex, per-till
test-directory isolation) the reviewer proved each by breaking it in a
throwaway copy and confirming the corresponding test actually failed, so
none of them are theatrical passes. No blockers, no majors, no regressions.
**Verdict: ready to merge**, with 3 MINORs:

- **MINOR A** — a doc comment overclaimed the reconciliation could heal "a
  manually deleted plugin dir" (it can't — the local half of the diff is
  DB-row based, and a deleted-but-still-registered binary is ut-docs#368's
  territory, not this card's). **Fixed**: comment corrected to say so
  explicitly.
- **MINOR B** — two legacy, UI-unreferenced routes
  (`/api/plugins/upload`, `/api/plugins/marketplace/install`) still wrote a
  `plugins` row on a replica with no guard, making the ADR's "every
  marketplace-plugin mutation is rejected on a replica" claim overbroad.
  **Fixed**: both guarded (matching the existing pattern exactly), with new
  test coverage — the ADR's claim is now literally true rather than needing
  to be softened.
- **MINOR C** — no post-install check that the installed version actually
  matches the pinned request; not exploitable today (the marketplace API
  errors on a missing version rather than silently serving latest) but would
  make a misbehaving marketplace retry forever instead of failing loud.
  **Not fixed this cycle** — noted as a follow-up (see below), it's a
  robustness hardening item, not a correctness gap in the verified behavior.

## Verified beyond automated tests

- Full gate run personally by the orchestrator (not just trusted from either
  subagent's report) after every commit: `go build ./...`,
  `go test ./... -race` (all packages, including
  `internal/pages` at ~60-340s depending on run), `go vet ./...`, and all
  four guard scripts (`guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-i18n.sh`, `guard-help-topics.sh`) — all green on the final commit.
- The Ed25519 verification path was traced end-to-end by the round-1
  reviewer (not just asserted): `syncPullPlugins` → `cloudInstallPluginVersion`
  → `MarketplaceInstaller.Install` → manifest signature + artifact
  checksum + compatibility check, with no shortcut, no direct DB/file write
  bypassing it.
- Round-2 reviewer independently confirmed the new `sync.plugins_bundle`
  cache setting can never leave a till (`PerTillSettingPrefixes` filters
  `sync.*` out of the admin dump) and is cleared on unjoin.

## Follow-ups filed (found along the way, not silently dropped)

- Read-side locking for `d.Menu`/`d.Pm.Installed` (the deferred half of the
  data-race MAJOR) — a wider, pre-existing architectural gap.
- MINOR C above (post-install version-match assertion / self-limiting retry
  on a misbehaving marketplace).
- Round-1 review's MINOR N5 (pre-existing, unrelated to this card):
  `/api/plugins/marketplace/install` accepts a caller-supplied URL/checksum
  with no Ed25519 verification at all — now at least replica-guarded by this
  card, but the underlying verification gap is its own, larger finding.

(Tracked as GitHub issues by the Scrum Master step; see the issue's
close-out comment for links.)
