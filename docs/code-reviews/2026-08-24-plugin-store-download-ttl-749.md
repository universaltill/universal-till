# 2026-08-24 — ut-docs#749: TTL eviction for staged plugin-store downloads

**Card:** [ut-docs#749](https://github.com/universaltill/ut-docs/issues/749)
**Complexity:** medium — **Build model:** Sonnet (inline) — **Review model:** Opus, worktree-isolated subagent

## Why

`internal/plugins/installer_store.go`'s `DownloadToStore`/`InstallFromStore`
flow stages plugin bundles under `{pluginBaseDir}/downloads/` with no disk
quota and no automatic eviction. Cleanup previously happened only on
successful install (`DeleteStoreDownload`) or via the manual delete button in
the plugin-store UI. A manager who downloads-to-store repeatedly without
installing, or an install that fails after a successful download, left
staged bundles accumulating unbounded disk usage with no automatic recovery.

## What shipped

- `storeDownloadTTL = 48 * time.Hour` (unexported constant, not
  config-surfaced — a fixed safety-net default, per the card's own
  non-goal against resurrecting the deleted `CacheStore` package or
  building a quota UI).
- `sweepStoreDownloads()`: a single age-based pass over
  `{pluginBaseDir}/downloads/` — any file (not directory) whose mtime is
  older than the TTL is removed. Deliberately no separate orphan-detection
  branch: an orphaned bundle/metadata file left by a partial write ages
  out the same way, since it has a real mtime too.
- Called at the top of `DownloadToStore` (reclaim space before doing new
  network work) and at the top of `ListStoreDownloads` (self-heals just
  from a manager opening the plugin-store page — the far more frequent
  trigger than a new download).
- `DownloadToStore`'s metadata-encode/write failure paths now call
  `DeleteStoreDownload(listingID)` (not just `os.Remove` on the bundle) so
  a bundle promoted just before a metadata failure — and any stale
  metadata left over from a prior stage of the same listing — isn't left
  behind waiting on the next sweep.
- Sweep removal failures and a per-sweep reclaim count are now logged via
  `logging.L()`, matching the rest of the package (previously silently
  swallowed with `_ =`).
- `web/help/en/plugins.md`: one new numbered step explaining the
  "Downloaded" badge reverts to "Download" after 48 hours if the plugin
  isn't installed — this is a user-visible consequence of the TTL and the
  standing manual-ships-with-the-feature rule (ut-docs#324) makes it
  non-optional. `fa`/`ar`/`tr` are **not** yet updated — the NAS Ollama
  translation endpoint (`192.168.1.231:11434`) is unreachable from this
  cloud session (confirmed: connection timeout), matching the existing
  `blocked:env` pattern already used for this exact gap elsewhere in the
  backlog (ut-docs#941, #915, #943, #957). Follow-up filed: ut-docs#960.
- New tests in `installer_store_test.go`: stale pair swept; fresh pair
  survives; stale orphan bundle swept; missing downloads dir tolerated
  (no panic); `ListStoreDownloads` sweeps before listing; `DownloadToStore`
  sweeps stale *unrelated* listings before doing its own download;
  metadata-write failure cleans up the just-promoted bundle.

## Independent review (Opus, worktree-isolated)

Gate re-run clean in the isolated worktree: `gofmt`, `go build ./...`,
`go vet ./...`, full `go test ./internal/plugins/...`
(`internal/pages -run Store`), `guard-data-access.sh`, `guard-i18n.sh`,
`guard-help-topics.sh`, `guard-compliance-claims.sh` — all pass.

**TDD re-verified independently**, not taken on the implementer's word: the
reviewer did its own revert-then-restore inside its isolated worktree,
pasted the actual failing output (5 of the 6 new tests fail with genuine
assertion messages against the pre-fix behavior — e.g. `stale file
.../stale-listing.tar.gz not swept`, `orphaned bundle left behind after
metadata write failure`), then restored and confirmed all green again.

Specific risk classes the reviewer checked and cleared:

- **Mid-write race**: not possible. `.part` files live in a sibling tmp
  dir (`paths.Plugins("tmp")`), never inside the swept `downloads/` dir;
  `PromoteToPermanent` uses `os.Rename` (atomic — a bundle the sweep can
  see is either complete or has a fresh mtime, never both incomplete and
  old).
- **TOCTOU on the "slow admin at 47h59m" question**: only a live risk if a
  *second* concurrent request sweeps between `GetStoreDownload`'s stat and
  the extract — a microsecond window; failure mode is a clean re-download,
  not data loss. Not gated on.
- **Directory safety**: `e.IsDir()` guard confirmed against the existing
  `subdir.json`-directory test case — no directory tree can be swept.
- **Nothing else writes to `downloads/`** — grep-confirmed single owner;
  independently corroborated by `paths.go` already excluding `downloads`
  from backups (transient by design).
- **Manual-delete path**: zero lines of the diff touch `DeleteStoreDownload`
  or its API handler; its own test still passes.

## Findings — triaged

| # | Severity | Fixed? |
|---|---|---|
| F-1 | real-but-minor | **Fixed** — metadata-failure cleanup now calls `DeleteStoreDownload` (removes stale `.json` from a prior stage too, not just the bundle) |
| F-2 | real-but-minor | **Fixed** — sweep removal failures logged (`Warnf`); a per-sweep reclaim count logged (`Infof`) so a permission/mount issue that silently defeats the sweep is no longer invisible |
| F-3 | real-but-minor, accepted | Not fixed — TTL bounds *time*, not *space*: many distinct staged listings within the 48h window can still add up (200 MB/bundle cap × N listings). The issue's AC says "quota **and/or** eviction," so this satisfies scope as written; flagged rather than silently declared fully solved. Not actioned this task — a real space cap is a bigger, separate design (worth a Backlog follow-up if it ever matters in practice; no shop has approached this yet). |
| F-4 | real-but-minor | **Fixed (en only)** — manual step added; fa/ar/tr blocked on NAS unreachability, follow-up filed per the established pattern |
| F-5 | nitpick, accepted | Not fixed — sweep keys off filesystem mtime rather than the recorded `DownloadedAt`; simpler and sufficient for this scope, a `cp`/`rsync` without `-a` during a till migration could reset mtimes and extend a stale download's life, but that's an unrelated migration-hygiene issue, not this task's to fix |
| F-6 | nitpick, accepted | Not fixed — a bundle/metadata pair written milliseconds apart can straddle the TTL cutoff in one sweep pass; self-heals next pass, `ListStoreDownloads` already skips an unmatched bundle so the UI stays correct in the interim |

**Verdict:** independent review said safe to merge with F-4 addressed first
(no blockers). F-1/F-2/F-4(en) applied in this same branch before merge, as
the reviewer recommended taking them together rather than as a follow-up.
Full gate re-run clean after the fixes (see below).

## Verified beyond automated tests

- Manually re-ran the full `go test ./...` (every package, not just
  `internal/plugins`) after applying the F-1/F-2/F-4 fixes — all green,
  same as the pre-fix gate.
- No UI/visible surface *changed* (backend disk housekeeping; the manual
  edit only adds a sentence describing existing behavior) — but editing
  `web/help/en/plugins.md` still trips `guard-docs-shots.sh` (it hashes
  topic markdown, not rendered pixels), which the pre-push local gate
  missed and CI caught on the real PR (`build` job, `guard-docs-shots.sh`
  failing with "topic markdown changed since its screenshot was taken").
  Fixed by running `make docs-shots` for real (full 92-screenshot suite,
  92/92 passed) and committing only the resulting `manifest.json` hash
  update for `plugins/en` — the actual `plugins/*.png` files came back
  byte-identical (confirming no pixel changed), so none needed
  re-committing. Two unrelated `translations.png` (ar/fa) diffs surfaced
  by the same run were discarded, not committed — the suite's own output
  warns the reused Chromium (141.0.7390.37) doesn't match the
  `@playwright/test` pin (149.0.7827.55, ut-docs#622), and re-rendering
  unrelated screenshots on a mismatched browser version is exactly the
  kind of out-of-scope drift that warning exists to flag, not something
  this PR should carry.
- Confirmed NAS Ollama translation endpoint unreachable from this cloud
  session directly (`curl` timeout) before deciding to defer fa/ar/tr,
  rather than assuming.

## Deferred (new Backlog cards)

- ut-docs#960 — fa/ar/tr translation of the one new `web/help/*/plugins.md`
  sentence, via the NAS Ollama pipeline — same shape as
  ut-docs#941/#915/#943/#957.
- F-3 (space-bounded, not just time-bounded, staged-download quota) — not
  filed as a separate card; noted here as an accepted, scoped-out gap per
  the original issue's own "quota and/or eviction" wording. Revisit if
  real-world disk pressure from this path is ever observed.
