# Code review: postremove.sh removes unitill-kiosk.service.d drop-in

**Date:** 2026-08-27
**Issue:** universaltill/ut-docs#1082
**Complexity:** easy
**Author (Dev):** Sonnet (inline, this pipeline cycle)
**Reviewer:** independent Sonnet subagent, fresh context, isolated worktree

## What shipped

`packaging/scripts/postremove.sh` deleted the two kiosk systemd unit
*files* on `apt remove`/`apt remove --purge` but never the drop-in
directory `/etc/systemd/system/unitill-kiosk.service.d/` (holds e.g.
`10-portal.conf`, the screencast-portal wiring, ut-docs#395). That
directory is as un-package-owned as the unit files themselves — same
class of remnant as the `.bak` cleanup ut-docs#257 already added to
this script — and survived a full purge, meaning the next install on
that box would not behave like a genuinely fresh one.

Fix: `rm -rf /etc/systemd/system/unitill-kiosk.service.d` added right
after the existing `rm -f` of the two unit files, inside the same
`if [ "$1" = "remove" ] || [ "$1" = "purge" ]` conditional (fires on
both plain removal and purge, matching the unit-file cleanup it sits
next to).

## TDD

`packaging/kiosk_setup_test.go`'s `TestRemovalScriptsCleanUpKioskUnits`
extended with an assertion that `postremove.sh` contains the new `rm
-rf` line, using the file's existing `codeLines`/`anyLineContains`
non-comment-line matcher (not a raw substring check — this file's own
header explains why that matters: an earlier version of these tests
could be satisfied by a comment mentioning the fix). Confirmed failing
before the fix (`postremove.sh does not remove
/etc/systemd/system/unitill-kiosk.service.d — ...`), passing after.

## Independent review

A fresh-context Sonnet subagent (this is a `complexity:easy` card, so
review runs at the same model tier in a clean instance rather than
Opus, per the pipeline's model-routing rule) reviewed the diff in an
isolated git worktree and:

- Re-verified the TDD claim itself: reverted just the `postremove.sh`
  line, re-ran `TestRemovalScriptsCleanUpKioskUnits`, confirmed it
  failed with the expected message, restored the fix, confirmed it
  passed again.
- Ran the full `packaging` package test suite (13 tests) — all green.
- Confirmed `sh -n packaging/scripts/postremove.sh` and
  `packaging/scripts/preremove.sh` parse cleanly (these run under
  `/bin/sh` on the target box, not bash).
- Confirmed `gofmt -l packaging/` is clean.
- Checked placement: the new line sits inside the removal/purge
  conditional, not the purge-only data-wipe block — fires on plain
  `remove` too, matching the sibling unit-file cleanup.
- Confirmed `rm -rf` (not `rm -f`) is correct for a directory target,
  and that `rm -rf` on a nonexistent path is a safe no-op under
  `set -e` (verified directly) — a box that never ran kiosk setup at
  all is unaffected.
- Checked for the two recurring bug classes this pipeline watches for
  (missing `os.MkdirAll` on a file-write handler, a cwd-relative path
  where `paths.Data(...)` belongs) — confirmed neither applies; this
  diff is a one-line shell deletion plus a Go test assertion, no file
  writes or path construction involved.
- Confirmed no real client/shop name appears anywhere in the diff.
- Confirmed the diff touches no template, UI page, or locale file, so
  no `web/help/` topic update or i18n key is owed.

**Verdict: PASS, no blockers.** One informational nit noted (the
existing unconditional `systemctl daemon-reload` a few lines below
already covers this cleanup — no action needed).

## Safe to merge

Yes. Build, `gofmt`, and the full `packaging` test suite are green;
the independent review re-verified the TDD claim and found no
blocking issues.
