#!/usr/bin/env bash
#
# Guard: no native window.confirm on till surfaces (ut-docs#6116, follow-up
# of #5524 and ut-docs#2097). The browser's confirm box is its own modal: it
# makes the persistent status bar and the rail's Lock inert, cannot be
# styled, and is unreachable in kiosk mode. Ask with
# UT.confirm(question, {tone, ok, opener}) -> Promise<boolean>
# (web/public/till-confirm.js) and act only on `true`, or put hx-confirm on an
# htmx request (base.html wires it to the same dialog).
#
# Fails on a CALL to (or reference of) the native confirm in web/ui/** or
# web/public/** (.html/.js; web/public/vendor excluded), in comment-free code:
# bare confirm( / confirm (, confirm.call/.apply/.bind(, window|self|
# globalThis|top|parent|frames .confirm, and ["confirm"] bracket access.
# UT.confirm(, hx-confirm, data-confirm* and a 'confirm': object key do not
# match. Comments are stripped first (HTML <!-- -->, Go template {{/* */}},
# JS/CSS /* */ across lines, `//` line and trailing comments).
#
# No exemption except a reviewed same-line `native-confirm:allow <reason>`
# marker (the reason must start with a letter or digit; a bare marker fails).
#
# Known gaps, accepted: (1) stripping is lexical, so a `/*` or `<!--` inside
# a string can hide code and a `//` inside a string hides the rest of its
# line; (2) a bare `confirm` reference that is not a call (var c = confirm;)
# is not caught; (3) other aliasing (document.defaultView.confirm) is not
# caught; (4) hx-confirm on a template rendered without base.html falls back
# to htmx's vendor native confirm, which this guard cannot see (none exist
# today); (5) alert()/prompt() are not covered.
#
# Explicit-args form for fixture-based testing (guard-no-native-confirm_test.sh):
# args are the ui dir and the public dir.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
UI_DIR="${ROOT_DIR}/web/ui"
PUBLIC_DIR="${ROOT_DIR}/web/public"
if [ "$#" -ge 1 ]; then UI_DIR="$1"; fi
if [ "$#" -ge 2 ]; then PUBLIC_DIR="$2"; fi

for d in "$UI_DIR" "$PUBLIC_DIR"; do
  if [ ! -d "$d" ]; then
    echo "❌ no-native-confirm guard: ${d} does not exist" >&2
    exit 1
  fi
done

files="$( { find "$UI_DIR" -type f \( -name '*.html' -o -name '*.js' \) ;
            find "$PUBLIC_DIR" -path "$PUBLIC_DIR/vendor" -prune -o -type f \( -name '*.html' -o -name '*.js' \) -print ; } | LC_ALL=C sort )"

if [ -z "$files" ]; then
  echo "❌ no-native-confirm guard: found no .html/.js files under ${UI_DIR} or ${PUBLIC_DIR}" >&2
  exit 1
fi

# $files is a newline-separated list of paths without spaces.
# shellcheck disable=SC2086
violations="$(awk '
  FNR == 1 { state = "" }
  # A block comment opens at a `/*` at line start, after whitespace or right
  # after `{{` - not in `image/*` or a path glob. Returns 0 if none.
  function blockopen(line,    i, p, rest) {
    p = 0; rest = line
    while ((i = index(rest, "/*")) > 0) {
      p += i
      if (p == 1 || substr(line, p - 1, 1) ~ /[ \t]/ || substr(line, p - 2, 2) == "{{") return p
      rest = substr(rest, i + 2); p += 1
    }
    return 0
  }
  # A `//` comment starts at line start or after whitespace. Returns 0 if none.
  function linecomment(line,    i, p, rest) {
    p = 0; rest = line
    while ((i = index(rest, "//")) > 0) {
      p += i
      if (p == 1 || substr(line, p - 1, 1) ~ /[ \t]/) return p
      rest = substr(rest, i + 1)
    }
    return 0
  }
  function strip(line,    out, a, b, best, op, cl) {
    out = ""
    while (length(line) > 0) {
      if (state != "") {
        b = index(line, state)
        if (b == 0) return out
        line = substr(line, b + length(state)); state = ""
        continue
      }
      a = index(line, "<!--"); b = blockopen(line); c = linecomment(line)
      if (c != 0 && (a == 0 || c < a) && (b == 0 || c < b)) {
        # a `//` comment ends the code here; a /* or <!-- after it is text
        out = out substr(line, 1, c - 1); break
      }
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
    if (!(code ~ /(^|[^A-Za-z0-9_$.-])confirm[ \t]*\(/ ||
          code ~ /(^|[^A-Za-z0-9_$.-])confirm[ \t]*\.[ \t]*(call|apply|bind)[ \t]*\(/ ||
          code ~ /(^|[^A-Za-z0-9_$.])(window|self|globalThis|top|parent|frames)[ \t]*\.[ \t]*confirm([^A-Za-z0-9_$]|$)/ ||
          code ~ /\[[ \t]*["\047`]confirm["\047`][ \t]*\]/)) next
    if ($0 ~ /native-confirm:allow[ \t]+[A-Za-z0-9]/) next
    if ($0 ~ /native-confirm:allow/) { print FILENAME ":" FNR ": (marker needs a reason) " $0; next }
    print FILENAME ":" FNR ": " $0
  }
' $files)"

if [ -n "$violations" ]; then
  echo "❌ no-native-confirm guard: a till surface asks with the browser's window.confirm (ut-docs#6116)" >&2
  echo "   A native confirm makes the status bar and Lock inert, can't be styled and is" >&2
  echo "   unreachable in kiosk mode. Ask with UT.confirm(question, {opener}) -> Promise<boolean>" >&2
  echo "   (web/public/till-confirm.js), or hx-confirm on an htmx request. A reviewed fallback" >&2
  echo "   carries a same-line 'native-confirm:allow <reason>' marker." >&2
  echo "$violations" >&2
  exit 1
fi

echo "✓ no-native-confirm guard: no till surface calls window.confirm"
