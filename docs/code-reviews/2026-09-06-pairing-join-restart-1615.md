# Code review: paste-a-code join's restart dead-end

- **Card:** universaltill/ut-docs#1615
- **PR:** universaltill/universal-till#812 (branch `fix/1615-paste-code-join-restart`)
- **Complexity:** easy — Dev inline (Sonnet), Review via a fresh-context
  Sonnet subagent (per `scrum-master`'s Model routing by complexity)
- **Date:** 2026-09-06

## What shipped

`POST /api/sync/join` and `POST /api/setup/join` (the paste-a-code tab of
the join flow) rendered a raw `<span>` success fragment ending in
"restart this till to finish" with no way to act on it — the exact dead
end ut-docs#1550 already fixed for the discovery-list "Request to pair"
flow, on a code path #1550 never reached (that flow renders through
`pairWaitView`; these two routes render their own one-shot fragment
directly).

This change:

- Adds `web/ui/partials/pairing_join_success.html`, a new partial mirroring
  `pairing_wait.html`'s existing "joined" branch (same restart button +
  `/healthz`-poll script, same `procrestart`-backed mechanism), with
  `join-`-prefixed element ids so it can safely coexist in the DOM with
  `pairing_wait.html`'s own instance — both `tills.html` and `setup.html`
  mount a discover tab and a code tab side by side, hidden only with
  Alpine `x-show`, not removed from the DOM.
- Adds `renderJoinSuccess` in `internal/pages/pairing_join.go`, reusing the
  existing `pairingRestartSupported()` seam.
- Wires `internal/pages/sync_api.go`'s two handlers to it: `/api/sync/join`
  → `/api/sync/pairing-restart`, explicit-click-only (manager-driven, a
  configured till restarts only when asked); `/api/setup/join` →
  `/api/setup/pairing-restart`, auto-fires on render (first-boot Pi kiosk
  has no shell to press a button from) — same split #1550 already
  established for the discovery flow.
- No new i18n keys: reuses `tills.joined`, `tills.pairing.restart_now`,
  `tills.pairing.restarting`, `tills.pairing.restarting_slow`,
  `tills.pairing.close_and_reopen`, already shipped in all four locales
  for #1550.
- Updates `web/help/{en,ar,fa,tr}/multitill.md`'s join-flow paragraph,
  which described the paste-a-code tab as needing a manual restart — no
  longer true, both tabs now finish the same way. Re-ran `make docs-shots`
  (manifest + `ar/multitill.png` changed; `en`/`fa`/`tr` screenshots were
  pixel-identical, since their captured viewport doesn't show this
  paragraph).

## What the independent review found

Reviewed by a fresh-context Sonnet subagent (`complexity:easy` →
Sonnet-reviews-Sonnet is the sanctioned exception, per the model-routing
table) in an isolated worktree (`isolation: "worktree"`), on commit
`9185c13`.

- `go build`, `go vet`, `gofmt -l .`, `go test ./...` (full suite, not just
  the touched package), `golangci-lint run ./internal/pages/...` (0
  issues), and the relevant CI guards (`guard-i18n.sh`,
  `guard-help-topics.sh`, `guard-docs-shots.sh`, `guard-htmx-loaded.sh`,
  `guard-page-http-error.sh`, `guard-data-access.sh`,
  `guard-compliance-claims.sh`) all PASS.
- **TDD claim independently re-verified**: reverted
  `internal/pages/sync_api.go` + `internal/pages/pairing_join.go` to
  `main` (test file left in place) — `TestJoinSuccess_*` failed with the
  literal old dead-end text (`"...— restart this till to finish</span>"`).
  Restored the fix — same tests pass. Confirmed the untouched error-path
  tests and the #1550 precedent's own tests still pass unmodified.
- Verified the DOM-id-collision premise is real (both tabs really do
  coexist in the DOM on `tills.html`/`setup.html`, `x-show`-toggled, not
  removed) rather than hypothetical, and that `join-restart-btn`/
  `join-restart-msg` never collide with `pairing_wait.html`'s own ids.
- Diffed the new partial's `<script>` block against `pairing_wait.html`'s
  behaviorally byte-for-byte (same i18n lookup pattern, same 2500ms/500ms/
  56-tries timing, same `/healthz` branching, same three htmx listeners).
- Confirmed the restart-URL/autoRestart mapping is correct and not
  swapped, `Content-Type` isn't regressed, no new i18n keys were needed
  (verified present in all four locale files), no real shop name/secret
  literal in the diff, and the new partial is RTL-safe (no directional
  CSS) with no new modal blocker.
- One **non-blocking** note: the manager-driven flow's help-doc wording
  ("restarts itself... a Restart now button is there too, in case it
  doesn't") is slightly loose about requiring an explicit click — but this
  exact phrasing already existed pre-diff for the analogous manager-driven
  "Request to pair" case, so this change is extending pre-existing wording
  faithfully, not introducing a new inaccuracy. Not fixed as part of this
  card; a genuine future polish item if picked up.
- **Verdict: safe to merge as-is.**

## Verified beyond automated tests

- Full `go test ./...` (not just `internal/pages`) — all packages green.
- `make docs-shots` actually run (not just the guard checked) — 100
  Playwright screenshots regenerated and passed.
- Manual re-read of all four `multitill.md` locale files' edited paragraph
  for behavioral accuracy against the new code, not just guard-checked
  structural completeness.

## Deferred / out of scope

- The pre-existing help-doc wording imprecision noted above (not
  introduced by this change).
- ut-docs#1611 (a separate, unrelated, explicitly low-severity i18n nit in
  the same general area) — not part of this card's scope.

## Addendum: post-review rebase (2026-09-06, stale-PR sweep, step 0c)

This PR sat open for a couple of hours after the review above with **zero
CI signal** (no `ci`/`UI E2E`/`commit-attribution` run ever started) and a
merge conflict against `main`: PR #806/#1550's "give the joined screen a
real restart" change merged after this branch was cut and independently
added `internal/procrestart`. A stale-PR sweep pass first flagged this as
needing real Dev-level rework and left it as-is; a follow-up pass then
did the rebase and found it was actually mechanical — this branch already
carried `internal/procrestart` via its own base (merged PR #809/#1613),
byte-identical to what #1550/#806 later put on `main`, so
`git rebase origin/main` auto-merged `procrestart.go`,
`procrestart_test.go`, `pairing_restart_test.go` and `pairing_wait.html`
with **zero conflicts**. The only conflict was the generated
`web/help/img/manifest.json` hash, resolved by re-running `make
docs-shots` (which also picked up the `multitill` topic's new hash from
this PR's own help-text edit, plus a couple of bytes of pre-existing
pinned/reused-Chromium screenshot drift in `ar/sell.png`, ut-docs#622 —
unrelated to this change).

No logic changed from what the review above already covered — the diff
against current `main` post-rebase is the same `pairing_join.go` /
`sync_api.go` / `sync_join_restart_test.go` / `pairing_join_success.html`
/ `multitill.md` set, plus the regenerated manifest/screenshot. Re-ran the
full gate on the rebased branch: `go build ./...`, `go vet ./...`,
`gofmt -l .` (none), `golangci-lint run ./...` (0 issues), `go test ./...`
(all packages green), and all 18 `scripts/ci/guard-*.sh` /
`check-brand-assets.sh` checks green. Pushed and let CI run for real this
time before merging, per PR-SWEEP.md's "re-check CI live, right now."
