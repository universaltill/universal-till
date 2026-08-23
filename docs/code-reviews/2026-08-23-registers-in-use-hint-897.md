# Code review: surface RegisterInUse as a non-blocking hint on the Registers admin page

- **Card**: universaltill/ut-docs#897
- **Branch**: `pipeline/897-registers-in-use-hint`
- **Complexity**: easy
- **Reviewer model**: independent read (this session), re-deriving the diff
  from `git diff main HEAD` rather than trusting the WIP commit's own
  reasoning

## What shipped

`POSRepo.RegisterInUse` (`internal/data/pos_repo.go`) was added alongside
the Registers admin page (#651) but shipped unreferenced/dead in
production — nothing ever called it. This surfaces it, informationally
only, on `GET /registers`:

- `internal/pages/registers_page.go`: `registerView` gets a new `InUse
  bool` field; the render loop in `renderRegisters` calls
  `posRepo.RegisterInUse(ctx, reg.ID)` per register and sets it. A doc
  comment on `registerView` and the existing comment above
  `registerRegisters` both point at the POST `.../active` handler as the
  place that explains why this must never gate deactivation.
- `web/ui/pages/registers.html`: each row's status cell gets a
  conditional `<span class="muted">{{ T "registers.in_use_hint" }}</span>`
  under the active/inactive label, reusing the page's existing `muted`
  token (no new hardcoded color/spacing).
- `web/locales/{en,ar,fa,tr}.json`: new key `registers.in_use_hint`
  ("Has shift/sale history" / ar / fa / tr equivalents).
- `web/help/{en,ar,fa,tr}/multitill.md`: one sentence added to the
  existing Registers bullet describing the hint and that it's
  informational-only.
- `internal/pages/registers_page_test.go`:
  `TestRegistersPage_ShowsInUseHintForRegisterWithHistory` — creates two
  registers, seeds a completed sale against one, asserts the hint appears
  in that register's own table row and not the other's.

The deactivation handler (`POST /api/registers/{id}/active`) is
**unmodified** by this diff — it still guards only on
`CountActiveRegisters` (last-active-register), never on `RegisterInUse`,
per the deliberate #651 design already documented in the handler's own
comment.

## Independent review — findings and resolution

1. **Real gap (fixed): `guard-docs-shots.sh` failed.** The diff touches
   `web/ui/pages/registers.html` and `web/help/*/multitill.md` without
   regenerating `web/help/img/manifest.json` via `make docs-shots` — a
   CI-blocking guard per `CLAUDE.md`'s "Before committing" list. This is
   the same class of gap the neighboring `#896` review record hit and
   documented: `registers.html` is part of the guard's whole-surface hash
   even though no *screenshotted* topic's `routes[0]` is `/registers`.
   Fixed by running the docs-shots harness directly
   (`bash e2e/scripts/docs-shots.sh`, after `npm ci` in `e2e/` and
   confirming a pre-installed, smoke-test-launchable Chromium at
   `/opt/pw-browsers/chromium` — version 141.0.7390.37 vs. the
   `@playwright/test` pin's expected 149.0.7827.55, a non-fatal warning
   per `resolve-chromium.sh`'s own design, ut-docs#622): all 84 Playwright
   screenshot tests passed, `web/help/img/manifest.json` regenerated.
   `guard-docs-shots.sh` now passes. The run also produced incidental
   diffs to `web/help/img/{en,ar,fa,tr}/{alerts,designer}.png` (each
   within a few hundred bytes of the previous version, same dimensions)
   — unrelated re-render drift from re-running the full Playwright suite,
   not caused by this diff, same as the `alerts`/`designer`/`sell` drift
   the `#896` review already saw and accepted; `multitill.png` itself is
   untouched, since `/registers` isn't a screenshotted route. (The
   surface/manifest hash quoted in the first fix pass is superseded by
   the rebase in finding 2 below, which re-ran `make docs-shots` a second
   time against the final base — see that finding for the actual
   committed hash.)
2. **Real gap (fixed): `main` had moved since the WIP commit's base,
   creating a genuine merge conflict, not just a stale-branch warning.**
   `PR #447` ("allow changing a register's stock location after
   creation", ut-docs#895) merged to `main` after this branch's base
   commit and touched the exact same three files this diff touches
   (`internal/pages/registers_page.go`, `registers_page_test.go`,
   `web/ui/pages/registers.html`), plus the same `multitill.md`
   sentence and the same screenshot/manifest artifacts. `git push`
   succeeded (a feature branch doesn't need to be up to date with `main`
   to push), and the branch's own build/tests/guards all had already
   passed *in isolation*, but the opened PR came back
   `"mergeable_state":"dirty"` and never got a single CI check run —
   both symptoms of the same underlying conflict, not something the
   pre-push local gate could have caught since it never diffed against
   `origin/main`'s current tip. Fixed by `git rebase origin/main` and
   resolving each conflict by hand: `registerView` now carries both
   `#895`'s `LocationOptions`/`LocationValue` fields and `#897`'s `InUse`
   field; the render loop's `RegisterInUse` call was folded into `#895`'s
   single per-register loop (the two branches had each independently
   written their own version of that loop) rather than left as a second,
   duplicate loop; the `multitill.md` sentence-level conflicts in all
   four locales were merged so both the stock-location-change sentence
   and the in-use-hint sentence survive, back to back; the screenshot/
   manifest binary conflicts were resolved by taking `main`'s copies
   (`git checkout --ours` during a rebase means the upstream/`main` side)
   and then regenerating fresh via `make docs-shots` against the final
   rebased tree (84/84 Playwright tests passed again; final surface hash
   `711b504040f1…`, all four `multitill` topic hashes updated). Full
   gate — `gofmt`/`build`/`vet`/`go test ./...`/all 16 guards — re-run
   and green against the rebased HEAD before pushing.
3. **No findings** on the core acceptance criteria: deactivation behavior
   verified unconditional on in-use status (see TDD re-verification
   below and the direct code read of the POST handler, confirmed again
   post-rebase); no raw SQL introduced (`RegisterInUse` itself is
   pre-existing, untouched — the diff only calls it, from inside
   `internal/pages`, which is fine since the call, not the query text,
   lives there); i18n key present and genuinely translated in all four
   locale files (see below); manual updated accurately in all four
   locales, including after the rebase merge; test checks the hint
   per-row, not merely "somewhere in the page" (see below); no real
   client/shop name anywhere in the diff (test registers are "Busy
   Till"/"Idle Till"/"History Till"/"Other Till", all synthetic); no
   file-write handler involved at all, so the `os.MkdirAll` /
   `paths.Data(...)` recurring bug classes don't apply here.
4. **Minor, deferred (not fixed): one extra DB round-trip per register
   per page render.** `RegisterInUse` is now called once per register
   inside the existing per-register loop (the same loop that already did
   an O(1) map lookup for `LocationName`), so `GET /registers` now issues
   N extra `EXISTS` queries where N = register count. Left as-is: this is
   a low-cardinality admin page (a shop typically has a handful of
   registers), the query itself is a cheap indexed `EXISTS`, and a batch
   `RegisterInUseMap`-style repo method would be new API surface beyond
   what #897 asked for. Worth a follow-up only if register counts ever
   grow large enough to matter, which isn't expected for this product.

## i18n / translation verification (read the actual JSON, not just the guard)

- `en`: `"registers.in_use_hint": "Has shift/sale history"`
- `ar`: `"لديه سجل ورديات أو مبيعات"` — "it has a record of shifts or
  sales", correct register/gender agreement, reads as natural Arabic
  admin-UI prose, not a transliteration or copy-paste.
- `fa`: `"دارای سابقهٔ شیفت یا فروش"` — "has a history of shift or sale",
  matches the existing Farsi wording pattern used elsewhere in the same
  file for "history" (`سابقه`/`سابقهٔ`).
- `tr`: `"Vardiya veya satış geçmişi var"` — "There is shift or sale
  history", idiomatic Turkish, consistent with `registers.error.*`
  strings' register/tone in the same file.

All four are genuine, distinct translations of the same meaning — none
is English left untranslated or obviously machine-garbled.

## TDD re-verification performed personally

Re-derived independently rather than trusting the WIP commit's own
claim. Working in an isolated worktree already checked out to a local
branch built from the WIP commit (`review-897-registers-in-use-hint`,
based on `62ed8892`), so the revert-then-restore below never touched a
shared checkout.

**Before (feature disabled)** — edited `renderRegisters`'s loop to force
`v.InUse = false` unconditionally instead of using the real
`RegisterInUse` result, then ran just the new test:

```
=== RUN   TestRegistersPage_ShowsInUseHintForRegisterWithHistory
    registers_page_test.go:246: Busy Till (has a sale) must show the in-use hint, row: Busy Till</td>
                  <td><span class="muted">None</span></td>
                  <td>
                    active

                  </td>
                  ...
--- FAIL: TestRegistersPage_ShowsInUseHintForRegisterWithHistory (0.04s)
FAIL
FAIL	github.com/universaltill/universal-till/internal/pages	0.050s
```

Real, specific assertion failure — the hint is genuinely absent from
"Busy Till"'s row when the feature is disabled, not a vacuous pass.

**After (reverted to the shipped code)** — restored the file from a
pre-edit backup, confirmed `git diff` against the file showed no
changes, then re-ran the full registers-page test suite:

```
=== RUN   TestRegistersPagePermissions
--- PASS: TestRegistersPagePermissions (0.01s)
=== RUN   TestRegistersPage_ShowsStrandWarning
--- PASS: TestRegistersPage_ShowsStrandWarning (0.03s)
=== RUN   TestRegistersPageCreate_WhitespaceOnlyNameRejected
--- PASS: TestRegistersPageCreate_WhitespaceOnlyNameRejected (0.01s)
=== RUN   TestRegistersPageCreateRenameDeactivate
--- PASS: TestRegistersPageCreateRenameDeactivate (0.01s)
=== RUN   TestRegistersPage_DeactivateWithHistoryIsAllowed
--- PASS: TestRegistersPage_DeactivateWithHistoryIsAllowed (0.01s)
=== RUN   TestRegistersPage_ShowsInUseHintForRegisterWithHistory
--- PASS: TestRegistersPage_ShowsInUseHintForRegisterWithHistory (0.01s)
=== RUN   TestRegistersPage_CannotDeactivateLastActiveRegister
--- PASS: TestRegistersPage_CannotDeactivateLastActiveRegister (0.01s)
PASS
ok  	github.com/universaltill/universal-till/internal/pages	0.111s
```

`TestRegistersPage_DeactivateWithHistoryIsAllowed` — a pre-existing test,
untouched by this diff — independently confirms the deactivation
behavior itself: a register with a seeded completed sale is still
successfully deactivated via `POST /api/registers/{id}/active`, exactly
the invariant #897 requires stay unchanged.

## Verification performed personally (not just trusting the diff/tests)

- `gofmt -l .` — clean.
- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test ./...` — full suite green, every package.
- All 16 CI-blocking guards from `CLAUDE.md`'s "Before committing" list,
  run individually: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-docs-shots.sh` (failing until
  fixed, see above), `guard-help-topics.sh`, `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `check-brand-assets.sh`,
  `guard-makefile-version.sh` — all pass. Re-run in full a second time
  after the `main` rebase (finding 2), against the final rebased HEAD —
  still all pass.
- After the rebase, re-ran the full `registers_page_test.go` suite
  (now 8 tests, `#895`'s and `#897`'s side by side) and confirmed both
  features' own tests pass together with no interaction:
  `TestRegistersPage_ChangeLocationAfterCreation` (from `#895`) and
  `TestRegistersPage_ShowsInUseHintForRegisterWithHistory` (from this
  card) both green in the same run.
- Read the POST `/api/registers/{id}/active` handler directly (not on
  faith): it calls `posRepo.CountActiveRegisters` and
  `posRepo.SetRegisterActive` only; no reference to `RegisterInUse`
  anywhere in that handler or elsewhere in the file outside the render
  loop.
- Confirmed `RegisterInUse` itself is untouched by this diff (only its
  call site is new), so no new raw SQL was introduced anywhere outside
  `internal/data`.
- Read the actual JSON for all four locale files and judged the
  translations as genuine (see i18n section above), not just trusted
  `guard-i18n.sh`'s key-parity check.
- Read all four `multitill.md` diffs: each adds one sentence to the
  existing Registers bullet, describing the hint and its
  informational-only nature, consistent with what the code actually
  does (never gates deactivation).
- Confirmed no real client/shop name and no secret-shaped literal
  anywhere in the diff.

## CI on the opened PR (informational, not a finding against this diff)

The pushed PR's `build` job (full `go test ./...` + all guards) failed
twice in a row, both times on the same pre-existing, unrelated test:
`TestReload_SurvivesRealisticPublisherContention` in
`internal/plugins/reload_busy_production_test.go`, which itself
self-documents as "the ut-docs#775 production-risk signal, not a flake
to retry away" — a genuine SQLite-lock-contention (`SQLITE_BUSY`) risk
under concurrent plugin reload, tracked separately, and unrelated to
anything `#897` touches. Evidence this is not caused by this diff:

- This diff never touches `internal/plugins`, wazero/wasm runtime code,
  or any SQLite connection/pragma configuration.
- Every guard in the same CI job passed, including `guard-docs-shots.sh`
  reporting the exact same surface hash (`711b504040f1…`) computed
  locally, and `gofmt`/`guard-makefile-version.sh`'s full `go build`.
- Locally, the full `go test ./...` (including `internal/plugins`)
  passed clean on this exact rebased tree, run twice.
- Running `TestReload_SurvivesRealisticPublisherContention` in isolation
  locally 5 times in a row: 5/5 pass.
- On CI, both failures hit the same `SQLITE_BUSY` contention path but at
  different points in the reload sequence ("built-in invariant" vs.
  "deactivate"), consistent with a timing/load-sensitive race that is
  more exposed on a busier shared CI runner than on this local sandbox,
  not a deterministic break introduced by this branch.
- One re-run of the failed job was triggered (`rerun_failed_jobs`) to
  rule out one-off runner noise; it failed the same way a second time.
  No further retries were attempted — the test's own message explicitly
  asks not to treat it as a flake to retry past, so this is reported
  as-is rather than retried until green.

This is a pre-existing gap in the shared CI environment's plugin-reload
concurrency handling (tracked as ut-docs#775), out of scope for `#897`,
and not touched or worsened by this diff. It is called out here rather
than silently retried or worked around.

## Safe-to-merge verdict

**The diff itself is safe to merge; CI on the PR is not fully green for
a reason unrelated to this diff.** Two real, in-scope gaps were found in
the diff and fixed directly: `guard-docs-shots.sh` failing on the
original submission, and a genuine merge conflict against `main` (which
had moved on with the overlapping `#895` register-location-edit feature)
discovered only once the PR was opened against the real, current `main`
— both resolved with the full local gate re-run green afterward, twice.
Everything else checked out clean on independent re-derivation,
including the core correctness requirement (deactivation stays
unconditional on in-use status) proven both by direct code reading and
by a from-scratch TDD revert-then-restore rather than by trusting the
original claim. The one open item is the PR's `build` check, which fails
on a pre-existing, unrelated, already-tracked flaky test (see "CI on the
opened PR" above) — not a defect in this diff, but real enough that this
record does not claim a fully green CI run, and the merge decision on
that basis is left to the human/orchestrator rather than forced through.
