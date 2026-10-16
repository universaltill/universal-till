#!/usr/bin/env bash
#
# Guard: no physical left/right CSS on till surfaces (ut-docs#4375, ADR-0160
# §8). The till lays out right-to-left for ar/fa/he/ur (httpx.IsRTL drives
# <html dir>), so CSS uses logical properties -- margin-inline-start,
# padding-inline-end, inset-inline-start, border-inline-start,
# text-align: start -- never left/right (CLAUDE.md "RTL"). A physical rule
# looks right in English and puts the gap, border or pinned edge on the wrong
# side in Arabic, Farsi, Hebrew and Urdu.
#
# Scans CSS only:
#   - web/public/**/*.css (web/public/vendor excluded);
#   - in web/ui/**/*.html and web/public/**/*.html: the text inside <style>
#     blocks and style="..."/style='...' attributes (both may span lines).
# JS is not scanned: el.style.left / getBoundingClientRect().left place
# things at measured screen coordinates, which are physical by nature.
#
# Fails on, as a declaration (after `{`, `;` or at the start of the CSS text):
#   margin-/padding-/scroll-margin-/scroll-padding-left|right,
#   border-left|right[-*], border-top|bottom-left|right-radius, left, right,
#   and text-align|float|clear: left|right.
# Out of scope (deliberately): transform-origin, background-position, the
# 4-value inset/margin/padding shorthands.
#
# Comments do not count: CSS/JS /* */ (also across lines, which covers
# {{/* */}}) and HTML <!-- --> are stripped before matching.
#
# Exempt:
#   - a reviewed exception: a same-line `rtl:allow <reason>` marker (e.g. a
#     left: 50% + translateX(-50%) centring, which is symmetric);
#   - a line in the baseline (default scripts/ci/rtl-css-baseline.txt):
#     `<path relative to the repo root><TAB><the line, indentation trimmed>`.
#     The baseline is shrink-only: an entry that no longer matches a
#     violation fails the guard, so a fixed line must leave the file.
#
# Go template actions ({{ if .X }}…{{ end }}) count as declaration
# boundaries. A baseline entry covers as many copies of its line as it is
# listed times. `el.style=` / `:style=` bindings are not style attributes.
#
# Known gap, accepted: comment stripping is lexical, so a `/*` inside a
# string (other than a quoted glob like accept="image/*") hides what follows
# until the next `*/` (a missed rule, never a false alarm). Out of scope too:
# 4-value border-radius, box-shadow offsets, translateX, and
# internal/recovery/templates. This is a grep, not a parser.
#
# Explicit-args form for fixture-based testing (guard-rtl-logical-css_test.sh):
#   guard-rtl-logical-css.sh [ROOT [BASELINE]]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
BASELINE="${SCRIPT_DIR}/rtl-css-baseline.txt"
if [ "$#" -ge 1 ]; then ROOT="$(cd "$1" && pwd)"; fi
if [ "$#" -ge 2 ]; then BASELINE="$2"; fi

UI_DIR="${ROOT}/web/ui"
PUBLIC_DIR="${ROOT}/web/public"
for d in "$UI_DIR" "$PUBLIC_DIR"; do
  if [ ! -d "$d" ]; then
    echo "❌ rtl-logical-css guard: ${d} does not exist" >&2
    exit 1
  fi
done
if [ ! -f "$BASELINE" ]; then
  echo "❌ rtl-logical-css guard: baseline ${BASELINE} does not exist" >&2
  exit 1
fi

files="$( { find "$UI_DIR" -type f -name '*.html' ;
            find "$PUBLIC_DIR" -path "$PUBLIC_DIR/vendor" -prune -o -type f \( -name '*.css' -o -name '*.html' \) -print ; } | LC_ALL=C sort )"
if [ -z "$files" ]; then
  echo "❌ rtl-logical-css guard: found no .css/.html files under ${UI_DIR} or ${PUBLIC_DIR}" >&2
  exit 1
fi

# $files is a newline-separated list of paths without spaces.
# shellcheck disable=SC2086
result="$(awk -v root="${ROOT}/" -v baseline="$BASELINE" '
  function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t\r]+$/, "", s); return s }
  # Strip /* */ and <!-- --> comments; `cstate` carries an open one across lines.
  function strip(line,    out, a, b, best, op, cl) {
    out = ""
    while (length(line) > 0) {
      if (cstate != "") {
        b = index(line, cstate)
        if (b == 0) return out
        line = substr(line, b + length(cstate)); cstate = ""
        continue
      }
      a = index(line, "<!--"); b = index(line, "/*")
      # `image/*"` (accept=) is an attribute glob, not a comment opener.
      if (b != 0 && substr(line, b + 2, 1) ~ /["\047]/) {
        if (a == 0 || b < a) { out = out substr(line, 1, b + 1); line = substr(line, b + 2); continue }
      }
      if (a == 0 && b == 0) { out = out line; break }
      if (a != 0 && (b == 0 || a < b)) { best = a; op = 4; cl = "-->" }
      else { best = b; op = 2; cl = "*/" }
      out = out substr(line, 1, best - 1)
      line = substr(line, best + op); cstate = cl
    }
    return out
  }
  # The CSS text on an HTML line: <style> block content and style attribute
  # values, each segment prefixed with ";" so it reads as a fresh declaration.
  # `inBlock` / `inAttr` (the open quote) carry across lines.
  function css(line,    out, lo, a, b, rest, g) {
    out = ""
    while (length(line) > 0) {
      if (inBlock) {
        a = index(tolower(line), "</style")
        if (a == 0) return out ";" line
        out = out ";" substr(line, 1, a - 1); line = substr(line, a + 7); inBlock = 0
        continue
      }
      if (inAttr != "") {
        a = index(line, inAttr)
        if (a == 0) return out ";" line
        out = out ";" substr(line, 1, a - 1); line = substr(line, a + 1); inAttr = ""
        continue
      }
      lo = tolower(line)
      a = index(lo, "<style")
      b = match(lo, /(^|[^a-z0-9_.:$-])style[ \t]*=[ \t]*["\047]/)
      if (a == 0 && b == 0) return out
      if (a != 0 && (b == 0 || a < RSTART)) {
        rest = substr(line, a); g = index(rest, ">"); inBlock = 1
        if (g == 0) return out
        line = substr(rest, g + 1)
        continue
      }
      inAttr = substr(line, RSTART + RLENGTH - 1, 1)
      line = substr(line, RSTART + RLENGTH)
    }
    return out
  }
  function physical(c) {
    c = tolower(c)
    gsub(/\{\{[^}]*\}\}/, ";", c)
    return c ~ /(^|[{;])[ \t]*(scroll-)?(margin|padding)-(left|right)[ \t]*:/ ||
           c ~ /(^|[{;])[ \t]*border-(left|right)(-[a-z]+)?[ \t]*:/ ||
           c ~ /(^|[{;])[ \t]*border-(top|bottom)-(left|right)-radius[ \t]*:/ ||
           c ~ /(^|[{;])[ \t]*(left|right)[ \t]*:/ ||
           c ~ /(^|[{;])[ \t]*(text-align|float|clear)[ \t]*:[ \t]*(left|right)[ \t]*(;|}|!|$)/
  }
  BEGIN {
    while ((getline l < baseline) > 0) {
      if (l ~ /^[ \t]*(#|$)/) continue
      t = index(l, "\t")
      if (t == 0) { print "BADBASE " l; continue }
      key = substr(l, 1, t - 1) "\t" trim(substr(l, t + 1))
      if (!(key in base)) order[++nb] = key
      base[key]++
    }
  }
  FNR == 1 { cstate = ""; inBlock = 0; inAttr = ""; rel = substr(FILENAME, length(root) + 1); isCSS = (FILENAME ~ /\.css$/) }
  {
    code = strip($0)
    c = isCSS ? ";" code : css(code)
    if (c == "" || !physical(c)) next
    if ($0 ~ /rtl:allow/) next
    key = rel "\t" trim($0)
    if ((key in base) && used[key] < base[key]) { used[key]++; next }
    print "VIOLATION " rel ":" FNR ": " trim($0)
  }
  END {
    for (i = 1; i <= nb; i++) if (used[order[i]] < base[order[i]]) print "STALE " order[i]
  }
' $files)"

violations="$(printf '%s\n' "$result" | sed -n 's/^VIOLATION //p')"
stale="$(printf '%s\n' "$result" | sed -n 's/^STALE //p')"
badbase="$(printf '%s\n' "$result" | sed -n 's/^BADBASE //p')"

status=0
if [ -n "$violations" ]; then
  echo "❌ rtl-logical-css guard: physical left/right CSS (ut-docs#4375)" >&2
  echo "   The till lays out right-to-left for ar/fa/he/ur. Use the logical property:" >&2
  echo "   margin-inline-start/-end, padding-inline-*, border-inline-*, inset-inline-start/-end," >&2
  echo "   border-start-start-radius, text-align: start/end (prefer flex/grid over float)." >&2
  echo "   A reviewed exception carries a same-line 'rtl:allow <reason>' marker." >&2
  echo "$violations" >&2
  status=1
fi
if [ -n "$stale" ]; then
  echo "❌ rtl-logical-css guard: baseline entries that no longer match (${BASELINE})" >&2
  echo "   The baseline is shrink-only: remove each fixed or moved line below." >&2
  printf '%s\n' "$stale" | sed 's/\t/  ->  /' >&2
  status=1
fi
if [ -n "$badbase" ]; then
  echo "❌ rtl-logical-css guard: malformed baseline line (want <path><TAB><line>):" >&2
  echo "$badbase" >&2
  status=1
fi
[ "$status" -eq 0 ] || exit 1

echo "✓ rtl-logical-css guard: no physical left/right CSS outside the baseline"
