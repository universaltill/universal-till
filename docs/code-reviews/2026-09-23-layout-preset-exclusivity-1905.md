# 2026-09-23: Layout preset exclusivity and competitor-naming guard (ut-docs#1905, ADR-0106)

## What shipped

This implements ADR-0106 (`ut-docs/adr/0106-layout-preset-exclusivity-and-naming.md`),
on top of the `layout` type that ADR-0088 already shipped.

- **Convention (Decision B).** A `layout` entry's `config` may carry
  `"role": "preset"`. `internal/uislot/slot.go` adds `RoleField` and
  `PresetRole`, and the amendment parser now accepts `role` as a top-level
  config field.
- **Exclusivity (Decision C).** Only one active plugin may hold
  `role:"preset"`. This mirrors the fiscal-sign exclusive-owner pattern
  (`FiscalSignExclusiveEvents` / `FiscalSignExclusiveOwner` /
  `validateExclusiveHookOwnership`). It is enforced in three places:
  - persist time: `validateLayoutPresetExclusivity`, called from
    `PersistManifest` inside the install transaction;
  - enable time: `setPluginActiveHandler` returns a 409 that names the
    incumbent;
  - rollback: `RollbackManager.Rollback` runs the same check.

  A plugin's own registration is excluded from the check, and a DB error
  fails closed. The queries are `PluginRepo.HasActiveLayoutRole` and
  `ActiveLayoutRoleOwner` in `internal/data`, using JSON1 with a
  `json_valid` guard.
- **Naming rule (Decision D).** New `scripts/ci/guard-competitor-naming.sh`
  and its self-test, wired into `ci.yml`'s `build` job. Two existing
  template comments that quote the product owner carry the reviewed
  `naming-rule:allow` marker. `web/help/img/manifest.json`'s
  `surface_sha256` was regenerated for those comment edits.
- `CLAUDE.md` gained rule lines for both.

## Independent review

The code was built on Fable. This review was done by Opus in an isolated
copy of `feat/1905-layout-preset-exclusivity` at the WIP snapshot
`40639f38`.

### Gate (re-run by the reviewer, after the reviewer's fixes)

- `gofmt -l`: clean. `go build ./...` and `go vet ./...`: clean.
- `golangci-lint run ./...`: 0 issues.
- `go test ./...`: all green.
- `shellcheck` on both new scripts: 0 issues.
- `guard-competitor-naming.sh` on the real tree: passes, 347 files scanned.
- `guard-competitor-naming_test.sh`: 34 assertions pass. The fixtures
  include both kinds of case:
  - should fail: 15 imitation phrasings, one per surface, bare names in a
    layout/theme manifest or locale, the allow-marker in JSON, missing or
    empty directories;
  - should pass: factual competitor mentions, a payment plugin naming the
    provider it integrates, "square"/"toast" as ordinary words, the
    allow-marker in help and UI, and the real tree.
- Other build-job guards touched by the diff are green: data-access,
  i18n, compliance-claims, docs-shots, help-topics, deadcode-baseline,
  page-http-error and kiosk-engine.
- ut-docs `scripts/guard-adr-index.sh`: ok.

### TDD claims re-derived by the reviewer

For each guard, the reviewer disabled only that guard, ran its tests, then
restored the guard and ran them again.

| Guard disabled | Test | Result with the guard disabled |
|---|---|---|
| `PersistManifest` step 0g call | `TestPersistManifest_RefusesSecondActiveLayoutPreset` | FAIL: "installed a second active layout preset" |
| same | `TestPersistManifest_RefusesUpdateNewlyDeclaringLayoutPreset` | FAIL |
| same | `TestPersistManifest_LayoutPresetCheckFailsClosedOnDBError` | FAIL: the error came from the later 0f check, not the preset check |
| `setPluginActiveHandler` `if declaresPreset` block | `TestLayoutPresetExclusivity_SecondActivePresetRefused` | FAIL: got 200, plugin enabled |
| same | `TestLayoutPresetExclusivity_SelfAndUnownedSucceed` | FAIL: got 200 on re-enabling A while B holds the role |
| `setPluginActiveHandler` `presetErr` branch | `TestLayoutPresetExclusivity_EnableFailsClosedOnDBError` | FAIL: got 200 |
| `Rollback` preset check | `TestRollback_RefusesRestoringASecondActiveLayoutPreset` | FAIL: "rollback restored a role:\"preset\" entry…" |

Every test passed again once its guard was restored.

### Adjudication 1: should `role` accept any string? No. Fixed (Medium)

As first drafted, ADR-0106 B and the parser treated any `role` string other
than `"preset"` as an ordinary shared amendment. So a manifest with
`"role": "perset"` or `"Preset"` installed cleanly as a normal layout
plugin, dropped out of mutual exclusivity, and could sit active next to
another preset with no error anywhere. That is exactly the incoherent state
Decision C exists to prevent.

The same parser already refuses an unknown config key, including `"rol"`,
and an unknown `slot` value. Accepting any role value was the one
exception to that rule.

The "a future role might need it" argument doesn't hold up:

- A new role needs its own ADR and parser change anyway.
- An older till already refuses a manifest with an unknown field, so
  refusing an unknown value is consistent with that.
- Tightening costs nothing: before this branch, `role` was itself an
  unknown field and was refused, so no installed row can carry any other
  value. No shipped manifest or fixture sets `role` either.

**Fix:**
- `internal/uislot/slot.go` `parseSlotAmendments`: when `role` is present
  it must be exactly `"preset"`. Anything else is refused at install
  (through `validateLayoutEntries`, which covers both `PersistManifest` and
  `Rollback`). The error names the field, shows the bad value and lists the
  supported value.
- Tests:
  - `TestParseAmendmentsJSON_RoleMustBeAString` became
    `TestParseAmendmentsJSON_RoleMustBePreset`. Its cases: typo, wrong
    case, another word, empty, number, null, padded, plus the existing
    misspelled-key case.
  - The "non-preset role is an ordinary amendment" assertion became "no
    role is an ordinary amendment".
  - New `TestPersistManifest_RefusesUnknownLayoutRoleValue`: a
    `role:"perset"` install next to an active preset is refused and leaves
    no row.
  - TDD: all five string sub-cases and the persist test fail against the
    pre-fix parser, and pass after the fix.
- Docs, updated in the same change:
  - ADR-0106 Decision B, rewritten and noted as tightened by this review
    before merge. The ADR isn't merged yet, so this is an amendment within
    the same card, not a supersession.
  - ADR index row.
  - `ut-docs/architecture/plugin-architecture.md` "Layout presets".
  - `ut-docs/reference/plugin-manifest.md`.
  - The `CLAUDE.md` rule line.

### Adjudication 2: third enforcement point in `rollback.go`. Kept, and ADR updated (Low, doc drift)

`Rollback` is reachable from `POST /api/plugins/{id}/rollback`, which the
plugin manager's per-version "roll back" button calls. Cloudsync's
automatic rollback after a failed update (`cloudsync_wire.go`) calls it
too. It only runs on an active plugin (`GetActivePluginVersion` requires
`is_active = 1`). It rewrites `plugin_entries` from the on-disk prior
manifest through `ReplacePluginEntries`, without going through
`PersistManifest`.

So if a plugin's current version is an ordinary layout but its target
version declared `role:"preset"`, rolling back would create a second active
preset. The rollback test proves this: with the check removed, the rollback
succeeds. The check is necessary.

It also follows established precedent: `Rollback` already re-runs the
page-key, route, payment-key and ADR-0088 `validateLayoutEntries` checks
for the same reason. The fiscal-sign group has no rollback check because
`Rollback` never rewrites hooks. That is also why "same two enforcement
points as fiscal-sign" was an incomplete description for an entry-based
marker.

`ReplacePluginEntries` has only two callers (`PersistManifest` and
`Rollback`), and `is_active` only goes 0→1 through `SetPluginActive` in the
enable handler. With these three points, every path that can activate a
preset is covered.

**Fix:**
- ADR-0106 Decision C now lists **Rollback** as a third enforcement point
  and explains why fiscal-sign doesn't need one.
- The plugin-architecture doc, the manifest reference, the ADR index row
  and `CLAUDE.md` all mention rollback.
- Also corrected Decision E's path: `universal-till/architecture/...` →
  `ut-docs/architecture/plugin-architecture.md`, which is where the
  subsection actually lives.

### Other findings

- **Low, accepted.** The 409 and 500 messages are raw English API strings,
  not i18n keys. The plugin manager's generic `act()` handler shows
  `j.error` verbatim. This is the same as the fiscal-sign 409 beside it,
  and `guard-i18n.sh` is green. Localizing plugin-manager API errors would
  be its own card and doesn't belong in this one.
- **Low, accepted.** The enable-time check runs outside a transaction (a
  check-then-set gap). This is identical to the fiscal-sign precedent. It
  is an admin-only action on a single till, and persist time checks inside
  the transaction.
- **Info.** The guard's documented gaps are acceptable for a denylist
  check: "unlike SumUp" matches, and phrases split across lines or tags
  are missed.

## Confirmed clean

- **No new UI screen.** The 409 is JSON-only (`{"data":null,"error":…}`)
  and appears through `web/ui/pages/plugins.html`'s existing `act()` error
  message. No template or locale key was added. No preset ships yet, so no
  shop-owner-visible behaviour changes and no help topic is due. The
  concrete presets are ut-docs#2457.
- **No money, offline or kiosk impact.** The preset query only runs on
  install, enable and rollback, and only when the plugin declares the
  role.
- **Recurring bug classes:** none. There are no production file writes, and
  the test fixtures use `t.TempDir()` with `os.MkdirAll`. No cwd-relative
  paths: rollback callers use `paths.Plugins()`.
- **Repository pattern:** the new SQL lives only in `internal/data`.
- **Test data** is generic (`com.test.*`, `com.example.*`, "RB Layout").
  No real shop or client names.
- **Secrets:** none.

## Deferred / not this card

None new. The ADR's own deferral stands: concrete Familiar/Classic/Compact
presets are ut-docs#2457, gated on #1906.

## Verdict

Safe to merge, with the reviewer's fixes above included. The ut-docs ADR
branch must carry the matching ADR-0106 and doc edits.
