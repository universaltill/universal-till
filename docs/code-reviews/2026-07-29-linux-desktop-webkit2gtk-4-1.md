# 2026-07-29 — Linux desktop shell: webkit2gtk-4.0 → 4.1 (ADR-0028)

## What shipped

`unitill-desktop` (native Linux/Windows/macOS desktop shell) failed to
even `exec` on current Raspberry Pi OS (Debian 13 trixie) — its upstream
dependency `github.com/webview/webview_go` hardcodes a Linux cgo
pkg-config target of `webkit2gtk-4.0`, a package trixie dropped entirely
(`apt-cache policy` shows no installable candidate at all). Root-caused
live on Farshid's actual field-test Pi (`Pi4-1`, SSH-accessible).
Upstream `webview_go` is unmaintained (`go list -m -versions` shows no
newer release) so there was no version to bump to.

Fixed by vendoring a one-line-patched fork in-tree
(`internal/thirdparty/webview_go/`, wired via a `go.mod` `replace`
directive) targeting `webkit2gtk-4.1` instead — a libsoup2→3 soname bump,
not a C API break, confirmed available on every currently-built target
(Debian bookworm, Ubuntu 22.04 jammy, Debian trixie; Tauri v2 made the
same call for the same dual-distro-compat reason). Updated
`.github/workflows/release.yml`'s `linux-shells` job and
`.goreleaser.yaml`'s `.deb` `recommends` list to match. Added a new CI
guard (`scripts/ci/guard-webkit-version.sh`, wired into `ci.yml`) that
fails if `webkit2gtk-4.0` reappears anywhere or if the vendored fork
stops targeting 4.1. Full rationale: `ut-docs/adr/0028-linux-desktop-webkit2gtk-4-1.md`.

## Independent review (different model, opus)

**Verdict: PASS**, no blocking or worth-fixing issues. Everything claimed
by Dev/Tester was independently re-verified with fresh evidence, not
taken on trust:

- `diff -r` against the real upstream module in `GOMODCACHE` confirmed
  the vendored fork differs from upstream in exactly one functional way
  — the pkg-config line in `webview.go` — plus a provenance comment;
  LICENSE/README/`.cc`/`.c`/headers all byte-identical to upstream.
- Independently reverted the fix and re-ran the new guard script — it
  failed with the expected error; restored the fix (byte-exact diff) —
  guard passed again.
- Independently rebuilt `cmd/unitill-desktop -tags desktop` in a fresh
  `ubuntu:22.04`/amd64 container (this pipeline had only tested
  arm64) — zero missing libraries via `ldd`.
- Independently spun up a real `debian:trixie-slim` container and
  confirmed `libgtk-3-0t64 Provides: libgtk-3-0` and that
  `libwebkit2gtk-4.1-0`+`libgtk-3-0` both resolve via
  `apt-get install --dry-run` — validating the `.goreleaser.yaml`
  comment's claim rather than accepting it as asserted.
- Confirmed Windows/macOS build paths are untouched (only
  `webview_fallback.go`, `desktop && !darwin`, imports `webview_go`; the
  patched cgo directive is scoped to `linux openbsd freebsd netbsd`) and
  no other importer of the module exists in this repo.
- Confirmed no secrets or real client/shop names introduced.

Two nitpicks noted, neither actionable: (1) the vendored upstream header
still contains a `libwebkit2gtk-4.0.so` dlopen fallback string inside
`webview.h` — verbatim upstream, tries `4.1` first, harmless, and the
real build-time link is governed by the pkg-config directive the guard
already asserts; (2) the guard's grep scope is intentionally narrow
(the three files this change touches) — a future regression in an
unrelated new file wouldn't be caught, acceptable for now.

## Verified beyond automated tests

- Real build+link on both `linux-shells` CI matrix legs (`ubuntu-22.04`
  amd64 and arm64) reproduced locally via Docker before push.
- Real run on the actual live Pi that reported this bug: the patched
  binary, executed against the Pi's real X11 session (attach-mode against
  the till already answering `/healthz` on :8080), spawned genuine
  `WebKitWebProcess`/`WebKitNetworkProcess` children — the real
  WebKitGTK-4.1 multi-process engine actually initializing, not just a
  successful link. No crash, no missing-symbol error.
- `libwebkit2gtk-4.1-0` installed cleanly on the Pi via real `apt-get
  install` (was previously absent — Candidate existed, nothing had
  pulled it in yet).

## Explicitly deferred / accepted gaps

- **Pixel-level rendering on the physical screen is not verified** —
  process-tree evidence proves the engine initialized and ran, not that
  the window renders correctly on the actual display. Same standing gap
  as the earlier kiosk-mode fix (PR #84) — needs Farshid to look at the
  device directly.
- The guard script's grep scope is narrow by design (see nitpick above);
  widening it to catch a hypothetical future regression elsewhere is a
  possible small follow-up, not blocking.
- No Playwright e2e applies — this change touches no HTML/UI templates,
  only build/packaging config and a vendored C binding.

## Safe to merge

Yes. Feature branch `fix/linux-desktop-webkit2gtk-4-1`, committed and
merged per this pipeline's auto-push authorization.
