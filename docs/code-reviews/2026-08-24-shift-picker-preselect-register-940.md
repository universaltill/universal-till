# Code review: shift-open register picker preselects this till's own register (ut-docs#940)

**Date:** 2026-08-24
**Author:** autonomous pipeline (Dev: Sonnet, inline; Review: Sonnet, fresh-context subagent — `complexity:easy` per the scrum-master skill's model routing)
**Issue:** universaltill/ut-docs#940

## What shipped

`web/ui/pages/shifts.html`'s shift-open register `<select>` listed every
active register with no preselection. Before ut-docs#894, most shops had
exactly one register, so this was harmless; after #894, multi-till shops
routinely have 2+ active registers, and a cashier opening a shift could pick
the wrong one with no visual cue.

- `internal/pages/shifts_page.go`: resolves this till's own register identity
  via `pos.ResolveTillRegisterID` (same pattern already used by the Settings
  page's own till-register picker, `internal/pages/settings_page.go`), and
  passes it to the template as `TillRegisterID`. `pos.ErrRegisterIdentityAmbiguous`
  (2+ registers, nothing persisted) leaves the picker unselected — no
  guessing past a real ambiguity — any other resolution error is logged and
  otherwise best-effort, never a failed page.
- `web/ui/pages/shifts.html`: the register `<option>` matching
  `TillRegisterID` gets `selected`.
- `web/help/en/reports.md`: one new paragraph in "Cash adjustments & payouts
  (Shifts)" documenting the new default.
- `make docs-shots` re-run; `web/help/img/manifest.json` refreshed.

## Independent review (Sonnet, fresh context, worktree-isolated)

**Initial verdict: NOT SAFE TO MERGE** — one blocking finding, two non-blocking.

### Blocking (fixed)

**`guard-docs-shots.sh` failed** — the `web/help/en/reports.md` paragraph was
added *after* the first `make docs-shots` run, so the manifest's content hash
for `en/reports` was stale. Fixed by re-running `make docs-shots` after all
prose edits landed; guard now passes (`23 routed topics × 4 locales
screenshotted and fresh`).

### Non-blocking (both fixed)

1. **Ordering deviated from the `settings_page.go` precedent.**
   `shifts_page.go` originally called `repo.ListRegisters` *before*
   `pos.ResolveTillRegisterID`, the reverse of `settings_page.go`'s explicit,
   commented ordering. On a brand-new shop's very first shift-open (zero
   registers), `ResolveTillRegisterID` self-creates a register via
   `POSRepo.EnsureRegister` (real name "Default Register", id
   `"reg-default"`) — but with the wrong ordering, the already-captured empty
   `registers` slice meant that register never appeared as a real `<option>`,
   and the page silently fell back to the template's own hardcoded
   `reg-default`/locale-text (`"Default"`) fallback. It only rendered
   correctly by coincidence (the fallback's literal id happened to equal
   `EnsureRegister`'s). **Fixed**: swapped the call order to resolve first,
   list second, matching `settings_page.go` exactly, with a comment
   explaining why. Reviewer also recommended a regression test for the
   zero-register case — added
   (`TestShiftsPage_OpenShiftPickerBootstrapsAndPreselectsOnZeroRegisters`)
   and personally re-verified: reverted just the ordering fix, confirmed the
   new test fails (picker falls back to the locale-text-only option with no
   `selected` and no real register name), restored the fix, confirmed it
   passes.
2. **Untranslated manual addition** (fa/ar/tr `reports.md`). Confirmed
   genuinely environment-blocked: the homelab Ollama translation endpoint
   (`http://192.168.1.231:11434`, `ut-docs/reference/translation.md`) timed
   out from this session (private homelab LAN, unreachable from a cold cloud
   cycle) — same accepted-gap pattern as ut-docs#941/#915. Filed a dedicated
   follow-up card, ut-docs#943, rather than leaving it implicit.

## Verified beyond automated tests

- **TDD claims personally re-verified**, not taken on trust: reverted the
  `selected`-attribute template logic, re-ran
  `TestShiftsPage_OpenShiftPickerPreselectsOwnRegister`, confirmed it failed
  with the expected message, restored, confirmed pass. Same for the ordering
  fix and its own regression test (see above).
- Full gate re-run after all review-driven fixes (not just the specific
  cases named): `go build ./...` clean; `gofmt -l` on both changed `.go`
  files empty; `go vet ./internal/pages/...` clean; `go test ./...`
  (whole module) green, including `internal/pages` (56s, no regressions);
  every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job run
  and green: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh`, `guard-docs-shots.sh`, plus
  `guard-webkit-version.sh`, `guard-kiosk-launch-flags.sh`,
  `guard-android-status-address.sh`, `guard-android-i18n.sh`,
  `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `check-brand-assets.sh`,
  `guard-makefile-version.sh`.
- No real client/shop name anywhere in the diff; no secret-shaped literal
  introduced.
- No new user-facing string was added to any template (`guard-i18n.sh`
  confirms) — the only new user-visible text is the manual prose, handled
  above.

## Safe-to-merge verdict

**Safe to merge** after the fixes above. All CI-blocking guards green, full
test suite green, both TDD claims personally re-verified via real
revert/restore, no scope creep beyond the card's stated requirement and
non-goals (`ResolveTillRegisterID` itself and enrolment-time provisioning
were explicitly untouched, as required).

## Explicitly deferred

- fa/ar/tr translation of the new manual paragraph — ut-docs#943, blocked on
  homelab NAS reachability from a cold cloud session, not on anything in this
  diff.
