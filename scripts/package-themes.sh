#!/usr/bin/env bash
# Packages each theme plugin under plugins/themes/ into an importable
# .tar.gz bundle (manifest.json at the archive root — the layout
# Importer.Import / the plugins-page "import from file" expects).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC_DIR="$REPO_ROOT/plugins/themes"
OUT_DIR="${1:-$REPO_ROOT/dist/themes}"

mkdir -p "$OUT_DIR"

for dir in "$SRC_DIR"/*/; do
  name="$(basename "$dir")"
  [ -f "$dir/manifest.json" ] || { echo "skip $name (no manifest.json)"; continue; }
  version="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$dir/manifest.json" | head -1)"
  out="$OUT_DIR/${name}-${version}.tar.gz"
  # Archive explicit top-level entries (no "./" members — the POS importer
  # rejects them as path traversal).
  entries=()
  while IFS= read -r e; do entries+=("$(basename "$e")"); done < <(find "$dir" -mindepth 1 -maxdepth 1)
  tar -czf "$out" -C "$dir" "${entries[@]}"
  echo "packaged $out"
done
