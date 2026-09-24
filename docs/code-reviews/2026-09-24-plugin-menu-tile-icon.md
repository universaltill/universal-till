# 2026-09-24: A plugin page declares its own menu-tile icon (ut-docs#1734)

## What shipped

Branch `feat/1734-plugin-menu-tile-icon`, commit `590ef4c` (Dev, Sonnet),
plus the reviewer's fixes listed below. These fixes are in the working tree
and are not committed yet.

- **Manifest.** `plugins.ManifestEntry` gains `IconName` (`icon_name`). It
  applies only to `type:"page"` entries and must be a name from core's
  drawn-icon set. `IconPath` is now documented as button-only.
- **Install-time validation.** `validatePageEntryIcon` refuses an unknown
  name in both `PersistManifest` (step "0h.") and `Rollback`. It runs before
  anything is written.
- **Persistence without a migration.** `entryIconColumn()` writes a page
  entry's `IconName` into the existing `plugin_entries.icon_path` TEXT
  column. Every other entry type still writes `IconPath` there. Both write
  loops share this helper, so they cannot drift apart.
- **Read and render.** `ListMenuEntries` selects `COALESCE(icon_path,'')`
  into `MenuEntryRow.IconName`. The value flows through `MenuPlugin.Icon`,
  then `common.MenuItem.Icon`, then `menuSlotEntries`, and finally
  `menuIcon`. `menuIcon` → `httpx.Icon` is a bounded map lookup, and an
  unknown name falls back to `puzzle`.
- **Import-cycle workaround.** `internal/uislot/icon_names.go` holds the
  closed name set. `internal/plugins` cannot import `internal/httpx`
  because httpx already imports plugins. A mirror test pins this set to
  `httpx.IconNames()`.

## Independent review

Reviewed by Opus. The code was written on Sonnet. The reviewer read the
full diff, re-ran the gate, and checked the cross-repo contracts.

### F1 (High, blocking): the ut-cloud signing mirror was missing. Fixed in ut-cloud

The ut-cloud `CLAUDE.md` requires `internal/signing.CanonicalEntry` to
mirror `plugins.ManifestEntry` field for field. The marketplace signer
round-trips every uploaded manifest through that struct. As a result:

1. `POS_DIR=…/universal-till go test ./internal/signing/ -run
   TestCanonicalManifestMirrorsPOS` fails against this branch:
   `field #4 name mismatch: POS ManifestEntry has "IconName", cloud
   CanonicalEntry has "SortOrder"`. ut-cloud's `manifest-contract-guard`
   would go red on its first build after this merges.
2. **Functional impact:** the signer strips `icon_name`. Every
   marketplace-signed plugin would install and verify cleanly, but its tile
   would silently show `puzzle`. The feature would only work for unsigned
   dev installs.

**Fix:** a ut-cloud working-tree change on branch
`feat/1734-canonical-entry-icon-name` (not committed). It adds
`CanonicalEntry.IconName` (`json:"icon_name,omitempty"`) right after
`IconPath`, plus `TestSignManifestPreservesPageEntryIconName`. That test was
seen failing first ("signed manifest entry is missing \"icon_name\"") and
passes after the fix. `scripts/ci/verify.sh` exits 0 with
`POS_DIR` pointing at this branch, and the contract test is green.
Existing signed manifests are unaffected: `omitempty` means a manifest
without `icon_name` marshals to the same bytes, so its signature still
verifies. **Merge order:** merge this POS PR first, then the ut-cloud PR
straight away. Its contract guard checks against POS `main`.

### F2 (Medium): the precedence claim described a path production cannot reach. Comments fixed

The `ManifestEntry.IconName` doc, the `menu_page.go` comment and the commit
message all say a layout plugin's Decision H re-icon amendment "still wins"
over a plugin tile's declared icon. In practice no installed plugin can do
this:

- `uislot.ParseAmendmentsJSON` refuses any amendment key that is not a core
  menu destination: "key %q is not a core menu-slot destination".
- A page whose route matches a core key becomes that core entry in
  `menuSlotEntries`, and its own icon is ignored.

`TestMenuPage_LayoutAmendmentStillWinsOverPluginDeclaredIcon` injects the
amendment directly into `dp.MenuAmendments`. It is a valid defensive test of
`uislot.Resolve` ordering, but not a production scenario. The reviewer
rewrote all three comments to say so accurately. The test is kept, and its
doc comment now labels it defensive. The commit message wording should be
softened when this is committed.

### F3 (Medium, blocking in CI): the docs-shots surface hash was stale. Fixed

`590ef4c` changes `internal/pages/menu_page.go`, which is part of the
docs-shots surface, but did not refresh `web/help/img/manifest.json`, so
`guard-docs-shots.sh` failed. No docs-shots fixture declares `icon_name`,
so no screenshot pixel changes. The reviewer ran
`scripts/ci/update-docs-shots-surface-hash.sh`, and the guard now passes.
**The commit must carry the `Docs-Shots-Unchanged: true` trailer**, as the
script instructs.

### F4 (Low): the exported mutable map gated validation. Fixed

`uislot.KnownIconNames` was an exported `map[string]bool`, so any package
could add or remove names at runtime and change what install accepts. It is
now the unexported `knownIconNames`, reached through `IsKnownIconName`.
First attempt: an exported sorted-slice accessor. That tripped
`guard-deadcode-baseline.sh` because only a test called it. The mirror test
therefore moved to `internal/uislot/icon_names_test.go` (package
`uislot_test`, which may import httpx without a cycle), using a test-only
`export_test.go` accessor. The old test in `internal/httpx/icons_test.go`
was removed. The new test compares the two sorted lists exactly, in both
directions. Its teeth were checked: removing `"eye"` from the set makes it
fail, and restoring the name makes it pass.

### F5 (Low): legacy rows reuse the column. Documented and pinned by a test

Before #1734, `PersistManifest` wrote a page entry's `icon_path` file path
verbatim into the same column that `ListMenuEntries` now reads as an icon
name. `ut-cloud`'s own signer fixture has
`"type":"page", "icon_path":"assets/icon.png"`, so such rows exist. They
are harmless, because the value is only ever used as a key into httpx's
bounded map. The `MenuEntryRow.IconName` doc used to say this column is
"never a file path", which is false for legacy rows, and it now says so.
The reviewer also added `TestMenuPage_LegacyPageIconPathFallsBackToGeneric`.
It stores `../../assets/icon.png"><script>x</script>` in a page row's
`icon_path` and asserts that none of it reaches `/menu` and that the tile
renders `puzzle`.

### Accepted, not changed

- **Import-cycle design.** A cleaner design would move `railIcons` (the SVG
  bodies) into a leaf package that both httpx and plugins import, giving
  one source of truth with no mirror. That refactor is larger than this
  card. The mirror test now fails loudly on drift in either direction, so
  the duplication is a maintenance cost, not a silent risk. Worth a backlog
  card.
- **Step "0h." numbering.** The lettering in `PersistManifest` is already
  chronological rather than positional (0g, 0f and 0e appear in reverse
  order). "0h." sits next to the other page-entry validators (0b and 0c),
  which is the logical spot. Consistent with existing practice.
- **`icon_name` on a non-page entry, and `icon_path` on a page entry, are
  silently ignored.** This matches the documented page-only and
  button-only contract. A loud refusal would be stricter but is not needed
  for safety.
- **Concurrent installs.** The validator reads no data and is pure, so it
  has no race.
- **Security sink review.** The only sinks are `httpx.Icon(name)` (a map
  lookup that returns a constant SVG or "") and `data-icon` inside that
  constant SVG. No plugin string reaches a file path, URL or template
  unescaped. `/plugin-icons/…` reads only `ListButtonEntries`
  (`type='button'`), so a page's icon name never reaches the file-serving
  route.

### TDD claims, re-verified by the reviewer

The reviewer disabled the `validatePageEntryIcon` call in `PersistManifest`
and turned `entryIconColumn` back into pass-through behaviour. Four of the
five `page_icon_validation_test.go` tests then failed. The one that stayed
green is the button-unaffected control, which is correct. After the code was
restored, all five passed.

### Gate (re-run by the reviewer on the final tree)

- `gofmt -l .`: empty.
- `go build ./...`: ok.
- `go vet ./...`: ok.
- `golangci-lint run ./...`: 0 issues.
- `go test ./...`: exit 0, 60 packages ok.
- Every guard in `ci.yml`'s `build` job (65 commands) passes. The exceptions
  are `shellcheck scripts/ci/*.sh` and `guard-shellcheck-version.sh`: the
  shellcheck binary is not installed in this container, which is
  environmental and fails identically on `main`. This change touches no
  shell script.
- ut-cloud: `POS_DIR=/home/user/universal-till scripts/ci/verify.sh`
  exit 0.

### Beyond automated tests

- **Repository pattern.** SQL stays in `internal/data`. The new test SQL is
  in a `_test.go` file, which the guard exempts.
- **No migration.** The column already exists, so ADR-0100 is untouched.
- **i18n.** No user-facing strings.
- **Offline-first.** No new network use.
- **Help manual.** This is not a shop-owner workflow change: a plugin's
  tile shows a different drawn glyph. No `web/help` topic is affected.
- **Docs.** ut-docs `reference/plugin-manifest.md` is updated on branch
  `docs/1734-plugin-manifest-icon-name` (working tree): a new `icon_name`
  row, `icon_path` marked button-only, and the Decision H `icon` row no
  longer says plugin icons are #1734's open scope. `README.md` needs no
  change. `docs/data-model.md` lists the column only.
- **No real client names and no secrets** in test data.

## Deferred / not this card

- The ut-cloud marketplace does not validate `icon_name` at publish time,
  so an author finds a typo only when a POS install refuses it. Backlog
  candidate.
- A backlog candidate to move the icon SVG set into a leaf package and
  remove the mirror (see "Accepted").

## Verdict

**Safe to merge once the reviewer's fixes are committed and the ut-cloud
mirror PR (F1) lands right after it.** Without F1, the feature silently
does nothing for any marketplace-signed plugin, and ut-cloud CI goes red.
The core design (closed enum, install-time refusal, bounded render lookup)
is sound, and no injection surface was found.
