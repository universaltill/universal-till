# Code review — a Linux till that can't update itself now says so (ut-docs#2733)

- **Date:** 2026-10-02
- **Ticket:** ut-docs#2733 (split from #2726; root cause class #151).
- **Branch:** `feat/2733-autoupdate-stuck-chip`
- **Author:** Opus 5.5 (this cycle's build model, `lane:cloud-24`).
- **Reviewer:** independent pass, Fable (different model from the author,
  per `MODEL-ROUTING.md`), in a separate worktree.
- **Verdict: SAFE TO MERGE**, after the fixes below.

## The gap

With auto-update on by default (#2726), a linux till whose install tree the
service user can't write makes `selfupdate.Supported()` false and
`autoUpdateTick` returns silently every night, while Settings still shows
"Update automatically" ticked. The status chip said only "In-app update
isn't available for this install" — the same as a till that never
auto-updates, with no remedy.

## What shipped

- `internal/selfupdate`: `LinuxInstallUnwritable()` — a linux install of a
  self-updatable shape (not apt's `/usr`, a locatable binary) whose binary
  dir or cwd the till can't write: the one cause a `.deb` reinstall fixes.
- `internal/pages/update_api.go`: pure `autoUpdateStuckFor` (linux,
  auto-update effectively on, update available, not a replica, folder
  unwritable — the probe called last); every scheduler tick publishes it via
  `updates.SetAutoUpdateStuck`, with the folder probe cached for 10 min;
  a "dev" build is never stuck. Settings → Check now shows
  `settings.update.auto_blocked` in that case (fresh probe) and clears the
  flag when the till can self-update again.
- `internal/updates`: the `atomic.Bool` + accessors; `internal/httpx`:
  `autoupdatestuck` template func.
- `web/ui/layouts/base.html`: in the unix fallback branch, a chip with
  `status.update_auto_blocked` whose text links to `/help/updates`
  (`.is-stuck` keeps the phone sale screen's status row visible, like
  `sb-power`); `web/public/app.css`: dark link ink on the green chip,
  wrapping instead of clipping.
- Keys in `web/locales/{en,ar,fa,tr}.json`; `ut-plugin-language-{de,es}`
  follow-up PRs; `web/help/{en,de,ar,fa,tr}/updates.md` sentence;
  CHANGELOG entry; docs-shots surface hash refreshed (pixel-neutral: the
  new branch only renders with an update available and an unwritable tree,
  which the screenshot harness never has).

## Findings

1. **major — fixed.** `!Supported()` also covers apt `/usr` installs and an
   unlocatable binary, where "reinstall the .deb" / "/opt/unitill isn't
   writable" is wrong. Now keyed on `LinuxInstallUnwritable()`; strings no
   longer hardcode `/opt/unitill` and name the non-.deb remedy too.
2. **minor — fixed.** The 30 s tick ran the two temp-file probes on every
   tick once an update was available (SD-card writes). Now cached for
   10 min (`TestAutoUpdateTick_ProbesInstallFolderAtMostOncePerTTL`), and
   skipped on a dev build.
3. **minor — fixed.** On a phone the sale screen hides the status row
   unless a listed chip is present; `.sb-update.is-stuck` added to that list
   (driven run at 390×844: visible, wraps, link fully readable).
4. **minor — fixed.** de/es strings were du/tú inside Sie/usted packs; de
   help sentence too. Now Sie/usted.
5. **minor (UX) — accepted.** Only the remedy text is the link, not the
   whole chip: a whole-chip `<a href="/help/updates">` with the arrow-up
   glyph fails `TestNoTwoNavDestinationsShareAnIcon` (arrow-up already leads
   to `/plugins`; the external download link is not counted by that test).
   The text link wraps and is the widest part of the chip.
6. **nit — fixed.** Check now on a fixed till clears the flag at once
   (`TestUpdateCheck_SupportedClearsStuck`).
7. **nit — accepted.** A failed settings read on a replica could make it
   look like a main till for one tick; pre-existing pattern of `get`,
   self-corrects on the next tick.
8. **nit — fixed.** The handler test asserts the translated key, not an
   English substring.

## Verified beyond unit tests

- TDD: tests written first and seen failing; reviewer re-verified by
  breaking `autoUpdateStuckFor` and the base.html branch (tests fail),
  restoring (pass). `TestLinuxInstallUnwritable` run as a non-root user
  (root writes through 0555 dirs, so it skips under root).
- Driven run: a `0.0.1`-stamped build run as `nobody` from a root-owned
  install dir, real GitHub release check (v0.30.15 available). Status chip
  read at 1024×600 (en, tr), 1280×800 (fa, RTL), 800×1280 (ar, RTL),
  390×844 phone (en); tapping the link opens `/help/updates` in every
  locale. Check now returns the stuck text with auto-update on, the generic
  text with it off. Not looked at: dark theme, the de/es packs installed on
  a till (validated by their own key-drift/validate scripts only).
- Gate: gofmt, build, vet, golangci-lint (0 issues), every guard in
  `ci.yml`'s build job, `go test ./...`. Locally `guard-deadcode-baseline`
  (internal/logging, untouched here) and `guard-shellcheck-version` (no
  shellcheck binary) fail identically on `origin/main`, whose CI is green.

## Deferred

- None new. Replicas keep the #2738 follow chip; darwin portable binaries
  keep the generic message (different remedy).
