#!/usr/bin/env bash
#
# ut-docs#5214: GitHub retires hosted runner images on a fixed date, and the
# brownouts before it cancel every run on that label. A workflow that still
# names a retired image must move to its replacement before the date, not
# after it. Any non-comment occurrence of a retired label as a YAML value
# (runs-on:, matrix runner:/os: lists) fails. Comments, including trailing
# ones, are ignored so historical notes can stay.
# When GitHub retires another image, add a row to retired below.
# Usage: guard-retired-runner-images.sh [root-dir]   (default: repo root)
set -euo pipefail

root="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"

# Retired label -> replacement.
retired=(
  "macos-13=macos-15"      # retired 2025-12-04
  "macos-14=macos-15"      # retired 2026-11-02 (ut-docs#5214)
  "ubuntu-20.04=ubuntu-24.04" # retired 2025-04-15
  "windows-2019=windows-2025" # retired 2025-06-30
)

fail=0
shopt -s nullglob
for f in "$root"/.github/workflows/*.yml "$root"/.github/workflows/*.yaml; do
  rel="${f#"$root"/}"
  n=0
  while IFS= read -r line || [ -n "$line" ]; do
    n=$((n + 1))
    code="${line%%#*}" # drop comment text (full-line and trailing)
    for row in "${retired[@]}"; do
      label="${row%%=*}"
      repl="${row#*=}"
      # Label as a whole YAML token, plus its -large/-xlarge variants, which
      # GitHub retires with the base image; any other longer name is not it.
      if [[ "$code" =~ (^|[^A-Za-z0-9._-])"$label"(-large|-xlarge)?($|[^A-Za-z0-9._-]) ]]; then
        echo "❌ guard-retired-runner-images: $rel:$n names retired runner image '$label' — use '$repl'" >&2
        fail=1
      fi
    done
  done <"$f"
done

[ "$fail" -eq 0 ] || exit 1
echo "✅ guard-retired-runner-images: no workflow names a retired runner image"
