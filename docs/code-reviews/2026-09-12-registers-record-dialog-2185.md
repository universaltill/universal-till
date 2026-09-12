# Review: Registers admin adopts the record_dialog/list_header pattern (ut-docs#2185)

## What shipped

`web/ui/pages/registers.html` + `internal/pages/registers_page.go` ported
from the old list-plus-inline-form admin pattern to the shared
`record_dialog`/`list_header` dialog (ut-docs#2010), mirroring
`locations.html`/`locations_page.go` (ut-docs#2124, its near-twin) plus one
structural addition: a `location_id` `<select>` in the dialog's fields
slot, ranging over `.root.stockLocations` (`record_dialog.html`'s own `$`
vs `.root` slot-data contract).

- `redirectRegisters`/`renderRegistersDialogError` mirror
  `redirectLocations`/`renderLocationsDialogError` exactly.
  `isHtmxDialogRequest` reused from `categories_page.go` (same package),
  not redefined.
- Destructive-guard semantics correctly NOT copied verbatim from
  Locations: the `POST .../active` handler guards only on
  `CountActiveRegisters<=1` (last-active) — `RegisterInUse` is fetched and
  shown only as an informational row hint, never consulted in the
  active-toggle handler, matching `registerRegisters`'s pre-existing
  "a register with history CAN still be deactivated" doc comment.
- New/removed locale keys in every `web/locales/*.json` (kept
  `registers.new.location_label`/`.location_none`, still used inside the
  dialog's `<select>`). `web/help/en/multitill.md`'s "Registers" section
  rewritten to match the new flow.
- New Go-level htmx-path tests + a new e2e spec
  (`registers-record-dialog-2185.spec.ts`, driven for real, 5/5 pass) —
  covers the `location_id` prefill/round-trip specifically, the one piece
  with no Locations precedent.

## Independent review

Fresh-context Sonnet subagent, isolated worktree (`complexity:easy`
routing).

**Verdict: safe to merge.** One should-fix found and fixed by the
reviewer itself (not just reported): the e2e spec's edit-prefill test had
a silent `if (options.length > 0) { … } else { … }` branch that would
pass vacuously if the seeded fixture ever stopped guaranteeing a real
stock-location option — replaced with a hard
`expect(options.length).toBeGreaterThan(0)` so a future seed-data change
fails loudly instead of leaving the test green while proving nothing.
Re-run for real after cherry-picking the fix back onto this branch: 5/5
pass.

Verified for real: `gofmt`/`go build`/`go vet` clean; `golangci-lint run
./...` (full repo) 0 issues; `shellcheck` 0 issues; full `go test ./...`
0 failures (`internal/pages` 386.5s, including 19/19 Registers tests);
`guard-i18n`/`guard-docs-shots`/`guard-help-topics`/`guard-help-drift`
all green.

**TDD re-verified independently** (reviewer's own worktree, reverted only
`registers_page.go` to `main`, kept the new tests): all 3 new tests fail
with the real pre-fix error (`code=303`); restored, all 3 pass. Matches
this session's own separate independent TDD check byte for byte.

**`location_id` prefill checked by reading the actual mechanism**, not
assumed: `record-dialog.js`'s `setField()` does `el.value = value` for
anything that isn't a checkbox — confirmed this is the standard, correct
way to set a `<select>`'s selection, by reading the file, not
characterizing it.

**Locale/help-topic checks**: locale diff scoped exactly to `registers.*`
across all four files, same shape each, `new.location_label`/
`.location_none` correctly kept (still referenced). `multitill.md`'s new
prose accurately describes the add-icon/tap-to-edit flow.

No secrets, no real client/shop name in test/demo data. No file-write
path in this diff.

## CI note (playwright check, both this card and #2124)

Both PRs' first CI run caught a pre-existing e2e spec neither review pass
had surfaced: `e2e/tests/locations-registers-auth-901.spec.ts` (ut-docs#901)
drives both `/locations` and `/registers` via the old inline-form
selectors, which no longer exist on either converted page. Fixed per-PR
(each PR updates only its own page's describe block in that shared file,
since each branch only converts one page — see that file's own updated
header comment for the ordering note): `/registers`' block updated here,
`/locations`' block updated on ut-docs#2124's own branch. Both fixes
independently verified against a real, freshly-booted local server.

## Safe-to-merge verdict

Yes. Merge via `merge_method: "merge"` (never squash/rebase — ut-docs#250).
