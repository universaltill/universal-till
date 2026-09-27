# Review: Auto effects → Light on every Linux (WebKitGTK) till (ut-docs#2992)

ADR-0119 §4's shell-engine signal, split out of #2926. Built by Sonnet
(complexity:easy), reviewed by Opus 5.5 in a fresh worktree.

## What shipped

- `internal/fxlevel`: `Signals.Engine`, derived on the host by
  `shellEngine(GOOS)` — `webkitgtk` on `linux`, unknown elsewhere. It is a
  hard Light trigger in `Detect`, adds a `webkitgtk` reason token right
  after `pi` (`pi,webkitgtk,cores=4,ram=16g`), and is part of the
  fingerprint (`|engine=…`), so a Linux till that stored `detected=full`
  re-detects at its next boot. `ParseReason` accepts the new flag token.
- Settings → Display renders it as "WebKitGTK" (new key
  `settings.display.effects_reason_webkitgtk`, a product name, same in
  every locale). `effects_help` and the manual (`display.md` step 20, 5
  languages) say Auto picks Light on Linux.
- Android, Windows and macOS are unchanged; an explicit operator level is
  never overwritten.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Every `GOOS=linux` host counts as WebKitGTK, including the headless server (Docker, a Linux box serving only LAN browsers/tablets), which now defaults to Light for every browser it serves. ADR-0119 §4 says "a desktop shell attached on linux". | Accepted: the card (owner request "no effects on the Pi and Linux") scopes it to every Linux till, and whether a shell attaches is only known ~60 s after boot. An operator can pick Full/Balanced explicitly. |
| 2 | minor | Dev regenerated all 124 manual screenshots with an unpinned local Chromium (the pinned one was unreachable); the `display` shots only changed font rendering and don't show the effects line. | Fixed: every PNG restored to `main`; only the `display` hashes in `manifest.json` move (guard-docs-shots green). |
| 3 | nit | The fingerprint now always carries `engine=`, so non-Linux tills re-detect once after upgrade (same result, one settings write + one log line). No repeated churn: the engine derives from constant `GOOS`. | Accepted. |
| 4 | nit | ar/fa `effects_help` wrote "Linux" in Latin script; the help topic and the rest of those locales use لينكس / لینوکس. | Fixed. |
| 5 | nit | `internal/pages` fixtures set `Engine` directly (the seam skips `ReadSignals`); GOOS→engine is proven in `fxlevel`'s `TestReadSignals*`. | Accepted. |

## Verified

- TDD: tests written first (failed to compile on the missing `Engine`);
  the reviewer additionally ran six mutations (engine off on linux, Detect
  ignoring it, fingerprint dropping it, ParseReason dropping it, reason
  token omitted, android treated as WebKitGTK) — each caught by a failing
  test.
- Driven run on a real 4-core/16 GB Linux host: the `main` binary stored
  `full (cores=4,ram=16g)` and served `fx-full`; the branch binary on the
  same data dir re-detected `light (webkitgtk,cores=4,ram=16g)`, served
  `<html class="fx-light">` and Settings read "Light (WebKitGTK, 4 CPU
  cores, 16 GB memory)" (fa rendered "WebKitGTK، 4 هستهٔ پردازنده…");
  `POST level=full` then served `fx-full`.
- e2e: the default project and docs-shots pin the level explicitly; the
  four static-server projects now run Light on Linux CI but none asserts
  motion (and they already run reduced-motion).
- Gates: gofmt, build, vet, `go test ./...`, guard-i18n, guard-help-drift,
  guard-help-topics, guard-docs-shots, guard-core-neutral,
  guard-compliance-claims, guard-competitor-naming.
- Visual: no layout change; the Settings line was checked as rendered
  HTML (en, fa), not screenshotted.

## Language packs

New key → `ut-plugin-language-de` / `-es` follow-up PRs in the same cycle
(key allowlisted as same-as-English, `effects_help` retranslated, version
bumped).

**Verdict:** safe to merge.
