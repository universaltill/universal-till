# Review — docs-shots determinism: pin the raster tile size (ut-docs#2929)

Date: 2026-09-27 · Lane: `lane:cloud-24` · Branch: `fix/2929-docs-shots-tile-size`
Author model: Opus 5.5 · Reviewer: Fable (independent subagent)

## What shipped

`e2e/playwright.docs.config.ts` (docs-shots harness only): two more Chromium
switches, `--default-tile-width=1024` and `--default-tile-height=1024`. The
harness's full flag set is now `--disable-gpu`, `--disable-gpu-compositing`,
`--disable-partial-raster`, `--disable-skia-runtime-opts`,
`--run-all-compositor-stages-before-draw`, `--force-color-profile=srgb`,
`--default-tile-width=1024`, `--default-tile-height=1024`. The real e2e config
(`playwright.config.ts`) and the product are unchanged.

## Root cause

The earlier diagnostics (`2026-09-27-docs-shots-determinism-diagnostics-2929.md`)
found one signature: 25 anti-aliased pixels on the rail's Orders button and
the Help button's icon. Measured rail geometry at 1024×600:

| rail item | y range |
|---|---|
| Sell | 73.9–121.9 |
| Menu | 127.0–175.0 |
| Inventory | 180.1–228.1 |
| **Orders** | **233.2–281.2** (crosses y=256) |
| **Help** | **468.3–516.3** (crosses y=512) |
| Bug report | 521.4–569.4 |

Orders and Help are the only rail items that straddle a boundary of
Chromium's default 256 px software-raster tiles. They are also the only
items that ever differed. With one tile per layer covering the whole frame,
nothing in the frame crosses a tile edge.

## Evidence

- `workflow_dispatch` of the guard with the flags on `exp/2929-tile`:
  **10/10 byte-identical** (runs 36300402359–36300407305 and
  36300740288–36300745055, two batches of five on the same day). With the
  old ~70% per-pair failure rate, ten passes by chance is about 0.3^10.
- The two baseline dispatches on `main` that ran at the same time both
  failed (runs 36300408629 and 36300409634; one had 5 differing PNGs).
- Locally, the full two-run guard passes with the flags (125 files
  byte-identical). `guard-docs-shots.sh` is still fresh, because the config
  is not part of the hashed surface.

## Review findings (Fable)

1. **Major (conditional), fixed.** The comment claimed "10/10" before the
   second batch finished. The count in the comment and in this record now
   comes from the completed runs.
2. **Minor, fixed.** The tile switches work only on the CPU raster path.
   The comment now says so: if the GPU flags are ever removed, cc derives
   the tile size from the viewport and these switches do nothing.
3. **Nit, fixed.** Tiling is per layer, not per viewport. The comment now
   says "per layer" and "inside the frame". A layer taller than 1024 px
   still has a seam at y=1024, but that is outside the 1024×600 capture.
4. **Nit, fixed.** This record lists the full flag set.

The reviewer checked that the switch strings exist in the local Chromium
build, and that cc clamps the tile size only to the layer bounds (rounded
up to 64) and to the software max texture size (≥ 4096). So 1024 is honoured.
Side effects: one ~2.6 MB tile instead of sixteen 256 KB ones, which does
not matter.

## Verdict

Safe to merge. The PR itself triggers `docs-shots-determinism` (the file is
in its `paths:` filter), which gives one more independent run.
