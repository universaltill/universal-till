# Code review — display-manager alias kiosk guard (2026-08-03)

## Scope

Fix the fresh-Pi kiosk guard reported in `universaltill/ut-docs#287`.
On Debian 13 / Raspberry Pi 5, `systemctl is-enabled
display-manager.service` succeeded for an alias resolving to
`graphical.target`, even though no desktop display manager was present. That
skipped automatic kiosk staging on a clean installation and made the manual
kiosk helper attempt to stop a non-loaded unit.

## Change reviewed

- Both packaging paths now resolve `display-manager.service` through
  `systemctl show --property=Id,LoadState --value`.
- They consider it a desktop manager only when the resolved unit is loaded
  and its canonical ID ends in `.service`; a target alias and a non-loaded
  service name are ignored.
- The packaging regression test executes the exact shell helper with a mocked
  `systemctl`, covering a loaded `graphical.target` alias (rejected), a loaded
  `lightdm.service` (accepted), and a non-loaded `display-manager.service`
  (rejected).

## Independent review

An independent read-only review checked the exact diff, ran the packaging
tests, shell syntax checks, ShellCheck, and `git diff --check`.

Initial finding: **low** — the first regression test was structural only.
It was fixed before this record: the test now behaviorally exercises each
script's extracted helper with mocked `systemctl` output. Follow-up review
found no remaining findings.

## Verification

- TDD evidence: with the two guards restored temporarily to their old
  `is-enabled` behaviour, `TestKioskDisplayManagerGuardsResolveAliases`
  failed for both scripts; restoring the fix made it pass.
- `go build ./...`
- `go vet ./...`
- `go test ./...`
- `bash scripts/ci/guard-data-access.sh`
- `bash scripts/ci/guard-i18n.sh`
- `sh -n packaging/scripts/postinstall.sh`
- `bash -n packaging/linux/unitill-kiosk-setup.sh`

## Deferred

The fixed package still needs a fresh-install field re-test on the Pi; that
work remains tracked by `universaltill/ut-docs#21` and will use the release
pipeline rather than a manual deployment.

## Verdict

**Safe to merge.** The desktop-protection intent remains intact, while the
field-proven alias-only false positive no longer blocks a headless Pi kiosk.
