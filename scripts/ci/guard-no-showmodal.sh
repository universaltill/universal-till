#!/usr/bin/env bash
#
# Guard: no showModal() on till surfaces (ut-docs#2097, product owner
# 2026-09-28). A showModal() dialog makes the rest of the page inert --
# including the persistent status bar and the rail's Lock, which CLAUDE.md
# ("Offline-first", ut-docs#1999) says stay reachable on every surface.
# "Unclickable counts as blocked, no exception for small dialogs." Till
# dialogs open with .show() in an explicit fixed frame (app.css) over the
# shared #ut-scrim (base.html, ut-docs#2873), which keeps the status bar and
# Lock above it; base.html's data-ut-escape-close gives them Escape.
#
# Fails on a `.showModal(` CALL in web/ui/** or web/public/** (.html/.js;
# web/public/vendor excluded). Comments do not count: HTML <!-- -->, Go
# template {{/* */}}, JS/CSS /* */ blocks (also across lines) and `//` line
# comments are stripped before matching, so prose explaining why a dialog is
# NOT showModal() never trips it.
#
# Exempt:
#   - the self-order kiosk (partials/self_order_cart.html,
#     partials/self_order_grid.html): a line opening #selforder-modal. In
#     kiosk mode status/lock/exit must be UNreachable, so it stays modal.
#   - a reviewed exception: a same-line `showmodal:allow <reason>` marker.
#
# Known gap, accepted: comment stripping is lexical, so a `/*` or `<!--`
# inside a JS string/regex can hide what follows until the matching close
# (a missed call, never a false alarm), and a `//` inside a string hides
# the rest of its line. This is a grep, not a parser.
#
# Explicit-args form for fixture-based testing (guard-no-showmodal_test.sh):
# args are the ui dir and the public dir.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
UI_DIR="${ROOT_DIR}/web/ui"
PUBLIC_DIR="${ROOT_DIR}/web/public"
if [ "$#" -ge 1 ]; then UI_DIR="$1"; fi
if [ "$#" -ge 2 ]; then PUBLIC_DIR="$2"; fi

for d in "$UI_DIR" "$PUBLIC_DIR"; do
  if [ ! -d "$d" ]; then
    echo "❌ no-showmodal guard: ${d} does not exist" >&2
    exit 1
  fi
done

files="$( { find "$UI_DIR" -type f \( -name '*.html' -o -name '*.js' \) ;
            find "$PUBLIC_DIR" -path "$PUBLIC_DIR/vendor" -prune -o -type f \( -name '*.html' -o -name '*.js' \) -print ; } | LC_ALL=C sort )"

if [ -z "$files" ]; then
  echo "❌ no-showmodal guard: found no .html/.js files under ${UI_DIR} or ${PUBLIC_DIR}" >&2
  exit 1
fi

# Self-order kiosk files, relative to the ui dir.
KIOSK_FILES="partials/self_order_cart.html partials/self_order_grid.html"

# $files is a newline-separated list of paths without spaces.
# shellcheck disable=SC2086
violations="$(awk -v ui="$UI_DIR/" -v kiosk="$KIOSK_FILES" '
  BEGIN { n = split(kiosk, k, " "); for (i = 1; i <= n; i++) isKiosk[ui k[i]] = 1 }
  FNR == 1 { state = "" }
  function strip(line,    out, a, b, best, op, cl) {
    out = ""
    while (length(line) > 0) {
      if (state != "") {
        b = index(line, state)
        if (b == 0) return out
        line = substr(line, b + length(state)); state = ""
        continue
      }
      a = index(line, "<!--"); b = index(line, "/*")
      if (a == 0 && b == 0) { out = out line; break }
      if (a != 0 && (b == 0 || a < b)) { best = a; op = 4; cl = "-->" }
      else { best = b; op = 2; cl = "*/" }
      out = out substr(line, 1, best - 1)
      line = substr(line, best + op); state = cl
    }
    return out
  }
  {
    code = strip($0)
    sub(/(^|[ \t])\/\/.*$/, "", code)
    if (code !~ /showModal[ \t]*(\(|\.call[ \t]*\(|\.apply[ \t]*\()|\[[ \t]*["\047]showModal["\047][ \t]*\]/) next
    if ($0 ~ /showmodal:allow/) next
    if ((FILENAME in isKiosk) && code ~ /selforder-modal/) next
    print FILENAME ":" FNR ": " $0
  }
' $files)"

if [ -n "$violations" ]; then
  echo "❌ no-showmodal guard: a till dialog is opened with .showModal() (ut-docs#2097)" >&2
  echo "   showModal() makes the status bar and Lock inert. Open it with .show(), give it a" >&2
  echo "   fixed frame + z-index in app.css (see #hold-modal) and data-ut-escape-close;" >&2
  echo "   #ut-scrim (base.html) blocks the page behind it. A reviewed exception carries a" >&2
  echo "   same-line 'showmodal:allow <reason>' marker." >&2
  echo "$violations" >&2
  exit 1
fi

echo "✓ no-showmodal guard: no till dialog opens with .showModal()"
