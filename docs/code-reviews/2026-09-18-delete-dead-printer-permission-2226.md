# Code review: delete dead `HasActivePrinterPermission` (ut-docs#2226)

**Date:** 2026-09-18
**Branch:** `fix/2226-delete-dead-printer-permission`
**Card:** universaltill/ut-docs#2226
**Complexity:** easy
**Review model:** Sonnet (fresh-context subagent, isolated worktree), per
`MODEL-ROUTING.md`'s easy-tier routing.

## What this does

`internal/data.PluginRepo.HasActivePrinterPermission` (checks the
`devices:printer` grant on any active plugin, ignoring `install_state`) had
no production caller. The one live call site
(`internal/pages/pos_api.go`) already used the narrower
`HasActivePrinterCapability`, which additionally requires
`install_state='installed'` so a mid-upgrade plugin doesn't read as
printer-capable. `HasActivePrinterPermission` was kept alive only by its
own differential test, `TestHasActivePrinterPermissionAndCapability`,
written to lock down that the two methods genuinely diverge.

Card #2226 (filed while burning down ut-docs#1566's `internal/data`
deadcode-baseline slice) asked for a real decision: find a real (existing
or planned) call site that wants the broader, install-state-agnostic
check — e.g. printer-setup UI shown during a plugin upgrade — and wire it
up, or delete the method and its differential test together if nothing
plausible turns up.

**Investigation (BA/Architect pass, this cycle):** grepped
`internal/pages` (`plugins_page.go`, `plugin_api.go`,
`plugins_store_page.go`, `settings_page.go`, and every
printer/`install_state`-touching file) and `internal/plugins` for any
existing or planned UI that distinguishes "grant exists regardless of
install state" from "must be fully installed." None found — the only
printer-related UI on the settings page is the built-in ESC/POS
`printerConfig`, unrelated to plugin permissions/capability at all. No
plugin-detail or setup-wizard page shows printer state during an upgrade
today. Nothing in the differential test's own history (see
`docs/code-reviews/2026-09-12-deadcode-internal-data-slice-1566.md`)
suggested a concrete planned caller either — it was filed explicitly as
"not urgent, no behaviour currently wrong."

**Decision: delete.** This is the "nothing plausible turns up" branch the
card itself named as the fallback.

## Diff

- `internal/data/plugin_repo.go` — deleted `HasActivePrinterPermission`
  and its doc comment; updated `HasActivePrinterCapability`'s comment to
  record the ut-docs#2226 resolution and why (repo-wide search found no
  caller, past/present/planned).
- `internal/data/plugin_repo_lifecycle_test.go` — renamed
  `TestHasActivePrinterPermissionAndCapability` to
  `TestHasActivePrinterCapability`, dropping only the
  `HasActivePrinterPermission`-specific assertions; kept the
  `install_state` gate and the deactivation-revokes-capability assertions
  for the surviving method.
- `scripts/ci/deadcode-baseline.txt` — removed the now-obsolete
  `HasActivePrinterPermission` line (the method is gone, not merely
  unreachable — this shrinks the baseline, which the guard treats as
  healthy, not a violation; it only fails on *new*, un-baselined entries).

No SQL added or moved outside `internal/data`; no money/i18n/RTL/
offline-first/kiosk-engine/help-topic surface touched (backend-only,
`ux` label on the card notwithstanding — the actual ask was BA/UX
*judgment* about UI intent, not a UI change).

## Independent review — Sonnet (fresh-context subagent, isolated worktree)

**Verdict: safe to merge as-is**, no changes requested.

- Ran `go build ./...`, `go vet ./internal/data/...`, the targeted test,
  the full `internal/data` package suite, and
  `golangci-lint run ./internal/data/...` (v2.5.0, matches CI's pin) — all
  clean.
- Confirmed via `grep -rn HasActivePrinterPermission .` that no live code
  references the deleted method — only historical review docs (correctly
  untouched) and the new explanatory comment mentioning the name for
  context.
- **Independently re-verified the TDD claim**, not just trusted it: in an
  isolated detached worktree, checked out `main`'s versions of both
  touched Go files over the fix, ran the *original*
  `TestHasActivePrinterPermissionAndCapability` and confirmed it passes on
  `main` — proving the two methods really did diverge, i.e. the
  differential test's original premise was genuine, not a stale
  justification for dead code. Restored to the fix commit and confirmed
  `TestHasActivePrinterCapability` still passes with equivalent coverage
  of the surviving method.
- Independently corroborated the "no plausible call site" finding with
  its own greps (`printer.*(setup|upgrade|wizard)` across
  `internal/pages`/`internal/plugins`, and `devices:printer` in
  non-test code) — same conclusion, no additional call site found.
- Confirmed the new test isn't weakened incorrectly: both behavioral
  assertions that matter for `HasActivePrinterCapability` alone (the
  `install_state` gate, deactivation revokes capability) survive; only
  the assertions about the now-deleted method were dropped.
- Checked the two recurring bug classes this pipeline's reviews watch for
  (missing `os.MkdirAll` on a file-write handler; a cwd-relative path
  where `paths.Data(...)` belongs) — not applicable, this diff has no
  file I/O.
- No real client/shop name, no secret-shaped literal (`com.example.printer`
  is the existing placeholder plugin id already used by this test file).

## Verified beyond automated tests

- `scripts/ci/guard-data-access.sh` → clean (diff only removes SQL,
  entirely within `internal/data`, adds none elsewhere).
- Confirmed the full `go test ./...` (whole repo) passes, not just the
  touched package.
- `scripts/ci/guard-deadcode-baseline.sh` could not be run in this
  sandbox — same known limitation recorded in
  `docs/code-reviews/2026-09-12-deadcode-internal-data-slice-1566.md`:
  `cmd/unitill-desktop`'s cgo GTK/WebKit dependency isn't installable
  here. Not a risk for this diff specifically (a pure deletion can only
  shrink `deadcode`'s reachable set, never grow it), and real CI (which
  has the headers) is the actual gate. Filed as a Backlog follow-up below
  since it's now the second review in a row hitting the identical wall.

## Deferred (filed as Backlog, non-blocking)

- `scripts/ci/guard-deadcode-baseline.sh` can't run without GTK/WebKit dev
  headers present in the environment; give it the same
  `cmd/unitill-desktop` exclusion `.golangci.yml` already carries
  (ut-docs#1581 precedent) so the guard is actually runnable in a
  headless dev/CI sandbox without those packages installed.

## Gate (full repo, run once when finished)

```
gofmt -l .                                     clean
go build ./...                                 OK
go vet ./internal/data/...                     clean
go test ./...                                  ok (all packages)
golangci-lint run ./...                        0 issues
scripts/ci/guard-data-access.sh                ✓ no inline SQL outside internal/data
```
