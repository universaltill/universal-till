# 2026-07-29 — Kiosk Chromium stuck behind an undismissable keyring prompt

## What shipped

Confirmed live while deploying an unrelated fix to Farshid's Pi: restarting
the kiosk (`cage` + Chromium `--kiosk`) session made Chromium's first
touch of its password manager pop a native "Authentication required —
keyring is locked" GTK dialog over the one surface `cage` renders. With no
keyboard/mouse on a bare touchscreen till, there is no way to dismiss a
GTK dialog like this — the till would be stuck showing only the prompt,
never reaching the POS UI, on any restart or reboot that triggers it.

Fixed with `--password-store=basic` in
`packaging/linux/unitill-kiosk-launch.sh` — this kiosk is a single-purpose
browser pointed at one hardcoded local URL, not a general browsing
profile; POS auth is server-side (sessions/PINs), so the OS keyring
wasn't protecting anything meaningful here. New CI guard
(`scripts/ci/guard-kiosk-launch-flags.sh`) fails if this flag ever
regresses out of the launch script.

## Independent review (different model, opus)

**PASS.** Re-ran the guard both ways personally: reverted the launch
script to `main`'s version, confirmed the guard fails with the exact
expected message, restored the fix, confirmed green again. Confirmed
`go build` and the three pre-existing unrelated guards (data-access,
i18n, webkit-version) are unaffected. Confirmed `--password-store=basic`
is a real, standard Chromium flag (not a hallucination), consistent with
common kiosk/embedded Chromium deployment practice. Flagged (and this
session then confirmed) that a plain `git checkout --` cannot restore
*uncommitted* working-tree changes — a process note for this pipeline,
not a defect in the change itself. Confirmed no security downgrade of
substance (device access already implies physical access; nothing
credential-worthy lived in this profile) and no secrets/client names in
the diff.

## Verified beyond automated tests

Live on the actual device: applied the flag, restarted the kiosk
service, took a real screenshot — the dialog did not reappear, the POS
screen rendered normally, footer showed the hotfix build version.

## Safe to merge

Yes. Feature branch `fix/kiosk-chromium-keyring-prompt`.
