# 2026-07-28 — Release version-injection regression gate

## Context
A user reported the Mac app's status bar showing "Universal Till vdev"
after downloading and installing a real release. Investigation found the
actual installed app was fine (`v0.2.44`, confirmed via `Info.plist` and
`strings` on the binary) — the "vdev" was a stray local dev-mode test
process I'd left running on port 8080 from earlier debugging in the same
session, which the user's browser happened to hit.

That was resolved by killing the stray process. But the user's follow-up
was pointed and correct: "add some test that dont let you do kind of
mistake." A code-level test can't stop an agent from forgetting to kill a
background process — that's session hygiene, not something the codebase
can enforce. But looking at *why* "vdev" is even a plausible thing to see
turned up a real, currently-shipping bug worth a real regression test.

## Real bug found while investigating
`buildinfo.Version` is injected via `-ldflags "-X .../buildinfo.Version=…"`
at build time; without it, the variable defaults to `"dev"` — exactly the
string that confused the user. Checking every build target that produces
it:

| Target | ldflags has `-X buildinfo.Version=` ? |
|---|---|
| goreleaser `linux` (unitill-pos) | yes |
| goreleaser `windows` (unitill-pos.exe) | yes |
| goreleaser `darwin` (unitill-pos) | yes |
| **goreleaser `desktop-windows` (unitill-desktop.exe)** | **no** — `ldflags: ["-s -w -H windowsgui"]` |
| **release.yml `linux-shells` (unitill-desktop)** | **no** — `-ldflags "-s -w"` |
| `packaging/macos/build-app.sh` (both binaries) | yes |

Both desktop-shell (WebView) builds for Windows and Linux — the two
platforms whose custom build commands were written separately from their
sibling plain-server build, evidently copy-pasted without carrying the
version flag over — have been shipping `buildinfo.Version == "dev"` in
every release. Any Windows or Linux user opening the native app window
(not the browser-based server) would see exactly "Universal Till vdev" in
the status bar, indistinguishable from the confusion today. macOS was
unaffected only because `build-app.sh` is a from-scratch script, not a
copy of the plain build command.

## Fix
- `.goreleaser.yaml`: `desktop-windows` build now includes the same
  `-X .../buildinfo.Version={{.Version}}` its sibling `windows` build has.
- `.github/workflows/release.yml`: `linux-shells` job's build command now
  takes `VERSION` from `needs.prepare.outputs.version` and injects it the
  same way.

## The actual regression test
New `verify-versions` job in `release.yml`, running after `goreleaser` and
`macos-app`: downloads every release artifact that's supposed to embed
`buildinfo.Version` (linux tar.gz, windows zip, darwin tar.gz, the macOS
`.dmg`), extracts each, and asserts via `strings` that none of the
binaries inside — `unitill-pos` *and* `unitill-desktop` on every platform
that ships both — contains a bare `"dev"` string or fails to contain the
real release version. Runs on `macos-14` so one runner has `hdiutil` (for
the `.dmg`) plus `tar`/`unzip` for everything else. A future regression of
this exact kind (someone adds a new build target, or edits an existing
one's ldflags, and drops the version injection) now fails the release
loudly instead of shipping silently.

## Verification
`goreleaser check --config .goreleaser.yaml` passes. `actionlint
.github/workflows/release.yml` reports only one warning, pre-existing and
unrelated to this change (verified via `git diff` hunk ranges). `go build
./...` unaffected (no Go source touched). The check logic itself can't be
unit-tested outside a real GitHub Actions run — it will get its first real
exercise on the next release, at which point it should also silently
confirm the fix (both previously-broken binaries now pass).

## Follow-up: the gate itself had a bug on its first real run
Cut v0.2.45 to actually exercise this end to end. `linux-shells`,
`goreleaser`, `macos-app`, `windows-installer`, `android-app` all passed —
the real fix works. `verify-versions` itself failed: its "Wait for the
GitHub release to exist" step looped for the full 10-minute timeout
insisting the release didn't exist, even though `gh release list` showed
v0.2.45 published a minute *before* the job even started.

Root cause: every sibling job with this same wait-loop pattern
(`windows-installer`, `macos-app`, `android-app`) runs `actions/checkout@v4`
first — `gh` infers which repo to talk to from the checked-out git remote.
This job had no checkout step at all, so `gh release view "$TAG"` failed
for an unrelated reason (no repo context), and the `>/dev/null 2>&1`
redirect on that line silently swallowed the real error, making a
config mistake look exactly like "release not visible yet." Copied the
wait-loop text from a sibling job without copying the step it depends on.
Fixed by adding the same `actions/checkout@v4` step. Worth noting plainly:
even a change added specifically *to* catch mistakes needs its own real
end-to-end run before trusting it — it had a bug that would have made the
regression gate itself silently useless (timing out and failing the whole
release without ever checking a single binary) if the timeout hadn't been
loud enough to notice.

## Follow-up #2: the check itself had a pipe bug (v0.2.46)
With the checkout fix in, the job ran the real check — and every single
binary failed with "does not contain the expected version string," even
though (verified separately, by hand, against the actual downloaded
artifacts) the version really was there and correct. Cause: `strings -a
"$file" | grep -qx "..."` — macOS's `strings` (Xcode's, not GNU's) errors
("failed to flush output") when `grep -q` closes its read end early after
finding the first match, and `set -o pipefail` then reports that broken-pipe
error as the whole pipeline failing, even though `grep` itself found what it
was looking for and would have reported success. Fixed by capturing
`strings`' output into a variable first (`out="$(strings -a "$file" ||
true)"`) and grepping the captured text instead of a live pipe — no process
left running to receive a SIGPIPE, so no interaction with `pipefail` at all.
Verified locally against the real v0.2.46 artifacts (downloaded by hand)
before pushing again, rather than spending a third CI cycle to find out.
