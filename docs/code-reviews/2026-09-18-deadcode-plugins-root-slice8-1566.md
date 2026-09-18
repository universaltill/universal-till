# 2026-09-18 — Dead-code burn-down, slice 8: internal/plugins root files (ut-docs#1566)

## What shipped

Eighth slice of the ongoing `scripts/ci/deadcode-baseline.txt` burn-down
(ut-docs#1566), covering the 8 baseline entries in `internal/plugins`'
root-level files: `ipc.go` (×3: `EventBus.GetEventMode`,
`EventBus.Subscribe`, `EventBus.Unsubscribe`), `manifest.go`
(`ComputeSHA256`), `permissions.go` (`ListPluginPermissions`),
`plugins.go` (`Manager.CatalogList`), `revocation.go`
(`RevocationChecker.GetRevokedPlugins`), `rollback.go`
(`RollbackManager.GetVersionHistory`).

All 8 were already dispositioned **keep-with-doc-comment** by the
2026-09-13 `internal/plugins` slice (`docs/code-reviews/
2026-09-13-deadcode-plugins-slice-1566.md`, follow-ups ut-docs#2225,
#2238, #2239, #2240, #2242). This slice re-verifies every one of those
claims against everything that has changed in this package since then
(the `Unsubscribe` double-close fix, `ManifestVerifier.VerifyArtifact`
wired into the staged-install path, the revocation-decode fix) rather
than assuming the 5-day-old comments still hold.

**Result: all 8 confirmed still keep. Two comments corrected:**

- `EventBus.Subscribe` (`ipc.go:351`): call-site count was stale (25 →
  26) — ut-docs#2242's `TestEventBus_Unsubscribe_MultiEventTypeSharedChannel`
  added a 26th call site since the count was last written.
- `ComputeSHA256` (`manifest.go:183`): the old comment claimed *every*
  shipped path that needs a bundle hash streams it inline rather than
  re-reading a finished file. That's now false: `ManifestVerifier.
  VerifyArtifact` (`manifest_verifier.go:80-104`), wired into
  `installBundleFile` by ut-docs#2241 to re-check a staged bundle right
  before extraction, does re-read a finished file — and carries its own
  inline duplicate of `ComputeSHA256`'s exact hash loop rather than
  calling it. Comment corrected to say so; the duplication itself filed
  as ut-docs#2398 (consolidation question, not a mechanical cleanup —
  the two functions' error strings differ).

No deletions this slice — same "all-keep, baseline unchanged" outcome as
the `internal/data`, `internal/manual` and `internal/plugins/marketplace`
slices before it. `scripts/ci/deadcode-baseline.txt` stays at 67 lines.

## Independent review

Opus, pointed directly at the dev worktree (no revert/restore needed —
this diff has no bug-fix/TDD claim to re-verify, it's a documentation
correction over an already-zero-behaviour-change baseline). Findings:

- Independently re-counted `EventBus.Subscribe`'s call sites from source
  (not trusting the comment's own number): 26, across exactly 10
  `_test.go` files, confirmed the 26th is genuinely
  `TestEventBus_Unsubscribe_MultiEventTypeSharedChannel` (added by commit
  `b2c6d10`) and that `OrderStatusBroadcaster.Subscribe()` (a same-named,
  unrelated zero-arg method elsewhere in the repo) was correctly excluded.
- Independently confirmed `VerifyArtifact`'s hash loop and its exact
  caller chain (`installBundleFile`, first statement, only directory
  setup between it and extraction) — the comment's claim is literal, not
  an approximation.
- Proved the diff is comment-only two ways: every added/removed line
  starts with `//`, and parsing both file revisions with `go/parser`
  *without* `ParseComments` produced byte-identical ASTs for both files.
- Re-ran the full gate independently: `gofmt -l .` clean, `go build
  ./...`, `go vet ./...`, `go test -count=1 ./internal/plugins/...` (all 4
  packages `ok`, forced uncached), `golangci-lint run
  ./internal/plugins/...` (0 issues).
- Adversarial sweep of every *neighbouring* comment this slice didn't
  touch, re-deriving each cited test name/call site from source — no
  additional stale claims found.
- Re-checked all 8 target functions repo-wide (`internal/`, `cmd/`,
  `scripts/`, `e2e/`, `mobile/`) for a production caller the dev pass
  might have missed — none found; every caller is a test.

**Verdict: safe to merge as-is, no blocking findings.** Two non-blocking
observations (a cosmetic wording nit in the `Subscribe` comment; a
prompt to file the `VerifyArtifact`/`ComputeSHA256` duplication as its
own card rather than leaving it untracked) — the second was acted on:
filed as ut-docs#2398 before this record was written.

## Verified beyond automated tests

- Repo-wide grep for every one of the 8 bare identifiers, across every
  directory a production caller could plausibly live in, independently
  by both the dev pass and the reviewer.
- `scripts/ci/guard-deadcode-baseline.sh` fails in this sandbox for a
  pre-existing, already-documented reason (no GTK/WebKit pkg-config
  headers to type-check `cmd/unitill-desktop` under `-tags=desktop`,
  same limitation every prior `complexity:hard` slice on this card has
  hit). Substitute: ran `deadcode@v0.48.0` directly with roots that don't
  need the desktop tag — 66 findings vs. the 67-line baseline, zero *new*
  unreachable functions, the one baseline entry not visible from this
  rootset (`controlServer.LastInputAt`) matching prior slices' own
  reports of the same gap. Since the diff is a proven comment-only
  change, this substitute is confirmatory, not load-bearing on its own.

## Safe-to-merge

Yes. `merge_method: merge` (never squash/rebase — see `reviewer` skill's
"Merge method" note, ut-docs#250).

## Explicitly deferred

- `ut-docs#2398` — fold `VerifyArtifact`'s duplicate hash loop onto
  `ComputeSHA256` (needs an error-message-wording decision, out of scope
  for a zero-behaviour-change slice).
- The four still-open wire-vs-delete decisions from the prior
  `internal/plugins` slice remain open and untouched by this slice:
  ut-docs#2225 (`ListRevokedPlugins`/`GetRevokedPlugins` chain),
  ut-docs#2238 (`Manager.CatalogList`), ut-docs#2239
  (`RollbackManager.GetVersionHistory`), ut-docs#2240
  (`ListPluginPermissions`).
- ~58 baseline entries remain outside this slice's scope, concentrated in
  `internal/pos` (19 entries, tax code — triage carefully per ut-docs#1566's
  own body) plus misc single-entry files. Next slice should re-check the
  live baseline file rather than trusting this count.
