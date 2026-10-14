# Review — upsert can't turn an additional till into a kiosk (ut-docs#4263)

**Shipped:** `POST /api/settings/upsert` now refuses `key=display.mode&value=self_order`
on an additional joined till (`ownTillRole == additional`) with a 400 and the new
key `settings.display.additional_kiosk_use_profile` ("use Settings → Device
profile — it asks first and tells the main till"). The ut-docs#2781 satellite
gate and the new one share one `ownTillRole` read, both before the elevation
gate (a refusal never costs an approver's PIN). Main/standalone tills and
satellites behave as before. Key added to en/ar/fa/tr; de/es/pt follow in their
language-pack PRs.

**Test (TDD):** `TestUpsertDisplayMode_AdditionalTillCannotSideStepToSelfOrder`
(`internal/pages/till_role_test.go`): 400 + specific message; `display.mode`
unchanged; own role and the main till's roster still `additional`; no
`role_changed` audit on either side; an additional till can still upsert
`register`; a satellite can still upsert `self_order`. Before the fix it failed
with `204, want 400`.

**Independent review** (different model from the author, fresh context, own worktree):
- Re-verified the TDD claim: reverting only the `settings_page.go` hunk fails
  the test with `204 … want 400`; restored, it passes.
- Gate placement before any side effect/elevation confirmed; `value` is trimmed
  before the gate and every consumer compares exactly with `"self_order"`, so
  case/whitespace variants can't reach kiosk mode.
- `ownTillRole`'s unknown → `additional` fallback matches the dedicated
  handler; a blocked upsert only redirects to the working Device profile path.
- Findings:
  1. **Major, out of scope** — kiosk-user PIN sign-in (`auth_page.go` ~209)
     applies `self_order` with no role check. → ut-docs#4295.
  2. **Minor/major, out of scope** — cloud `set_setting` writes `display.mode`
     on an additional till (and a satellite) with no role gate
     (`cloudsync_wire.go` ~1186). → ut-docs#4295.
  3. Nit, pre-existing — upsert doesn't validate the `display.mode` enum.
     Accepted (dead string, not a bypass); noted on ut-docs#4295.
  4. Nit — explanatory comment sat on the helper, not the test. **Fixed.**
  5. Nit — tr text lacked an object ("Bu kasayı …"). **Fixed.**
- No file writes or paths in the diff (`os.MkdirAll` / `paths.Data` N/A).

**Gate:** gofmt clean, `go vet ./...`, `golangci-lint` 0 issues, full
`go test ./...`, guards (data-access, i18n, compliance-claims,
competitor-naming, core-neutral, kiosk-engine, no-showmodal, help-topics,
help-drift, page-http-error, card-data-schema, netaccess, no-inline-handlers).
API-only change: no UI surface or help topic changes (Device profile flow
already documented in the multitill topic).

**Verdict:** safe to merge. `lang-pack-drift` goes red on `main` until the
de/es/pt pack PRs land (same cycle).
