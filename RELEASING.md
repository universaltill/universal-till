# Releasing a new till version

Everything is automated by `.github/workflows/release.yml` (goreleaser). Pick
whichever way is easiest — all three produce the same release:

## The easy ways (pick one)

**A. One click in the browser (no CLI):**
GitHub → **Actions** → **Release** → **Run workflow** → choose **patch / minor /
major** (or type an explicit version) → **Run**. The workflow computes the next
version, tags it, and builds + publishes every platform.

**B. One command locally:**
```sh
scripts/release.sh          # patch bump (0.2.0 → 0.2.1)
scripts/release.sh minor    # 0.2.0 → 0.3.0
scripts/release.sh 1.0.0    # explicit
```

**C. Manual tag (the classic way still works):**
```sh
git checkout main && git pull
git tag v0.2.1 && git push origin v0.2.1
```

## What happens

1. **Land your changes on `main`**, CI green. The version comes from the tag —
   nothing to bump in a file.
2. The **Release** workflow runs (tests first; a red test aborts it), then:

   To redo a botched release, delete the tag locally and remotely
   (`git push origin :v0.2.1`) and re-tag — but only before anyone has
   downloaded it.

3. **Watch the release run** (`gh run watch` or the Actions tab). On a `v*`
   tag `release.yml`:
   - runs `go test ./...` (a red test aborts the release),
   - runs **goreleaser**, which cross-builds and publishes a GitHub Release:
     - Linux `amd64` + `arm64`, Windows `amd64`, macOS `arm64` (Apple Silicon),
     - `.tar.gz` (mac/linux) and `.zip` (windows) archives that bundle the
       binary + `web/` + the per-OS launcher (`run-unitill.command` / `.sh` /
       `.bat`),
     - `.deb` packages for Raspberry Pi / Debian (arm64 + amd64),
     - `checksums.txt`,
   - builds the **Windows NSIS installer** (`unitill-pos-setup-<ver>.exe`) and
     the **macOS .app/.dmg** in separate jobs and attaches them. These jobs
     only require the GitHub release to *exist* (they wait for it), not the
     goreleaser job to have succeeded — a GitHub API blip that fails
     goreleaser after publishing (the v0.2.9/v0.2.10 outage: 503 → retry →
     double-upload → 422) no longer costs the `.exe`/`.dmg`.
   - The tag stamps `internal/buildinfo.Version`, so the running till shows the
     right version in its status bar.

4. **Nothing else to deploy.** `universaltill.com/download` fetches the GitHub
   `releases/latest` API at page load and rewrites its buttons to the new
   assets, so the site serves the new build immediately.

5. **Existing tills auto-notice.** `internal/updates` polls the releases API and
   compares against `buildinfo.Version`; an older till shows an "Update
   available" chip, and where self-update is supported a manager can click
   "Update now" (verifies the SHA256 against `checksums.txt`, swaps the binary,
   restarts). `.deb` installs update via `apt` instead.

## Notes / gaps

- **macOS is Apple-Silicon only and unsigned** — Gatekeeper warns on first run.
  See the desktop app work for the friendlier `.app` path. No Intel (amd64)
  mac build is produced.
- **Windows installer is unsigned** — SmartScreen warns until a code-signing
  cert is added.
- Public release = confirm intent, then push the tag. There is no separate
  "publish" button; the tag *is* the release.
