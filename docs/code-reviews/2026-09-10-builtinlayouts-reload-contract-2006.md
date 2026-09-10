# Code review — builtinlayouts.Sync/ReloadPlugins contract (ut-docs#2006)

**Date:** 2026-09-10
**Card:** universaltill/ut-docs#2006 (follow-up from the independent review of
ut-docs#2001, `docs/code-reviews/2026-09-10-builtinlayouts-boot-reconcile-2001.md`)
**Branch:** `fix/2006-builtinlayouts-reload-contract`
**PR:** universaltill/universal-till#1030
**Reviewer:** independent reviewer, fresh-context Sonnet (complexity:easy —
Dev builds inline at the session model, review is a fresh instance of the
same model per this pipeline's model-routing rules), isolated worktree

## What shipped

`internal/plugins/builtinlayouts.Sync` now returns `(changed bool, err error)`
instead of a bare `error`. All three call sites
(`internal/pages/setup_page.go`, `internal/pages/settings_page.go`,
`internal/pages/init.go`) now reload unless `err == nil && !changed` — a
genuine no-op skips the reload; any `Sync` error still reloads
unconditionally, matching `removeSalon`'s own pre-existing doc comment
("file-removal failure is deliberately swallowed specifically so the
caller's ReloadPlugins always runs"). Two gaps fixed:

1. Before this, every call site reloaded unconditionally on any nil-error
   return, including the overwhelmingly common no-op case — duplicating
   `plugins.Init`'s own work and any operator-facing startup warning on the
   boot-reconcile path (`init.go`, added by ut-docs#2001 the same day).
2. On the stale-version reinstall path (`removeSalon` succeeds, then
   `installSalon` fails), `Sync` returned an error and every call site's
   `else if` chain skipped `ReloadPlugins` entirely — leaving the DB saying
   "uninstalled" while the caller's in-memory `LayoutAmendments` still
   carried the pre-existing plugin's amendments.

The third call site (`init.go`) only existed on `main` as of
universal-till#1028 (ut-docs#2001), which merged mid-cycle — this PR was
rebased onto it so the fix covers all three sites the card asked for, not
just the two that existed when the branch started.

## Review

Independent fresh-context Sonnet review, isolated worktree, no access to
Dev's own reasoning. **Verdict: PASS, no blockers.**

Checked: every `Sync` return path's `changed`/`err` values (all correct —
`changed` is `false` on every error path, which is deliberate since no call
site treats it as load-bearing on error); all three call sites implement the
identical "reload unless err==nil && !changed" rule (verified algebraically
identical via De Morgan, confirmed via `golangci-lint`'s shadow checks); a
repo-wide grep for other `builtinlayouts.Sync(` callers (none missed); and
test genuineness via an independent revert-then-restore in the reviewer's
own isolated worktree — reverting the fix broke compilation (proving the
tests are pinned to the new signature), and surgically re-injecting only the
old caller bug (keeping the new `Sync` signature) made
`TestShopTypeEndpoint_FailedReinstall_StillReloadsPlugins` fail with exactly
the expected message, then restored cleanly.

Gate re-run independently: `gofmt -l .`, `go build ./...`, `go vet ./...`
clean; `go test ./internal/plugins/builtinlayouts/... ./internal/pages/...
-timeout 20m` green (`internal/pages` ~220s, matching this package's
documented known-slow baseline — see `.github/workflows/ci.yml`'s own
ut-docs#1992 comment); `golangci-lint run` (repo-pinned v2.5.0) — 0 issues.

**Two non-blocking notes, neither actioned (below the bar for a second
review round per this pipeline's model-routing rules — no money/tax/data-loss/
security dimension):**
- The "reload unless" block is duplicated near-verbatim three times, with
  cosmetic naming differences (`err` vs `syncErr`, `logging.L().Warnf` vs
  `init.go`'s own `log.Warnf` — the loggers genuinely differ per call site,
  not an inconsistency to fix). Worth a shared helper if a 4th call site
  ever appears; not worth extracting for three.
- Only `settings_page.go` got a handler-level "failed reinstall still
  reloads" regression test; `setup_page.go`/`init.go` share identical logic
  (confirmed by inspection) but rely on the `Sync`-level unit test
  (`TestSync_ReinstallFailure_StillReturnsError`) for the shared contract
  rather than each getting their own handler-level duplicate. Accepted as a
  minor, non-blocking coverage gap — the three call sites are structurally
  identical one-liners around the same `Sync` call.

## Verification beyond automated tests

- Manual TDD-first cycle (not just the reviewer's own re-verification
  above): every new test in `builtinlayouts_test.go` and
  `settings_page_test.go` was confirmed failing against the pre-fix
  signature before the fix landed (a compile-time failure for the
  signature-shape tests, a real assertion failure with the exact expected
  message for `TestSync_ReinstallFailure_StillReturnsError` and
  `TestShopTypeEndpoint_FailedReinstall_StillReloadsPlugins`).
- No UI surface touched (pure backend contract refinement) — no
  screenshot/visual check applies, no i18n keys added
  (`guard-i18n.sh` clean), no ADR needed (this refines an existing
  documented contract, not a new architectural decision).

Closes universaltill/ut-docs#2006.
