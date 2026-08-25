# Code review: desktop-OS Pi kiosk-overlay default (ut-docs#1040)

**Branch:** `pipeline/1040-desktop-os-kiosk-overlay`
**Author:** autonomous SDLC pipeline (Dev: Fable subagent, hard-tier build; Review: Opus subagent, independent, isolated worktree)
**Scope:** Linux/Raspberry Pi only, per this cycle's BA scoping note on the
issue. macOS/Windows equivalent is split off to ut-docs#1085 (spec-only).
Real-hardware confirmation of the PIN-exit round-trip stays tracked
separately at ut-docs#1078 (`blocked:env`) and is not claimed here.

## What shipped

A fresh `.deb` install on a Raspberry Pi running a **desktop OS**
(Raspberry Pi OS "with Desktop" — what a shop owner actually flashes) now
gets the till configured to run fullscreen kiosk **on top of** that
desktop, instead of the previous silent no-op (`is_pi_appliance`'s
display-manager bail did nothing and said nothing).

- `internal/app/provision.go` — a new `unitill-pos
  provision-desktop-kiosk-defaults` subcommand, idempotently seeding
  `display.window_mode=kiosk` + `display.launch_on_startup=true` through
  the repository layer (`SettingsRepo`, never raw SQL from bash), plus a
  system-actor audit entry (`POSRepo.InsertAudit`) so the decision is
  visible to the owner on `/audit`, not just installer stdout.
- `cmd/unitill-desktop/autostart_install_flag.go` + `desktop.go` — a
  hidden `--install-autostart` flag that calls the shell's own existing
  `reconcileAutostart(true)` (ut-docs#611) and exits, so the XDG autostart
  entry has exactly one author in Go — never a duplicated bash heredoc.
- `packaging/scripts/postinstall.sh` — a new `is_desktop_kiosk_overlay`
  branch, sharing a `is_fresh_install_pi_debian` gate with the existing
  `is_pi_appliance` so the two are mutually exclusive by construction
  (they differ only on whether a real display manager is present, and on
  the new branch's own opt-out marker). Detects the login user the same
  way `unitill-kiosk-setup.sh --auto` does, runs the two steps above as
  the right users, and reloads `unitill-pos` afterward (see Blocker,
  below) so the seeded settings actually take effect this boot. Opt-out:
  `/etc/unitill/no-desktop-kiosk-overlay`, deliberately distinct from the
  headless path's `/etc/unitill/no-kiosk`.
- `web/ui/pages/settings.html` + all 4 locales — the existing
  `window_mode_shell_attached` note (shown only when a desktop shell holds
  a live control poll) now also explains what "full screen/kiosk overlays
  this computer's own desktop" means and that a fresh Pi-with-desktop
  install starts this way automatically.
- `web/help/{en,ar,fa,tr}/display.md`, `README.md`, `.goreleaser.yaml` —
  manual and release-notes updated in the same branch (product-owner
  standing rule).
- Tests: `internal/app/provision_test.go` (idempotency, audit content,
  never clobbers an owner's later change), `cmd/unitill-desktop/
  autostart_install_flag_test.go`, `packaging/desktop_overlay_test.go`
  (functionally executes the extracted shell predicates against a fake
  `systemctl` — proves mutual exclusivity across all three
  display-manager shapes, not a substring check).

## Independent review — findings (Opus, isolated worktree)

**BLOCKER (found + fixed in review):** the overlay branch seeded the two
settings *after* `unitill-pos.service` was already restarted earlier in
the same script. The running service's in-memory settings cache never
saw the new values, and both the window-state API and the very next
`SaveState` call (the first-boot wizard, or any Settings save) would
silently write the stale `normal`/`false` values straight back over the
freshly-seeded ones — reproduced end-to-end against the real code path.
**Fix:** `systemctl try-restart unitill-pos.service` after a successful
seed, matching the script's existing never-fail-the-install convention.
Regression-tested (`desktop_overlay_test.go` asserts the restart exists
*and* comes after the seed call).

**SHOULD-FIX 1 (fixed, this pass):** the new Settings note was gated only
on `not piKioskAppliance`, so it rendered unconditionally on macOS/
Windows and on "no shell attached," directly contradicting the paragraph
immediately below it. Fixed by folding the explanation into the
`window_mode_shell_attached` string itself — the one topology where it's
actually true — and removing the separate always-on block, restoring the
page's three mutually-exclusive states.

**SHOULD-FIX 2 (fixed, this pass):** the documented opt-out
(`sudo touch /etc/unitill/no-desktop-kiosk-overlay`) fails on a genuinely
fresh machine — `/etc/unitill` doesn't exist until this postinstall
creates it, unlike the pre-existing `no-kiosk` marker, which is
documented as a post-install step. Fixed in `README.md`,
`.goreleaser.yaml`, and all 4 `web/help/*/display.md` files:
`sudo mkdir -p /etc/unitill && sudo touch ...`.

**Nits, accepted as-is / tracked separately, not blocking:**
- The audit action renders as a raw English slug (`desktop_kiosk_overlay_provisioned`)
  on `/audit` regardless of locale — pre-existing convention
  (`report_archive_pruned` has the same gap); a future audit-label i18n
  pass is real but out of scope here.
- New `en.json` key needs a follow-up in the external
  `ut-plugin-language-{de,es}` packs — `lang-pack-drift` is advisory on
  this PR (blocking only on push to `main`); tracked as a new Backlog card
  (ut-docs#1086) rather than done in this PR (cross-repo).
- Scope is Raspberry Pi specifically (`grep "Raspberry Pi"
  /proc/device-tree/model`), not "any Linux desktop" — consistent with
  every other piece of copy in this change and with the BA scoping note;
  a plain x86 Debian desktop install gets nothing from this branch. Noted
  for a future card if that scope is meant to widen.

## What was verified beyond automated tests

- **TDD claims independently re-verified**, not taken on trust: the
  Dev-reported reds were reproduced fresh by review — hand-reverting
  `provisionDesktopKioskDefaults`'s marker check reproduced exactly the
  claimed idempotency failure (settings clobbered, duplicate audit row);
  three separate reverts of `postinstall.sh` (dropping the display-manager
  check, a full pre-fix restore, dropping the upgrade guard) each
  reproduced the claimed mutual-exclusivity/upgrade-safety failures in
  `packaging/desktop_overlay_test.go`. Every revert was restored and
  re-confirmed green afterward.
- Bash-specific review: `set -e` interaction with the new `runuser` calls
  (both sit inside `if`, where `set -e` is suspended — an expected
  failure can't abort the install), `runuser`'s HOME/USER resolution for
  the target login user (verified empirically in-container, including for
  `pos`, whose shell is `/usr/sbin/nologin`), and that the shipped `.deb`
  actually carries a real `-tags desktop` Linux build of `unitill-desktop`
  (so `--install-autostart` reaches real `reconcileAutostart`, not
  `stub.go`'s no-op).
- i18n: ar/fa/tr strings checked as genuine, idiomatic translations (not
  English copies, not machine-garbled), consistent with the existing
  `exit_to_os_title` button label they reference.
- Full gate re-run after every fix, not just the specific case each
  finding named: `gofmt -l .` (clean), `go build ./...`, `go vet ./...`,
  `go test ./...` (full suite, all packages green), and all
  CI-blocking guards from `.github/workflows/ci.yml`'s `build` job that
  apply to this diff (`guard-i18n.sh`, `guard-data-access.sh`,
  `guard-help-topics.sh`, `guard-compliance-claims.sh`,
  `guard-docs-shots.sh` — `make docs-shots`, 92/92 passed after the
  settings.html copy fix).
- No real client/shop name, no secret-shaped literal, anywhere in the
  diff. No money/offline-first surface touched; no new network call added
  to any critical path (the provisioner is local SQLite only).

## Explicitly deferred / out of scope

- macOS/Windows equivalent shape → ut-docs#1085 (spec-only follow-up).
- Real Raspberry Pi hardware confirmation of the PIN-exit round-trip →
  ut-docs#1078 (`blocked:env`), stays open after this merges.
- `ut-plugin-language-{de,es}` follow-up for the new `en.json` key →
  ut-docs#1086 (new Backlog card).
- Audit-action i18n labelling on `/audit` → noted above, not scoped to
  this card.

## Verdict

Safe to merge. One real blocker was found and fixed (with a regression
test) before this record was written; two should-fix copy/UX defects were
also found and fixed. No open findings block merge.
