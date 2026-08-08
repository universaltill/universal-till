# 2026-08-08 — Remove unverified legacy plugin install endpoints

**Card:** universaltill/ut-docs#480
**Branch:** `pipeline/480-remove-unverified-legacy-plugin-install`
**Model routing:** complexity:medium — built inline (session model, Sonnet),
independently reviewed by an Opus subagent (isolated worktree, one round).

## Requirement

`POST /api/plugins/upload` and `POST /api/plugins/marketplace/install`
(`internal/pages/plugin_api.go`) installed a plugin verifying only a
caller/catalog-supplied SHA256 checksum — no Ed25519 manifest-signature
verification at all, in direct contradiction of `universal-till/CLAUDE.md`'s
non-negotiable: "Installed plugins are Ed25519-verified before they run...
Never run an unverified plugin." Found during ut-docs#460's review (round 1,
Opus, MINOR N5) and filed as its own card.

Acceptance criteria: no install path in the codebase can install a plugin
without Ed25519 verification; a test proving the gap is closed.

## Design decision: remove, don't patch

The card offered two options: route through the existing Ed25519-verified
`MarketplaceInstaller` path (preferred), or bolt real signature verification
onto the legacy endpoints if they had a genuine separate reason to exist.
Neither endpoint did:

- **No live caller.** Grepped `web/ui/**`, `web/public/**`, and `ut-cloud`
  for both paths — zero references. Only `POST /api/plugins/install-from-marketplace`
  (the verified path, `handleInstallFromMarketplace`) is wired to the UI
  (`web/ui/partials/plugin_install_modal.html`).
- **Already known-dead.** `ut-docs/QUEUE-ARCHIVE.md`'s 2026-07-30 finding
  ("Legacy plugin_api.go install endpoints are half-implemented ghost-installs")
  had already established both endpoints download an artifact, checksum it,
  persist a *synthesized* manifest DB row, then delete the artifact and
  extract nothing — so even pre-fix, no runnable plugin code ever resulted.
  That finding recommended removal but deferred it to "its own cycle, not a
  drive-by." This card is that cycle.

So: deleted both handlers and the now-dead `writeSynthesizedManifest`
helper outright, rather than adding real Ed25519 verification + tar
extraction to intentionally-dead code. This satisfies the acceptance
criteria more strongly than a patch would — there is no unverified legacy
install route left to attack, full stop.

## What shipped

- Removed `POST /api/plugins/upload` and `POST /api/plugins/marketplace/install`
  from `internal/pages/plugin_api.go`, plus `writeSynthesizedManifest`.
- `internal/pages/plugin_api_legacy_test.go`: removed the endpoint-specific
  tests; added `TestLegacyInstallEndpoints_Removed`, which posts a
  catalog-matched, correct-checksum, fully-valid form (the shape the OLD
  code would have returned 200 for) and asserts 404 — a deliberately
  non-tautological regression test (an unknown-id 404 would have passed
  against both old and new code and proven nothing).
- `internal/pages/plugin_api_manager_gate_test.go`: dropped the two
  now-nonexistent routes from the manager-auth-gate table.
- `internal/pages/sync_plugins_test.go`: dropped the replica-guard subtest
  table for the two deleted routes (added by the separately-merged
  ut-docs#460, which had replica-guarded these same endpoints without
  fixing the Ed25519 gap) — replica-guarding a route doesn't fix an
  unverified install on the primary.
- `internal/plugins/install.go`: `InstallPlugin` (the checksum-only,
  no-signature primitive the deleted handlers called) now has no
  production caller. Left in place per the review's MINOR-2 finding, with
  a `// Deprecated:` doc comment pointing at `MarketplaceInstaller.Install`
  instead and a note that full removal is a follow-up.
- `web/locales/{en,fa,ar,tr}.json`: dropped the orphaned
  `plugins.marketplace.install_success` key (its only two consumers were
  the deleted handlers).
- `web/help/img/manifest.json`: regenerated (`node e2e/tests-docs/write-manifest.js`)
  — the recorded surface hash covers every non-test `.go` under
  `internal/pages/`, so deleting code there invalidates it even though no
  screen changed (confirmed: no template/CSS/JS touched, existing
  screenshots remain accurate).

## Independent review (Opus, isolated worktree)

Found one BLOCKER and three MINORs, all fixed in this branch before merge:

- **BLOCKER** — `scripts/ci/guard-docs-shots.sh` failed on the branch
  (passed on `origin/main`, confirmed branch-caused). Fixed by regenerating
  the manifest as above.
- **MINOR** — orphaned `plugins.marketplace.install_success` locale key
  left behind (guard-i18n doesn't check for orphans, only resolution +
  locale parity). Removed from all 4 locale files.
- **MINOR** — `InstallPlugin` (the unverified primitive) survived with zero
  production callers and no warning against reuse. Added a deprecation
  comment; full removal deferred to a follow-up card (see below).
- **MINOR** — a comment overstated "no install path left to attack" when
  `import-from-file` and the store API still exist (both separately
  Ed25519-verified in their normal path, unaffected by this change).
  Narrowed the wording.
- **NIT** (not fixed, optional) — the new regression test's catalog-stub
  fixture is technically unconsumed since the mux 404s before any catalog
  fetch; the "would-have-installed-shaped" property currently rests on the
  test's comment rather than an assertion on `legacyMarketplaceStub`'s
  `lastQuery`. Left as-is — the TDD re-verification below independently
  proves the property holds without it.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l` on every touched file, full
  `go test ./... -race` — all green, run personally on the final commit.
- All four repo guards green: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-i18n.sh`, `guard-help-topics.sh`, plus `guard-docs-shots.sh` (the
  one that initially failed).
- **TDD re-verified independently**, not taken on trust: the review agent
  reverted `plugin_api.go` to `origin/main`'s version (keeping the new
  test), re-ran `TestLegacyInstallEndpoints_Removed`, and confirmed it
  failed — both legacy endpoints actually returned **200** (i.e. actually
  installed) under the old code, not 404. Restored the fix and confirmed
  the test passes again.
- Repo-wide grep for `/api/plugins/upload`, `/api/plugins/marketplace/install`,
  and `writeSynthesizedManifest` after the change: only the new explanatory
  comments and two immutable historical `docs/code-reviews/` records —
  nothing else references the removed routes anywhere (`web/`, `scripts/`,
  `e2e/`, `internal/server`, `internal/auth`, `internal/httpx`).
- No real client/shop name, no secret-shaped literal in the diff.
- Not a visible surface (pure API removal, no template/dialog/page touched)
  — no screenshot check applicable; the docs-shots manifest refresh above
  is the only visual-surface-adjacent step, and it's a hash bookkeeping fix,
  not a content change.

## Deferred follow-up

- Delete (or further deprecate) `internal/plugins.InstallPlugin` now that it
  has no production caller — filed as its own backlog card, same pattern
  this card itself was carved out of ut-docs#460's review.

## Verdict

Safe to merge. CI green on the branch after the blocker fix; independent
review found nothing else blocking.
