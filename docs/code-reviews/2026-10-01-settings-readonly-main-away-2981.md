# Review: Settings shows shop-wide settings read-only while the main till is unreachable (ut-docs#2981)

- Date: 2026-10-01
- Branch: `fix/2981-settings-readonly-main-away`
- Author lane: lane:cloud-54 (Opus 5.5 build). Independent review: Fable, fresh context. Driven test: separate Opus subagent.

## What shipped
- `internal/pages/settings_shopwide_lock.go`: `shopWideLockRegions` maps 21 Settings regions to the shop-wide key each one's handler writes. `lockedSettingsRegions` locks all of them only when this till follows a main till (`tillFollowsMain`) and the cached link view says `unreachable` (`replicaLinkView`). That view is the status-bar chip's own derivation and needs no network probe, so the page stays offline-first.
- `web/ui/pages/settings.html`: each region sits in `<fieldset class="set-lock" disabled>` with a note. The note reuses the existing key `settings.error.main_till_unreachable`, so there is no new locale key and no language-pack follow-up. Per-till regions stay editable: theme, the Display card, printer, and this till's name and register.
- `web/public/app.css`: `.set-lock` uses `display: contents`, so a main till's layout is unchanged. `.set-lock-note` uses the warning-tint tokens and logical properties.
- The server-side write-through refusal (`settings_sync_proxy.go`) is unchanged. Only its header comment was updated.
- Help: `web/help/{en,de,fa,tr,ar}/multitill.md` step 13. `make docs-shots` regenerated the manifest.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| S1 | should-fix | WIP commit, no review record | Fixed: this record, plus a real commit message |
| S2 | should-fix | The `#settings-all` raw editor stays editable, which contradicts the new help sentence | Help sentence amended in all 5 locales: the all-settings list stays editable, and a shop-wide value changed there is refused the same way. The editor mixes per-till and shop-wide rows, so locking all of it would be wrong. Per-row locking is not in this card. |
| S3 | should-fix | The "Resume import" `<a>` sat inside the locked restore-prompt fieldset (a disabled fieldset doesn't disable anchors) | Fixed: the link moved outside. Only the Dismiss button (the settings write) is locked. |
| T1 | minor (Tester) | The fieldset wrapper stopped margin collapsing, so 6 cards grew by 6–11px on a main till | Fixed: `display: contents`, checked in the browser by the Tester (boxes identical, `disabled` still applies) |
| N1 | nit | `index` on a missing `lockedRegions` would error if another handler ever rendered settings.html | Accepted: GET /settings is the only renderer, and the map is always non-nil |
| N2 | nit | `replicaLinkView` also computes update-follow inputs (local reads, plus a disk probe when behind the main till) | Accepted: this cost already exists on the status-chip path. No network involved. |
| N3 | nit | The lock is decided at render time, so the page stays locked after the main till returns until a reload | Accepted. The opposite case (main till drops after load) is covered by the server refusal. |
| N4 / T2 / T3 | UX nits | One note per region; disabled text inputs look like enabled ones; the note's position varies | Accepted. A shared `input:disabled` style belongs in the global stylesheet, not this card. |

## Verified beyond unit tests
- The reviewer checked all 21 region→key mappings against their handlers, and confirmed no per-till control is inside a lock and no shop-wide writer on the page is left unwrapped except `#settings-all`.
- Falsification, done by both the reviewer and the Tester: forcing `lockedSettingsRegions` to return an empty map makes `TestSettingsPage_ShopWideLockedWhileMainTillUnreachable` fail. Removing one `disabled` makes `TestShopWideLockRegions_MatchTemplateAndScope` fail.
- Tester driven run on a real binary, with an admin session and a replica pointed at a dead main till (chip `unreachable`):
  - 20 locked regions rendered. The restore prompt is only shown while a restore is pending.
  - 65 locked controls were clicked, typed into and selected; none sent a settings request.
  - Tab never reaches a locked control. A per-till theme save is still accepted.
  - Viewed in EN light at 1280 and 1024×600, EN dark (note contrast 12.4:1), fa (RTL), de, and at 360px with no horizontal scroll.
  - On a main till: no lock and no note.
- Not verified: real touch hardware, an OSK-enabled till, ar/tr rendering, or a live reachable main till (that case is covered by a Go test).

## Verdict
Safe to merge.
