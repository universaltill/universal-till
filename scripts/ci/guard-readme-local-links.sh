#!/usr/bin/env bash
#
# Fails when README.md (or the markdown files passed as arguments) points at
# a repo-relative file that doesn't exist: a screenshot moved or renamed
# under docs/images/ otherwise just shows as a broken image on GitHub, and
# the README is the first thing a visitor sees (ut-docs#2611).
# Only relative targets are checked; http(s)/mailto links and #anchors are
# skipped.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

files=("$@")
[ ${#files[@]} -eq 0 ] && files=(README.md CONTRIBUTING.md)

missing=0
for md in "${files[@]}"; do
  dir="$(dirname "${md}")"
  # Code isn't a link: drop fenced ``` blocks and `inline code` first, so a
  # README that shows link syntax as an example doesn't fail the build.
  # The backticks below are literal markdown code markers, not expansions.
  # shellcheck disable=SC2016
  prose="$(awk '/^[[:space:]]*```/ { fence = !fence; next } !fence' "${md}" | sed 's/`[^`]*`//g')"
  # Markdown ](target) / ](target "title"), plus inline HTML href="…" and
  # src="…" (the README's footer and badges use raw <a>/<img> tags).
  while IFS= read -r target; do
    case "${target}" in
      http://*|https://*|mailto:*|\#*|"") continue ;;
    esac
    path="${target%%#*}"
    if [ ! -e "${dir}/${path}" ]; then
      echo "::error file=${md}::links to missing file: ${target}"
      missing=$((missing + 1))
    fi
  done < <(
    { grep -o '](\([^) ]*\)' <<<"${prose}" || true; } | sed 's/^](//'
    { grep -oE '(href|src)="[^"]*"' <<<"${prose}" || true; } | sed -E 's/^(href|src)="//; s/"$//'
  )
done

if [ "${missing}" -gt 0 ]; then
  echo "guard-readme-local-links: ${missing} broken local link(s)"
  exit 1
fi
echo "guard-readme-local-links: OK (${files[*]})"
