#!/usr/bin/env bash
#
# ut-docs#2184: asserts the actual acceptance criterion — "two consecutive
# `make docs-shots` runs with no source change produce byte-identical output
# for every PNG and for manifest.json" — for real, by running the harness
# TWICE and comparing bytes, rather than by inspection or theory.
#
# This is NOT the same check as guard-docs-shots.sh. That guard is cheap
# (recomputes source-surface hashes, never launches a browser) and runs on
# every PR via ci.yml's `build` job. This one launches the real Playwright
# docs-shots harness twice — 124 screenshots each run, ~2.5-4 minutes per
# run on the reference runner — specifically to catch RENDERING
# nondeterminism (encoder/rasterisation/GPU-compositor noise) that a
# source-hash comparison can never see, because it never looks at a PNG's
# bytes. Deliberately kept out of the cheap, every-PR `build` job for that
# cost reason; see .github/workflows/docs-shots-determinism.yml for where
# and how often this actually runs.
#
# NON-DESTRUCTIVE: web/help/img/ (whatever is currently committed/staged in
# the working tree) is backed up before this runs and restored on exit,
# success or failure alike. This script PROVES a property; it does not
# regenerate the real screenshots — use `make docs-shots` directly for that.
#
# Root cause this was written to catch (found live, ut-docs#2184): GPU-
# accelerated rasterization is Chromium's default even in headless mode
# (it uses SwiftShader for GL), and its compositor/raster thread scheduling
# is not guaranteed bit-exact run-to-run — a handful of anti-aliased pixels
# on a repeated vector icon (same relative offset in every catalog tile)
# rendered differently between two otherwise-identical runs. Fixed in
# e2e/playwright.docs.config.ts by forcing software rasterization
# (--disable-gpu et al.) for the docs-shots harness's OWN Chromium process
# only. This script is what stops that fix from silently regressing —
# e.g. someone "optimizing" docs-shots by re-enabling the GPU path, or a
# future Chromium version needing a different flag to stay deterministic.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

IMG_DIR="web/help/img"
if [ ! -d "$IMG_DIR" ]; then
  echo "guard-docs-shots-determinism: $IMG_DIR not found — run from the repo root." >&2
  exit 1
fi

WORK="$(mktemp -d)"
BACKUP="$WORK/original"
RUN_A="$WORK/run-a"
RUN_B="$WORK/run-b"
mkdir -p "$BACKUP" "$RUN_A" "$RUN_B"

echo "guard-docs-shots-determinism: backing up $IMG_DIR"
cp -r "$IMG_DIR"/. "$BACKUP"/

restore_original() {
  rm -rf "${ROOT:?}/$IMG_DIR"
  mkdir -p "$ROOT/$IMG_DIR"
  cp -r "$BACKUP"/. "$ROOT/$IMG_DIR"/
  rm -rf "$WORK"
}
trap restore_original EXIT

echo "guard-docs-shots-determinism: run A — bash e2e/scripts/docs-shots.sh"
bash e2e/scripts/docs-shots.sh
cp -r "$IMG_DIR"/. "$RUN_A"/

echo "guard-docs-shots-determinism: run B — bash e2e/scripts/docs-shots.sh"
bash e2e/scripts/docs-shots.sh
cp -r "$IMG_DIR"/. "$RUN_B"/

fail=0
mismatches=()

# Any file present in run A must exist, byte-identical, in run B — a topic
# appearing in one run and not the other is exactly as much a determinism
# failure as differing bytes.
while IFS= read -r -d '' f; do
  rel="${f#"$RUN_A"/}"
  if [ ! -e "$RUN_B/$rel" ]; then
    mismatches+=("$rel: present in run A, MISSING in run B")
    fail=1
    continue
  fi
  if ! cmp -s "$f" "$RUN_B/$rel"; then
    mismatches+=("$rel: differs between run A and run B")
    fail=1
  fi
done < <(find "$RUN_A" -type f -print0)

while IFS= read -r -d '' f; do
  rel="${f#"$RUN_B"/}"
  if [ ! -e "$RUN_A/$rel" ]; then
    mismatches+=("$rel: present in run B, MISSING in run A")
    fail=1
  fi
done < <(find "$RUN_B" -type f -print0)

if [ "$fail" -ne 0 ]; then
  {
    echo "guard-docs-shots-determinism: FAIL — docs-shots is not deterministic."
    echo "Two consecutive runs on an identical tree (no source change)"
    echo "produced different output for ${#mismatches[@]} file(s):"
    for m in "${mismatches[@]}"; do echo "  - $m"; done
    echo
    echo "This is the ut-docs#2184 property: byte-identical output across"
    echo "repeated runs. Look for a rendering-nondeterminism source (GPU"
    echo "rasterization, font hinting, an unmasked timing-dependent element,"
    echo "an unpinned deviceScaleFactor) in e2e/playwright.docs.config.ts /"
    echo "e2e/tests-docs/docs-shots.spec.ts — do not silence this by re-"
    echo "running until it happens to match."
  } >&2
  exit 1
fi

count=$(find "$RUN_A" -type f | wc -l | tr -d ' ')
echo "guard-docs-shots-determinism: PASS — $count files (PNGs + manifest.json) byte-identical across two independent runs."
