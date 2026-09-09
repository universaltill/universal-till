# Plugin locale-overlay collision detection + docs (ut-docs#1881)

**PRs:** universaltill/universal-till (branch `feat/1881-plugin-locale-collision-detection`),
universaltill/ut-docs (branch `docs/1881-plugin-i18n-convention`)
**Card:** universaltill/ut-docs#1881 (split off ut-docs#1828 at BA time — see
that issue's comment history for why: the "core mechanism" ut-docs#1828 asked
for already existed, so the scoped-down real gap is narrower)
**Complexity:** easy — Dev inline (Sonnet), Review via a fresh-context Sonnet subagent

## What shipped

**Verified before building anything** that `plugins.Manager.syncLocales()`
already merges `locales/*.json` from *any* active installed plugin (not only
`canonical_type:"language"` packs) into the translator as overlays, and that
ADR-0010 already states this as a decision ("Any active plugin may also ship
`locales/*.json` to translate its own strings") — so ut-docs#1828's premise
that this needs a new mechanism and a new ADR didn't hold. What was real and
missing:

1. **Undiscoverable.** `architecture/plugin-architecture.md` never spelled
   the capability out in practical terms — a plugin author had no reason to
   find it. New §7 "Plugin i18n" documents the path convention, when it's
   picked up, the actual precedence order (read from `config/i18n.go`'s
   `T()`, not assumed), and a recommended key-naming convention
   (`<plugin-id>.key`) to avoid colliding with another plugin's own keys.
2. **Unprotected.** `syncLocales()` iterated the bare `m.Installed` Go map,
   so two different plugins shipping the same overlay key for the same
   locale silently clobbered each other, non-deterministically across
   restarts. Now iterates in the existing stable sorted-by-id order
   (`InstalledIDs()`) and logs a warning naming both plugin IDs, the locale
   and the key on a real collision — same class of fix as `loadMenuEntries`'
   existing page-key-collision guard (ut-docs#472).

New regression test `TestSyncLocales_LogsShadowedKeyCollisionDeterministically`
proves both: deterministic winner, and the collision is logged.

**Non-goals, split into their own Backlog cards** (ut-docs#1828 stays open
tracking both): a CI i18n drift-gate template for `ut-plugin-*` repos
(ut-docs#1882), and actually shipping German strings in `ut-plugin-tax-de` /
`ut-plugin-payment-sumup` (ut-docs#1883) — neither is actionable from this
session's repo scope.

## Independent review (Sonnet, fresh context)

Read both diffs, the full `internal/plugins/plugins.go` and
`internal/config/i18n.go`, and the sibling pattern this mirrors
(`loadMenuEntries`'s existing collision logging + its test). Checked the docs
change's factual claims against the actual code and against ADR-0010's exact
text (verbatim match). Ran the gates itself with real output (below) and did
a genuine TDD revert-verify: reverted `syncLocales()`'s body to its pre-fix
form, confirmed the new test fails specifically on the missing collision log,
restored the fix, confirmed green again, left nothing committed.

**Finding, fixed before merge:** the new doc-comment and log line referenced
`docs/architecture/plugin-architecture.md`, which doesn't exist under
`universal-till/docs/` — that file lives in the sibling `ut-docs` repo. Every
other cross-reference to it in this codebase's own review records uses the
`ut-docs/architecture/plugin-architecture.md` form. Fixed to match.

**Finding noted, not acted on:** the collision warning fires even when two
plugins happen to ship byte-identical translation text for the same key (no
real behavioral collision, just coincidental duplication). Judgment call, not
a bug — the ticket asks to log any same-key collision, and a harmless
duplicate is still worth a plugin author's attention (it's still two plugins
that should probably namespace their keys). Left as specified.

## Verified

- `gofmt -l internal/plugins/` — clean.
- `go build ./...` — clean.
- `go vet ./internal/plugins/...` — clean.
- `go test ./internal/plugins/... -v -run 'TestSyncLocales|TestLoadMenuEntries|TestSetLocalizer'`
  — all green, including the new test, after the post-review fix.
- `golangci-lint run ./internal/plugins/...` and `golangci-lint run ./...`
  — 0 issues.
- `go test ./...` (full suite, before the post-review one-line path fix,
  which touched only a comment and a log format string) — all green.
- Not applicable / not touched: `guard-data-access.sh` (no SQL added),
  `guard-i18n.sh` (no `web/ui/**/*.html` string added — the new strings are
  server log lines), `guard-kiosk-engine.sh`, money handling, help topics.
- Docs diff (`ut-docs`): no CI gate is path-scoped to `architecture/**`
  (`docs-checks.yml` only watches `README.md`/`logo/**`/`adr/**`/specific
  scripts), so this PR runs no repo CI — verified by reading the workflow
  file rather than assuming.
