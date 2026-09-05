# Code review: golangci-lint `unused` gate (ut-docs#1565)

**Card:** universaltill/ut-docs#1565 — "No lint gate in CI: add golangci-lint
unused + a deadcode baseline (97 unreachable funcs, 0 gates)."

**Dev model:** Sonnet (inline, `complexity:medium`) · **Review model:**
Opus (independent, fresh context, isolated worktree — medium-tier review).

## Scope decision, made explicit up front

This card's own title bundles two things: a `golangci-lint` `unused` gate,
and a whole-program `deadcode -tags=desktop` baseline check. This PR ships
only the first. The second needs a build environment with GTK/WebKit dev
headers (cgo) to even type-check `cmd/unitill-desktop` — this session's
sandboxed container cannot install them: the default archive mirror's
plain-HTTP `apt-get install` hung indefinitely, and switching to an HTTPS
mirror got a direct `403 Forbidden` from the sandbox's own proxy allowlist
(verified, not assumed — both attempts are reproducible). Fabricating a
baseline file without being able to run the real analysis would be worse
than not shipping it (a wrong baseline either lets new dead code through
or spuriously fails every future PR). Split into universaltill/ut-docs#1581
with the full reasoning and a concrete task list.

## What shipped

- `.golangci.yml`: `unused` linter enabled (`linters.default: none` +
  `enable: [unused]`), `.claude/worktrees/`/`dist-shells` excluded (stale
  agent worktrees, ut-docs#1567), and `cmd/unitill-desktop` excluded with
  a comment explaining why (see below).
- `.github/workflows/ci.yml`: new "Lint (golangci-lint, unused)" step in
  the `build` job, via `golangci/golangci-lint-action@v8` pinned to
  `version: v2.5.0` (the version this session verified locally).
- `CLAUDE.md`'s "Before committing" list now includes
  `golangci-lint run ./...`.
- Five genuine dead-code removals: `internal/pages/index_page.go`'s
  `collectPaymentMethods`, `internal/pages/sync_plugins_test.go`'s
  `fakeMarketplace.catalogHits` + its backing `catHits` field/increment
  (superseded by `catalogHitsFor`), `internal/plugins/ipc.go`'s
  `EventBus.auditEvent` (only its `...WithDB` sibling is ever called),
  `internal/pos/sales.go`'s `generateReceiptNo`, and — found by review,
  see below — `internal/data/pos_repo.go`'s `POSRepo.ListPaymentMethodIDs`
  + its dedicated test.

## The false-positive class this PR deliberately does NOT "fix"

Running `unused`/`deadcode` over `cmd/unitill-desktop` **without** the
`desktop` build tag reports `attachDeadline` (`attach_gate_other.go`),
`waitForSafeStartup` (`startup_gate_other.go`), and
`shellPollClientTimeout`/`newShellPollClient` (`shell_poll.go`) as dead.
They are not: `attach_gate_other.go`/`startup_gate_other.go` are tagged
`!(desktop && linux)`, and their `_linux.go` siblings are tagged
`desktop && linux` — genuine per-platform implementations, and the
untagged analysis simply never compiles the `desktop`-tagged caller
(`desktop.go`, `webview_fallback.go`) that uses them. `shell_poll.go`
carries no build tag of its own at all, but both its symbols are only
ever called from `webview_fallback.go` (tag `desktop && !darwin`), so the
untagged pass reports them dead for the same underlying reason. Deleting
any of these would break the real desktop build while looking like a
clean "fix" — exactly the trap ut-docs#1565's own body warned about
("without `-tags=desktop`, 14 desktop functions are falsely reported
unreachable"). `.golangci.yml` excludes the whole `cmd/unitill-desktop`
package rather than sprinkling `//nolint` on legitimate code — cleaner,
though it means every untagged non-test file in that package
(`attach_gate.go`, `autostart.go`, `autostart_install_flag.go`,
`control.go`, `shell_poll.go`, `startup_gate.go`, `window_mode.go`) goes
entirely unlinted until the follow-up (#1581) lands.

## Independent review — real findings, both rounds documented

**Round 1 (Opus, fresh context, isolated worktree) found two blocking
issues and four minor ones — this was not a rubber stamp:**

1. **BLOCKING — `golangci-lint-action@v6` cannot run golangci-lint v2.**
   The action's major version gates which golangci-lint major it supports;
   v6 is v1-only, and both the pinned `version: v2.5.0` and
   `.golangci.yml`'s own `version: "2"` config schema need v8+. As
   written, the CI step would have failed outright — the reviewer couldn't
   run a real Actions runner to reproduce the exact failure text, but the
   version-compatibility mismatch is unambiguous and independently
   documented in the action's own release notes. **This is exactly the
   kind of gap that "verify only against a local binary" misses** — every
   other check in this PR (build/test/lint output) was run against a
   locally installed golangci-lint, which bypasses the GitHub Action
   entirely. Fixed: `@v8`.
2. **BLOCKING (repo rule) — the review doc itself wasn't in the reviewed
   commit.** True at the moment the reviewer checked out its snapshot; the
   doc had been written but not yet `git add`ed. Already fixed via a
   `commit --amend` before the review even completed (confirmed by
   `git log`), so this finding closed itself — noted here for the record,
   not because it's still open.
3. **MEDIUM — `collectPaymentMethods`'s removal orphaned
   `data.POSRepo.ListPaymentMethodIDs`.** It was that method's only
   production caller; after the removal, nothing but the method's own
   dedicated test (`pos_repo_lifecycle_test.go`) referenced it.
   `unused` doesn't flag it because it's exported. Fixed: removed the
   method and its test too (grepped repo-wide first — zero other
   references, no interface satisfaction, `index_page.go`'s payment-method
   query uses the unrelated, still-live `ListActivePaymentMethods`).
4. **LOW — a stale comment** in `setup_language_catalog_test.go` still
   named the now-deleted `catalogHits()` accessor. Fixed.
5. **LOW — the follow-up card was referenced as "ut-docs#1565" in three
   places** (`.golangci.yml` twice, `CLAUDE.md` once) instead of
   "ut-docs#1581" — actively misleading, since #1565 is the card this PR
   closes. Fixed all three.
6. **LOW — the exclusion comment under-described what it suppressed**,
   naming only the two `_other.go` stub files and missing that
   `shell_poll.go` (no build tag at all) is excluded for a related but
   distinct reason. Fixed, and expanded to name the now-entirely-unlinted
   file list for #1581's benefit.

All six were fixed in this same PR before merge — a second review round
was not spun up for them: they're either mechanical (action version pin,
three doc references, one stale comment) or a small, independently
re-verified deletion (`ListPaymentMethodIDs`), well under the bar for
re-earning a second full pass. The gate, tests and guards below were
re-run after every fix, not just at the end.

## Verified

- `gofmt -l .` clean.
- `go build ./...` clean.
- `go test ./...` — every package `ok`, none skipped, none failing.
- `golangci-lint run ./...` (v2.5.0, the version pinned in CI) —
  **0 issues**; separately confirmed (on a scratch copy of the config,
  restored immediately after) that removing the `cmd/unitill-desktop`
  exclusion reproduces exactly the 4 build-tag-scoped false positives
  described above, not something new.
- `golangci-lint-action@v8` is the version compatible with a v2-major
  golangci-lint and the `version: "2"` config schema — checked against
  the action's own documented compatibility, since this is precisely
  the class of thing local-only verification cannot catch.
- No client/shop name or secret-shaped literal introduced.
- Backend/tooling + a handful of internal function/method removals only —
  no UI surface, no user-facing string, no manual-topic update needed.

## Verdict

Safe to merge. The gate is real and verified against both the tool
(`golangci-lint run`) and, after the round-1 fix, the CI mechanism that
actually runs it. The deferred half is tracked with a concrete, reasoned
handoff at universaltill/ut-docs#1581 — not silently dropped.
