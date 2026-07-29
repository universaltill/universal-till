# Code review — menu icons invisible on Raspberry Pi (missing color-emoji font)

- **Date**: 2026-07-29
- **Branch**: `fix/menu-icons-emoji-font-fallback`
- **Field report**: Farshid, 2026-07-29 — "icons on the menu page are not
  appearing" on his live Pi till (`Pi4-1`, kiosk mode).
- **Reviewer**: independent different-model subagent (opus); pipeline
  (fable) implemented, triaged, and re-verified.

## Root cause (verified on the real device, not assumed)

The POS UI renders icons — the `/menu` launcher tiles, status glyphs,
plugin buttons — as **plain Unicode emoji text** (see `iconFor` in
`internal/pages/menu_page.go`), not image/SVG assets. A base Raspberry Pi
OS image ships **no color-emoji font at all**, and the app's CSS
font-family stack had no emoji-capable fallback entry either. Result:
most glyphs rendered as nothing/blank.

Evidence:

- `/var/log/apt/history.log` on the live Pi shows `fonts-noto-color-emoji`
  was first installed 2026-07-29 22:43 (by the interrupted first half of
  this session, as the live hotfix) — before that the device had zero
  emoji fonts (`fc-list | grep -i emoji` empty).
- **Counterfactual reproduced on the device**: a headless Chromium render
  of the exact 14 `iconFor` glyphs with Noto Color Emoji masked via a
  fontconfig `rejectfont` rule shows **9 of the 14 glyphs render as
  literally nothing** (🧾 🎨 📦 📒 📊 🧩 🏷️ 👤 🖥️), matching the field
  report precisely; only the few glyphs with legacy text-presentation
  coverage in other system fonts (🕒 ⚙️ ❓ 🌐 ▪️) survive as monochrome
  outlines. Same render with the font present: all 14 visible.

## The fix (5 files)

1. `web/public/app.css` — body font stack gains
   `"Apple Color Emoji", "Segoe UI Emoji", "Noto Color Emoji", "Segoe UI
   Symbol"` after `sans-serif`. Effective because font fallback is
   per-grapheme (`sans-serif` does not terminate the search), and
   deliberately placed *last* so color-emoji fonts never hijack ASCII
   digits/`#`/`©` (reviewer confirmed this ordering is the idiomatic
   pattern, e.g. Bootstrap/GitHub). Buttons/inputs inherit via the
   existing `font: inherit` rules — verified.
2. `deploy/raspberry-pi/install.sh` — installs `fonts-noto-color-emoji`,
   **best-effort and after the core install** (see review finding 3).
3. `.goreleaser.yaml` — `.deb` `recommends` gains the font package (the
   actual install path of the field device; arch:all, same package name
   on bookworm/jammy/trixie).
4. `packaging/linux/unitill-kiosk-setup.sh` — installs it explicitly,
   because the kiosk flow's own documented install command is
   `apt install --no-install-recommends ./unitill-pos_*.deb`, which
   bypasses `recommends` entirely — i.e. the exact field-reported
   configuration would have stayed broken with only fix (3).
5. `scripts/ci/guard-emoji-font.sh` (new, wired into `ci.yml`) — four
   checks (CSS fallback + all three Linux install paths), each proven
   red against its reverted half before green.

The interrupted first half of this session had shipped (1), (2), and a
first version of (5); this cycle's BA pass found that it missed the two
install paths the real device actually uses — (3) and (4) — and extended
the guard to cover them.

## Independent review findings (opus, different model)

Verdict: **PASS-with-nitpicks**. All four guard halves independently
re-verified red/green by the reviewer (reverted each, confirmed the
specific failure message, restored). Findings, all fixed before commit:

1. **(non-blocking, demonstrated live) Guard false-pass on the
   `.goreleaser.yaml` check** — the recommends block's explanatory
   comment also contains the package name, and the reviewer deleted the
   real `- fonts-noto-color-emoji` entry, kept the comment, and the guard
   still passed. Fixed: anchored to the YAML list-item form
   (`^\s*-\s*fonts-noto-color-emoji\s*$`), mutation-tested.
2. **(nitpick) Same substring-match weakness in the other checks** —
   fixed by anchoring the apt checks to a real (uncommented, unquoted)
   `apt-get install` line. While hardening this, the pipeline found a
   *second* false-pass the reviewer's regex suggestion wouldn't have
   caught: the install script's own failure-hint echo (`sudo apt-get
   install fonts-noto-color-emoji`) satisfied a comment-only exclusion.
   The final regex also excludes quoted contexts; all three script
   checks now mutation-tested (commented-out line, disabled line,
   deleted entry — guard fails on each).
3. **(non-blocking) `install.sh` regression risk** — the first version
   ran `apt-get update && apt-get install` *first* under
   `set -euo pipefail`, so a mirror/network failure would abort the whole
   POS install, which previously needed no network at all (offline
   re-runs are a real use of this script). Fixed: font install moved
   after the core install and made non-fatal with a clear WARN + manual
   remedy hint.

Reviewer explicitly cleared: CSS fallback ordering/semantics, nfpms
`recommends` validity, `--no-install-recommends` interplay, apt
idempotency, i18n precedent for root-run script echo lines, no real
shop names, no secrets.

## Verified beyond automated tests

- **Live device, red/green at the runtime layer**: headless Chromium on
  the actual field Pi rendering the exact menu glyph set — font masked →
  9/14 glyphs invisible (the reported bug); font present → all 14 render.
- Live kiosk screenshot (grim) shows the running till with nav emoji
  (👤, ☰-adjacent glyphs) rendering, v0.2.49 session intact.
- The live device itself is already fixed (font installed 22:43 during
  the interrupted session); this change makes every future install —
  all three Linux paths — get the font without manual intervention.
- Full gate: `go build`, `go vet`, full `go test ./...`, all five CI
  guard scripts, `goreleaser check`, and the full Playwright e2e suite
  (19/19) — all green.

## Explicitly deferred / out of scope

- Replacing emoji-glyph icons with bundled SVG assets (would make icon
  rendering fully deterministic across platforms, but is a design change
  touching ~25 templates — not warranted by this bug once the font is
  guaranteed present on every install path).
- Windows/macOS need nothing (ship system emoji fonts).

**Safe to merge**: yes.
