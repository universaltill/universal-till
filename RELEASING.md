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

Both bumps (A and B) start from the highest plain `vX.Y.Z` tag
(`scripts/ci/latest-stable-tag.sh`); pre-release tags such as `-beta.N` or
`-rc1` are ignored.

**C. Manual tag (the classic way still works):**
```sh
git checkout main && git pull
git tag v0.2.1 && git push origin v0.2.1
```

## Before tagging: write the release notes (required)

Every release ships an owner-facing "What's new" note (ut-docs#3091). The till
shows it in **Settings → About** and in a one-time "Updated to vX.Y.Z" chip for
managers, and the GitHub Release body leads with it.

1. List the cards closed since the previous tag (the pipeline board's **Done**
   column, or `git log --first-parent <previous-tag>..main`) and read each
   card's close-out comment. Credit a surface outside the till (my., the
   website, a plugin) only when that surface's own change has shipped: a
   till-side slice that merely lets my. send something is groundwork, not a
   feature. Check the card's sibling slices and any open `blocked:dep` cards
   naming it, and announce the feature in the release that ships its last
   slice (ut-docs#4219).
2. Write `web/release-notes/en/v<X.Y.Z>.md`:
   ```markdown
   ---
   version: v0.30.7
   date: 2026-10-02
   ---
   ## New
   - **Short name:** one or two sentences a shop owner understands.
   ## Improved
   - ...
   ## Fixed
   - ...
   ```
   Plain shop-owner language, grouped New / Improved / Fixed (leave out an
   empty group). No PR numbers, card IDs or internal jargon
   (`TestBuiltin_NotesAreOwnerLanguage` refuses `#123`, `ut-docs`, `ADR-…`).
3. Translate it into every help locale — `web/release-notes/{de,ar,fa,tr}/` —
   the same change (ut-docs `reference/translation.md`). A missing translation
   falls back to English, but ship them.
4. Merge to `main`, then release.

**`release.yml` refuses a release without it:** `prepare` runs
`scripts/ci/guard-release-notes.sh <tag>` — before creating the tag on the
one-click path, and on the tagged commit for a pushed tag. The goreleaser job
turns the note into the Release body's "What's new"; goreleaser's generated
change list follows under **Technical changes**. The guard also takes only
plain `vX.Y.Z` tags, so a pushed pre-release tag such as `v0.31.0-rc1` is
refused too — use the nightly pre-release for test builds.

## What happens

1. **Land your changes on `main`**, CI green — including the release notes
   above. The version comes from the tag — nothing to bump in a file.
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
   - The `provenance` job attests every asset before publish (build
     provenance, ADR-0181). A release that cannot be attested and verified
     stays a draft. After publish, `provenance-dmg` attests the `.dmg` and the
     rewritten `checksums.txt`, then verifies the whole published set.
   - The tag stamps `internal/buildinfo.Version`, so the running till shows the
     right version in its status bar.

4. **Launch-test the mac .dmg** (v0.2.11 lesson — the app aborted at launch
   while the server, pages and signature all checked out): download the
   released `.dmg`, mount it, `open` the app and confirm the
   `unitill-desktop` process **stays alive** with a window. Orphaned
   `unitill-pos` processes with no `unitill-desktop` = the shell is crashing.

5. **Nothing else to deploy.** `universaltill.com/download` fetches the GitHub
   `releases/latest` API at page load and rewrites its buttons to the new
   assets, so the site serves the new build immediately.

6. **Existing tills auto-notice.** `internal/updates` polls the releases API and
   compares against `buildinfo.Version`; an older till shows an "Update
   available" chip, and where self-update is supported a manager can click
   "Update now" (verifies the SHA256 against `checksums.txt`, and — once release
   signing is armed, see below — `checksums.txt` against its `checksums.txt.sig`
   signature first; then swaps the binary, restarts). `.deb` installs update via `apt` instead.

## Release signing (ADR-0178)

The till's self-updater trusts `checksums.txt` only if `checksums.txt.sig` is a
valid Ed25519 signature over its exact bytes by a key compiled into the build
(`buildinfo.ReleasePublicKeys`). The SHA-256 lines then vouch for each artifact.
It is **dormant until armed**: with no key configured, builds carry an empty
list and the updater checks SHA-256 only. The release run warns
(`release signing is not configured`) every time.

**Arming** (owner, once; the key ceremony is its own Admin Review card):

1. Offline, mint two keypairs, A (current) and B (next):
   `go run ./scripts/releasesig keygen` (prints seed, public key, key id).
   Keep both seeds in the password manager.
2. GitHub environment `release-signing` of this repo: secret
   `UT_RELEASE_SIGNING_KEY` = A's seed (64 hex).
3. Repository variable `UT_RELEASE_PUBLIC_KEYS` = `<A public hex>,<B public hex>`
   (1-4 lowercase 64-hex keys, comma separated, no spaces).
4. Cut the next release R1. Tills already installed accept R1 on SHA-256 only
   (they carry no key yet); R2 and later are signature-verified. Confirm in R1's
   run: the `checksums` job signed and verified, `verify-versions` found the
   compiled keys, `macos-dmg-attach` re-signed (the published `checksums.txt.sig`
   has two lines after the `.dmg` is folded in), and offline
   `UT_RELEASE_KEYS_HEX=<list> go run ./scripts/releasesig verify --checksums
   checksums.txt --sig checksums.txt.sig` passes on the downloaded pair.
5. Record the key ids (public) here.

A half-configured state fails the `checksums` job, so the release stays a draft:
secret without variable, or variable without secret.

**Rotation** (ADR-0178 section 6). Every armed build already carries the current
key **and the next one**, as `A,B`; CI signs with A. Tills accept any listed
key. `releasesig sign` refuses a seed that is not in `UT_RELEASE_PUBLIC_KEYS`,
so the secret and the variable are never changed in a way that leaves the
signing key unlisted.

- *Planned:* swap the secret `UT_RELEASE_SIGNING_KEY` to B's seed. B is
  already listed, so every armed till accepts the release. Some releases later,
  once the fleet heartbeat shows no till on a build older than the first `A,B`
  build, set the variable to `B,C` (mint C offline), which retires A.
- *A is compromised:* do the same swap at once (secret to B, whose key every
  armed till already trusts) and drop A from the variable in the next build
  (`B,C`). Never switch to fresh keys that armed tills do not trust.
- *Last resort only:* if B is lost or compromised too, fresh keys are the only
  way out. Every armed till then refuses the new release and needs a manual
  reinstall: that is the cost ADR-0178 section 6 accepts, so keep B offline.

A refused release shows on the till as `Update failed (signature)`. It is not
retried within the same run; "Update now" retries.

## Build provenance (ADR-0181)

Every release asset has a signed build-provenance attestation (SLSA Build L2).
It covers each file in `checksums.txt`, `checksums.txt` itself and
`checksums.txt.sig`. It is made by GitHub artifact attestations and the
public Sigstore service. No key of ours is involved. Nothing to set up.
Before attesting, the jobs compare the SHA-256 of `checksums.txt` and of
`checksums.txt.sig` with the values the signing job exported, so a file
re-uploaded in between is refused.

The `provenance` job runs after `checksums` and before the release is
published. `provenance-dmg` runs after `macos-dmg-attach` for the `.dmg` and
the rewritten `checksums.txt`. Both then run
`scripts/ci/verify-release-provenance.sh`, which also checks that a modified
file and a wrong workflow name fail. Neither job joins the `release-signing`
environment, so no job holds both the signing seed and a Sigstore token.

**Verify a release** (needs the GitHub CLI, any machine, no secrets):

```sh
gh attestation verify unitill-pos_X.Y.Z_linux_amd64.tar.gz \
  --repo universaltill/universal-till \
  --signer-workflow universaltill/universal-till/.github/workflows/release.yml \
  --source-digest "$(git rev-parse vX.Y.Z^{commit})"
git fetch origin main
git merge-base --is-ancestor "$(git rev-parse vX.Y.Z^{commit})" origin/main
```

The last command must succeed: the attested commit has to be on `main`.
Tag creation is not restricted, so a `v*` tag on any commit can run a release
workflow edited at that commit and get a valid attestation. The ancestry check
in `prepare` is part of that workflow and does not hold in that case. Only
this verifier-side check does (ADR-0181 section 7).

Run it for `checksums.txt` too. A fork uses its own `--repo` and workflow path.

**What this proves:** which commit and which workflow built the file. **It does
not prove the commit is safe.** A malicious change merged to `main` and then
released gets a valid attestation. Do not describe attestations as protection
against that. The control for it is merge review (ADR-0181 section 7).

If Sigstore or the attestation service is down, `provenance` fails and the
release stays a draft. On the one-click path the tag is removed, so start the
release again. A failure in `provenance-dmg` turns the run red, but the release
is already public. If `macos-dmg-attach` fails after it has uploaded (for
example in its re-verify step), the public `.dmg` and the rewritten
`checksums.txt` have no attestation yet and `provenance-dmg` is skipped:
re-run the failed jobs so `macos-dmg-attach` finishes and `provenance-dmg`
attests them.

**Start the one-click release from `main`.** The verifier accepts the
attestation's ref only as `refs/tags/<tag>` (a tag push) or `refs/heads/main`
(the one-click dispatch). A dispatch from another branch or an old tag gets a
different ref, so `provenance` fails, the release stays a draft and the tag is
removed. This is a documented rule rather than a new refusal in `prepare`:
the check there is ADR-0180's ancestor rule, and tightening it is a separate
decision for that ADR, not a side effect of this one.

The till does not check attestations. It still checks `checksums.txt.sig`
offline (ADR-0178).

## Android update: signer pin (ADR-0179)

The Android shell's in-app update installs an APK only if it is this app's
package, has a higher versionCode, and has exactly one signing certificate whose
SHA-256 digest is listed in `android/app/apk-signer-sha256.txt` (1 to 4 lowercase
hex digests; `#` comments). Gradle stamps it into `BuildConfig.APK_SIGNER_SHA256S`.
The digest is public; there is no secret or repository variable.

- **The release checks it.** The "Verify each APK" step runs
  `scripts/ci/check-apk-signer-pin.sh` per flavor and fails the release if the
  APK's signer is not in the file.
- **Fork / new key.** Put the SHA-256 of your own signing certificate
  (`apksigner verify --print-certs app.apk`) in the file. The release fails until you do.
- **Fork / update source (ut-docs#5271).** The in-app update downloads from the
  Gradle property `updateApkUrlBase`. A GitHub fork needs nothing: `release.yml`
  derives it from the repository it releases to. For another host set the
  repository variable `UT_ANDROID_UPDATE_APK_URL_BASE` (https, ending in `/`);
  the mirror must serve the three `unitill-pos-android*-latest.apk` names.
- **Rotation** (needs an APK Signature Scheme v3 lineage old to new).
  1) Add the new digest next to the old one in the file. 2) Sign with the new
  key plus the lineage, and keep the v1/v2 signature by the old key while any API
  24 to 27 till exists: those platforms ignore v3, see the old signer and keep
  updating only while the old digest stays pinned. API 28+ tills accept the
  release because the new key is pinned or, on a till that has only the old pin,
  because the old key is in the APK's platform-verified rotation lineage, so no
  overlap release is needed. 3) Remove the old digest only when no API below 28
  till remains; doing so strands them permanently. **Never tell an operator to
  uninstall:** that deletes local data including unsynced offline sales. Install
  the new APK over the existing app (Android honours the lineage on API 28+).
- **Local tests with a test key:** `-PutApkSignerPins=<sha256 of the test cert>`;
  an empty `-PutApkSignerPins=` builds dormant (no signer check, logged as
  `not pinned`). Never use the override in `release.yml`.

## Nightly builds (testing `main`, not a release)

`.github/workflows/nightly.yml` runs daily (03:00 UTC) plus on manual
`workflow_dispatch`, and publishes a rolling GitHub **pre-release** tagged
`nightly` (re-pointed to the latest `main` commit each run, old assets
replaced) — this is how to get a build of `main` onto a test device
without cutting a real release. It is a **prerelease**, so `releases/latest`
(what the auto-updater and the download page read) never resolves to it —
no existing till can be silently moved onto it.

Scope of this first cut: the core `unitill-pos` server binary + web assets
for Linux (`amd64`/`arm64`), Windows (`amd64`) and macOS (`arm64`) as plain
`.tar.gz`/`.zip` archives — no desktop-shell (WebView) build, no Windows
installer, no notarized macOS `.app`/`.dmg`, no `.deb`, no Android. Version
string is `<current-stable>-nightly.<date>.<short-sha>` (e.g.
`0.12.14-nightly.20260908.a1b2c3d4e5f6`), based on the *current* stable
version rather than a guessed next one so `internal/updates.Newer` keeps
ranking it correctly once that next stable actually ships. See
`universaltill/ut-docs#1432` for the beta channel, the till's channel
setting, and full platform parity — all tracked as separate follow-ups.

## Notes / gaps

- **Merge control is an open gap** (ADR-0181 section 7). `main` needs a pull
  request but no approval, so a malicious change could be merged, tagged and
  signed. Provenance does not close this. The owner decisions are open on
  ut-docs#5437.
- **macOS is Apple-Silicon only and unsigned** — Gatekeeper warns on first run.
  See the desktop app work for the friendlier `.app` path. No Intel (amd64)
  mac build is produced.
- **Windows binaries are code-signed** (Setup.exe, uninstall.exe and the
  portable zip's .exe files) as TASK RUNNER TECHNOLOGY LTD — see
  `packaging/windows/sign-exe.sh` and ut-docs `architecture/packaging.md`
  (ut-docs#2480, #2610).
- Public release = confirm intent, then push the tag. There is no separate
  "publish" button; the tag *is* the release.
