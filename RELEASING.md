# Releasing a new till version

Cutting a release is **pushing a `v*` git tag**. Everything else is automated
by `.github/workflows/release.yml` (goreleaser).

## Steps

1. **Land your changes on `main`** and make sure CI is green. The version is
   taken from the tag, not from a file — there is nothing to bump by hand.

2. **Tag and push** (semver, `v`-prefixed). From an up-to-date `main`:

   ```sh
   git checkout main && git pull
   git tag v0.1.3          # next version
   git push origin v0.1.3
   ```

   To redo a botched release, delete the tag locally and remotely
   (`git push origin :v0.1.3`) and re-tag — but only before anyone has
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
     attaches it.
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
