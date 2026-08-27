# 2026-08-27 — Remove vestigial `corepos/` package (ut-docs#1116)

## Summary

Deleted the top-level `corepos/` package (`basket.go`, `money.go`,
`pricing.go`, `sales.go`) entirely, and fixed the one live reference to
it in `specs/001-sql-repo-refactor/quickstart.md`.

## Background

Follow-up from `docs/code-reviews/2026-08-26-inclusive-sale-discount-tax-total-1035.md`'s
finding F8: `corepos/sales.go` was found to carry an unimported copy of
the pre-fix `computeSaleTotals` (flat per-line tax sum, never adjusted
for a whole-sale discount on an inclusive-priced sale) — the identical
bug `#1035` fixed in `internal/pos`. Harmless while unimported, but a
landmine if anyone ever revives or copy-pastes from it.

## Investigation (BA step)

- `grep -rln "universal-till/corepos" --include=*.go .` outside `corepos/`
  itself returned nothing — confirmed unimported.
- No test files (`find corepos -name "*_test.go"` empty), no CI workflow
  or Makefile reference.
- `corepos/pricing.go`'s own package doc comment claims: "Package corepos
  provides core POS business logic for pricing, basket, sales, and money
  operations. This package is designed to be used by both the POS and
  mobile app (gomobile compatible)." This looked like it might be an
  intentional scaffold for a future mobile split (acceptance criterion 2
  on the issue), which would mean *annotating* rather than deleting.
- Checked against the actual, accepted mobile strategy: **ADR-0023**
  ("Android/iOS till strategy: shared core, thin shells", Accepted
  2026-07-25) and its real implementation, `mobile/mobile.go`. That file
  embeds `internal/app`/`internal/server` directly via `gomobile bind` —
  the *entire* existing Go server, not a separate lightweight core
  package. `corepos` has zero relationship to `mobile/`.
- Conclusion: `corepos`'s doc comment describes a design that was
  considered and then superseded by ADR-0023's actual shape. It is not a
  live scaffold — it's dead code from an abandoned parallel design.
  Acceptance criterion 1 (delete) applies, not criterion 2 (annotate).

## Change

- `git rm -r corepos/` — 4 files, 204 lines removed, no other code
  changes required (nothing imported it).
- `specs/001-sql-repo-refactor/quickstart.md`: the SQL-sweep verification
  command (`rg "SELECT|INSERT|UPDATE|DELETE" internal corepos`) dropped
  the now-nonexistent `corepos` path, with a note pointing at this issue
  and ADR-0023 for why.
- Left untouched (correctly historical, not currently-existing-package
  references): `specs/001-sql-repo-refactor/tasks.md`'s two `[X]`-checked
  completed task lines, and the two prior `docs/code-reviews/*.md`
  records that mention `corepos` — both are append-only audit trail,
  not live documentation.

## Verification

- `gofmt -l .` — clean.
- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test ./...` — full suite green (every package, not just the
  touched one).
- All 18 CI-blocking guard scripts from `.github/workflows/ci.yml`'s
  `build` job — all pass (`guard-data-access.sh`,
  `guard-migration-version-collision.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-docs-shots.sh`,
  `guard-help-topics.sh`, `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `guard-osk-loaded.sh`,
  `check-brand-assets.sh`, `guard-makefile-version.sh`).
- No UI/runtime surface touched — pure dead-code removal, nothing to
  drive or screenshot.

## Independent review (different-model pass)

Fresh-context Sonnet subagent (no prior involvement) independently
re-verified all of the above from scratch — re-ran the unimported-symbol
grep, read ADR-0023 and `mobile/mobile.go` directly to confirm the
supersession claim, swept for non-`go build`-visible references
(`go:generate`, `//go:embed`, reflection, CI/Docker/config files), and
confirmed the two remaining doc mentions are legitimately historical.

**Verdict: safe to merge as-is.** No findings required a fix.

## Outcome

Merged via PR (see issue for link). `corepos/` no longer exists in the
tree; the stale-bug landmine it carried is gone with it.
