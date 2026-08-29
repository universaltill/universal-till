# Code review — per-PR CI build of the Linux desktop shell (ut-docs#1071)

- **Date:** 2026-08-29
- **Branch:** `fix/1071-ci-desktop-shell-build`
- **Commit under review:** `07026d2` — "ci: build and vet the Linux desktop
  shell on every PR (ut-docs#1071)" (was uncommitted in the working tree when
  the review started; committed by the orchestrator mid-review — the commit
  contains **only** `.github/workflows/ci.yml`, 33 insertions, no stray files
  from the reviewer's temporary experiments, which were all reverted first)
- **Reviewer:** independent reviewer — fresh eyes, no access to the
  implementation reasoning
- **Refs:** ADR-0028 (Linux desktop shell targets webkit2gtk-4.1, never 4.0),
  `.github/workflows/release.yml`'s `linux-shells` job (the recipe this
  mirrors)
- **Verdict: safe to merge.** No blocking issues. Three Minor findings, all
  accepted/deferred with rationale below; none of them prevent the job from
  doing the one thing the card asked for, which was independently proven by a
  TDD-style negative test.

---

## What shipped

A new `desktop-shell` job in `.github/workflows/ci.yml` (lines 200–231),
inserted between `build` and `e2e`. It checks out, pins Go 1.25, apt-installs
`libgtk-3-dev libwebkit2gtk-4.1-dev`, then runs:

```
CGO_ENABLED=1 go build -tags desktop ./cmd/unitill-desktop
CGO_ENABLED=1 go vet  -tags desktop ./cmd/unitill-desktop
```

with `needs: build`, `runs-on: ubuntu-latest`, and the same
`GOMODCACHE`/`GOCACHE` env vars the `build` job uses.

The gap it closes is real and was reproduced, not taken on trust. Every file
in `cmd/unitill-desktop` that touches cgo/GTK is behind a `desktop`-family
build tag (`desktop.go`, `binname.go`, `webview_fallback.go`,
`window_mode_linux.go`, `autostart_linux.go`, `startup_gate_linux.go`,
`attach_gate_linux.go`, …), with `stub.go` (`//go:build !desktop`) standing in
for all of them on the untagged path. The existing `build` job's `go build
./...` therefore compiles `stub.go` and *none* of the real shell.

---

## What was independently verified

Every command below was actually run in this session, in
`/home/user/universal-till`.

| # | Command | Result |
|---|---|---|
| 1 | `pkg-config --exists gtk+-3.0` / `webkit2gtk-4.1` | both present (headers already installed) |
| 2 | `CGO_ENABLED=1 go build -tags desktop ./cmd/unitill-desktop` | **exit 0**, 9.5 MB ELF produced |
| 3 | `ldd unitill-desktop \| grep -E 'webkit\|gtk'` | links `libgtk-3.so.0`, `libwebkit2gtk-4.1.so.0`, `libjavascriptcoregtk-4.1.so.0` — a real cgo build, not a stub |
| 4 | `CGO_ENABLED=1 go vet -tags desktop ./cmd/unitill-desktop` | **exit 0** |
| 5 | `gofmt -l .` | empty |
| 6 | `bash scripts/ci/guard-webkit-version.sh` | pass |
| 7 | `bash scripts/ci/guard-makefile-version.sh` | pass |
| 8 | All other CI-blocking guards (`guard-data-access`, `guard-kiosk-engine`, `guard-plugin-menu-read`, `guard-i18n`, `guard-compliance-claims`, `guard-docs-shots`, `guard-help-topics`, `guard-kiosk-launch-flags`, `guard-android-status-address`, `guard-android-i18n`, `guard-emoji-font`, `guard-htmx-loaded`, `guard-autofill-suppression`, `check-brand-assets`, `guard-osk-loaded`, `guard-migration-version-collision`) | **16/16 pass** |
| 9 | `python3 -c "yaml.safe_load(ci.yml)"` | parses; jobs resolve to `[build, desktop-shell, e2e, contract]`; `desktop-shell` lands as a **sibling job**, not a nested step — indentation is correct |
| 10 | `needs:` graph check across all four jobs | every `needs` target resolves to a real job; no cycle |

### The load-bearing test: can this job actually fail?

The card's whole point is that the job must be able to go red when the desktop
shell is broken. Verified by deliberately breaking the code twice and
reverting each time.

**(a) Syntax error in a `//go:build desktop && linux` file**
(`cmd/unitill-desktop/window_mode_linux.go`, appended
`func deliberateSyntaxErrorForReview( {`):

```
CGO_ENABLED=1 go build -tags desktop ./cmd/unitill-desktop
  → window_mode_linux.go:63:38: expected ')', found '{'      exit 1  ← NEW job fails
go build ./...
  →                                                          exit 0  ← OLD build job passes
```

**(b) Renamed symbol against the cgo package**
(`webview_fallback.go:95`, `w.SetSize` → `w.SetSizeRenamedSymbolForReview`):

```
CGO_ENABLED=1 go build -tags desktop ./cmd/unitill-desktop   → exit 1
CGO_ENABLED=1 go vet   -tags desktop ./cmd/unitill-desktop   → exit 1
go build ./...                                               → exit 0
go vet ./cmd/unitill-desktop                                 → exit 0
```

Both files were restored from backup afterwards and the green build/vet
re-confirmed (`git status` clean apart from `ci.yml`). This is the direct
evidence that the gap was real and that the new job closes it — the *old*
checks pass on broken desktop code, the *new* one does not.

### Can the job silently succeed while building nothing?

No, and this was tested rather than assumed:

- `CGO_ENABLED=0 go build -tags desktop ./cmd/unitill-desktop` **fails loudly**:
  `build constraints exclude all Go files in internal/thirdparty/webview_go`.
  So a cgo regression cannot degrade into a passing no-op build; the job either
  compiles the real thing or goes red.
- `CGO_ENABLED=1` is belt-and-braces (Linux runners already default to 1) but
  correct to state explicitly, and it matches release.yml.
- The apt packages are exactly what the build needs:
  `internal/thirdparty/webview_go/webview.go:15` is
  `#cgo linux … pkg-config: gtk+-3.0 webkit2gtk-4.1`, satisfied precisely by
  `libgtk-3-dev` + `libwebkit2gtk-4.1-dev`. No over- or under-install.

### Drift check against `release.yml`

The stated intent is that this mirrors `release.yml`'s `linux-shells` job
(lines 132–164). Compared field by field:

| | release.yml `linux-shells` | ci.yml `desktop-shell` | Verdict |
|---|---|---|---|
| apt packages | `libgtk-3-dev libwebkit2gtk-4.1-dev` | identical | ✅ exact mirror |
| step name | "Install GTK/WebKit dev headers" | identical | ✅ |
| build tag | `-tags desktop` | identical | ✅ |
| CGO | `CGO_ENABLED=1` | identical (as `env:`) | ✅ |
| runner | `ubuntu-22.04` + `ubuntu-22.04-arm` | `ubuntu-latest`, amd64 only | ⚠️ F2, F3 |
| Go version | `go-version-file: go.mod` | `go-version: '1.25'` | ✅ equivalent (`go.mod` says `go 1.25.0`); matches ci.yml's own convention, which pins `'1.25'` in all four jobs |
| flags | `-trimpath -ldflags "-s -w -X …buildinfo.Version=…" -o unitill-desktop` | none | ✅ intentional — this is a compile check, not a release artifact |

### Non-applicable checklists

- **Secrets / personal data / client names:** none. The diff is `apt-get` and
  `go build`; grepped for `secret|token|key|password|api_key|AKIA|ghp_|BEGIN`
  — no hits.
- **Shop-owner-visible surface:** none. `git diff main --name-only` is exactly
  one file, `.github/workflows/ci.yml`. No `web/`, no `web/help/`, no
  `web/locales/`, no `internal/pages/`. The manual/screenshot/i18n checklist
  correctly does not apply.
- **Repository pattern / money / offline-first:** no Go code changed.

---

## Findings

### F1 — Minor (deferred): `guard-webkit-version.sh` does not scan `ci.yml`

`scripts/ci/guard-webkit-version.sh:16-17` greps for a stray `webkit2gtk-4.0`
across `internal/thirdparty/webview_go`, `.github/workflows/release.yml` and
`.goreleaser.yaml` — **not** `.github/workflows/ci.yml`. The guard's own header
comment names its threat model as "copy-pasting an old apt/goreleaser
snippet", and this diff adds exactly such a copy-pasted apt snippet to a file
the guard cannot see. A future edit of ci.yml to `libwebkit2gtk-4.0-dev` would
pass the ADR-0028 guard.

Severity is Minor, not Major, because the regression is self-catching: the
vendored `webview.go` hard-requires `pkg-config: … webkit2gtk-4.1`, so a
4.0-only runner fails the build loudly (verified by the `CGO_ENABLED=0` test
above showing this package's constraints are enforced at build time).

Suggested one-token fix, for the orchestrator to apply or defer:
`scripts/ci/guard-webkit-version.sh:17` — add `.github/workflows/ci.yml` to
the grep path list. Deliberately **not** applied here: it widens a guard's
enforcement scope, which is a CI behaviour change beyond this card, and the
reviewer's remit was the workflow diff.

### F2 — Minor (accepted): `ubuntu-latest` vs release's `ubuntu-22.04`

The job compiles against whatever GTK/WebKit headers `ubuntu-latest`
(currently 24.04, WebKitGTK 2.44) ships, while releases build on 22.04
(2.40). A change that compiles on 24.04 headers but not 22.04's would still
reach a release tag. Two follow-on risks: `ubuntu-latest` moving to a release
that drops `libwebkit2gtk-4.1-dev` would redden CI for reasons unrelated to
any PR.

**Accepted.** `runs-on: ubuntu-latest` is the unanimous convention of every
other job in ci.yml (lines 7, 234, 297), the repo's own code here is a thin Go
wrapper with no direct header use, and 4.1 is present on 24.04 (confirmed by
this session's own build linking `libwebkit2gtk-4.1.so.0`). Pinning
`ubuntu-22.04` would be more faithful to release.yml but would make ci.yml
internally inconsistent; the call is defensible either way.

### F3 — Minor (accepted): amd64 only; Windows/macOS shells still uncovered

`release.yml` also builds `linux/arm64` (the Raspberry Pi target), plus the
Windows (mingw) and macOS (WKWebView) shells. This job covers Linux/amd64
only. **Accepted** — the job's own comment states this explicitly and says the
other platforms are tracked separately, and `cmd/unitill-desktop` has no
arch-conditional code (only `linux`/`windows`/`darwin` splits), so an
arm64-only compile break is unlikely to be missed by the amd64 build.

### F4 — Informational: `GOMODCACHE`/`GOCACHE` env vars are inert here

The job sets both env vars to `${{ runner.temp }}/…`, copying the `build`
job's pattern, but has neither the `Prep Go cache dirs` mkdir step nor the
`actions/cache` restore step that give those paths their meaning in `build`.
On a fresh runner they point at empty directories, so they buy nothing.

Harmless, and **not** a correctness problem: verified locally that `go build`
creates a missing `GOCACHE`/`GOMODCACHE` directory itself (ran a build with
both pointed at non-existent paths — exit 0, both directories created). The
only cost is a cold compile each run. Either add the matching `actions/cache`
step or drop the two env keys; leaving them is fine.

### F5 — Informational: `needs: build` gates a fast check behind a slow job

Desktop-shell feedback now arrives only after the `build` job's ~20 guards,
full test suite, a 20-minute-timeout package and three locale test runs have
all passed — and is skipped entirely if any of them fail. That is exactly the
`e2e`/`contract` convention and is what the card asked for, so it is correct
as written. Worth noting only because ci.yml itself argues the other way at
lines 13–17 ("so that check runs with the other fast guards and actually fails
fast — not after `needs: build` gates it"). A compile check is cheap enough to
run unconditionally in parallel; if desktop-shell breakages ever become common
enough to want earlier signal, dropping `needs:` is the lever.

### F6 — Informational (ops, not code): branch protection

`desktop-shell` is a new check name. If `main`'s branch protection lists
required checks explicitly, this job will run but not block until it is added
there. Worth a look after merge so the job doesn't become advisory in
practice.

---

## Verdict

**Safe to merge.** The job is syntactically valid, correctly indented as a
sibling job, consistent with ci.yml's conventions, a faithful mirror of
release.yml's apt/tag/CGO recipe, free of secrets or user-facing surface, and
— the part that matters — was proven by deliberate breakage to fail on exactly
the class of defect the card describes while the pre-existing `build` job
passes on it. No blocking issues. F1 is the one finding worth a follow-up.
