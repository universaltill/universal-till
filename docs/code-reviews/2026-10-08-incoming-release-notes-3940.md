# Review: incoming release notes before installing an update (ut-docs#3940)

**Shipped.** When an update is waiting, a manager now reads the incoming
version's release notes in Settings → Software update, above the Install
controls. If the till skipped versions, the notes cover every one of them,
newest first, in the till's locale, with English as the per-note fallback.

- **Source.** `release.yml` builds a `release-notes.json` asset
  (`go run ./scripts/release-notes-bundle`: every `web/release-notes/**`
  file in every locale, checked against the till's own loader before it is
  written). goreleaser attaches it through
  `release.extra_files` and lists it in `checksum.extra_files`, so the
  `checksums` job still finds every asset in `checksums.txt`.
- **Discovery.** `internal/updates.checkOnce` records the asset URL
  (`Status.NotesURL`). It accepts only
  `https://github.com/universaltill/universal-till/releases/download/…`,
  or the exact origin of an operator-set `UT_UPDATE_RELEASES_URL`. That
  override is for mirrors and the e2e harness. `internal/selfupdate` keeps
  its own releases URL, so the override never changes what gets installed.
- **Fetch.** `GET /api/update/notes` is gated on `plugin_management`, and
  it answers 403 before any status read or network call. It downloads the
  asset lazily, once per offered version (10 s timeout, 4 MiB cap,
  `netaccess` client, so the demo till never fetches). It validates the
  asset through the same strict loader as the embedded notes
  (`releasenotes.LoadBundle`, which shares `build` with `Load`) and caches
  it in memory. A failure is remembered for 10 min. After a failure the
  endpoint shows "Release notes unavailable", plus a link to the release
  page where an external link works (`updatedownloadlink`). Installing is
  never blocked.
- **Docs.** The help topic `updates` gets a new step in en/de/tr/ar/fa,
  with the fa drift baseline moved 12→13 / 17→18, a gap that was already
  there. The README documents `UT_UPDATE_CHECK` and
  `UT_UPDATE_RELEASES_URL`.
- **e2e.** A new `incoming-notes` Playwright project, with a fake release
  server (`e2e/fake-release-server.mjs`), has two tests: notes shown above
  the install controls, and the unavailable fallback.

**Reviewer:** an independent Fable subagent (the author was Opus 5.5). It
ran build, vet, the touched packages' tests and the i18n/help/docs-shots
guards. It re-verified TDD claims in a separate worktree: when
`notesURLAllowed` is broken, `TestNotesURLAllowed` and
`TestCheckOnce_ForeignNotesAssetDropped` fail; when the handler's
`canPerform` is removed, `TestUpdateNotes_CashierRefusedBeforeAnyNetwork`
fails. Restoring the code makes all of them pass again.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | A request cancelled by the client (manager leaves Settings mid-download) was remembered as a 10-minute "unavailable" | Fixed. `fetchNotes` now runs under `context.WithoutCancel` with its own 10 s timeout. `TestIncomingNotes_CallerCancelNotMemoised` failed before the fix (`context deadline exceeded` memoised) and passes after it |
| 2 | should-fix | The late-loading notes panel sits directly above `#android-update-form`, which brings back the #1534 anchor-jump bug | Fixed. The same `htmx:afterSettle` → `reveal` listener is now on `#incoming-release-notes` |
| 3 | nit | A non-2xx reply (session expired) leaves "Loading the release notes…" in place | Accepted. It's cosmetic, and an `hx-on` handler would need eval under the till's CSP. Installing is unaffected |
| 4 | nit | The four new `en.json` keys are missing from the `ut-plugin-language-{de,es,pt}` packs | Brand-new keys: core merges first, and the pack PRs follow (#1576 order) |
| 5 | nit | No ut-docs reference mentions `UT_UPDATE_RELEASES_URL` | Accepted. The README and help topic cover it, and no ut-docs page documents the update check's env vars today |

Security (reviewed, no findings):
- The allow-list rejects userinfo, query strings, fragments, decoded `..`,
  `github.com.evil`, `http://`, other repos and the api host.
- XSS: goldmark runs without `WithUnsafe`, so raw HTML is dropped and
  `javascript:` hrefs are emptied. The embedded and bundle paths share one
  parser.
- The bundle loader rejects unknown fields, trailing data, more than 5000
  entries, traversal, absolute paths and backslashes.
- The cache mutex serialises only notes fetches, never page renders.

**CI follow-up (desktop-shell → deadcode baseline):**
`releasenotes.Bundle` could be reached only from the
`scripts/release-notes-bundle` tool, and that tool isn't a deadcode root,
so the guard reported a new unreachable func. The writer moved into the
tool, which now loads its output back with `releasenotes.LoadBundle`
before writing. The till binary carries no writer, and the baseline is
unchanged.

Its tests moved too (`scripts/release-notes-bundle/main_test.go`). The
refusal test fails when that load-back is disabled.

**Verified beyond unit tests:** Playwright `incoming-notes` passed (2/2).
Screenshots were taken and looked at, with notes and with the fallback, at
1024×600 and 360px. Neither layout overlaps or clips, and the notes sit
above Check for updates / Update now.

**Not checked:**
- A real GitHub release carrying the asset. The first release built from
  this branch will be the first to carry it, and goreleaser isn't installed
  here (`goreleaser check` wasn't run; both YAML files parse).
- A cashier in the browser. The e2e till runs with auth off, so that case
  is covered by the Go handler and template tests only.
- A till that skipped many versions gets a long notes list with no height
  cap. That's accepted, because the list shrinks once the till is current.

**Deferred:** #3958 (notes from the status-bar "Update now" chip) and
#3959 (joined-till Update now on the Tills page).

**Verdict:** safe to merge once CI is green on the rebased head.
