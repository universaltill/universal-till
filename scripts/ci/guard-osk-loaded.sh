#!/usr/bin/env bash
#
# Guard: every standalone document that has a text-like input loads
# web/public/osk.js (ut-docs#1096) — the till's own on-screen keyboard,
# which exists precisely because kiosk Pis have no OS keyboard at all.
#
# Same shape of gap as ut-docs#400 (guard-autofill-suppression.sh) and
# ut-docs#344 (guard-htmx-loaded.sh): most pages render via
# web/ui/layouts/base.html, which loads osk.js for everyone — but
# login.html and setup.html are whole standalone documents responsible for
# their own <script> tags, and both silently omitted it. setup has 11 text
# inputs, login has 2; on a keyboard-less touchscreen neither page could be
# operated at all.
#
# UNLIKE guard-autofill-suppression.sh, this guard is INPUT-AWARE, not a
# blanket "every standalone document" rule: self_order.html,
# self_order_shop.html and order_tracking.html are also standalone
# documents but ship zero text-like inputs today, so requiring osk.js
# there would be enforcing a no-op. A standalone document only needs
# osk.js once it actually has something to type into — mirroring osk.js's
# own wantsOSK() definition of "OSK-able" exactly (web/public/osk.js),
# so the guard's notion of "needs a keyboard" can't drift from the
# runtime's.
#
# Three invariants:
#   1. Every standalone document containing a text-like input (an <input>
#      of type text/search/password/email/url/number/tel, an <input> with
#      no type attribute at all — the HTML default is text — or a
#      <textarea>) loads web/public/osk.js.
#   2. Every standalone document that is a LAYOUT (web/ui/layouts/**) loads
#      it unconditionally — its own file has no <input> of its own, but the
#      pages it wraps do. See is_layout() below.
#   3. web/public/osk.js still contains its own machinery (wantsOSK,
#      guardSweep, MutationObserver, LAYOUTS) — a refactor could plausibly
#      delete or rename the whole IIFE without breaking any Go test, since
#      none of this is exercised by `go test ./...`.
#
# This is a static presence check, not a behavioural one — the touch-device
# spec in e2e/tests/login.spec.ts covers the actual runtime behaviour (real
# Chromium, the keyboard actually appearing on a tap). The two are
# complementary: this guard is cheap and catches "the mechanism was
# deleted or a document silently stopped loading it"; the e2e spec catches
# "the mechanism is present but wrong".
#
# Explicit file arguments run this guard against fixtures instead of the
# real tree (see guard-osk-loaded_test.sh) — same convention as
# guard-autofill-suppression.sh/guard-htmx-loaded.sh: the first argument is
# a UI directory to scan for standalone documents, the second is the
# osk.js path to check.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
UI_DIR="${ROOT_DIR}/web/ui"
OSK_JS="${ROOT_DIR}/web/public/osk.js"

if [ "$#" -ge 1 ]; then UI_DIR="$1"; fi
if [ "$#" -ge 2 ]; then OSK_JS="$2"; fi

if [ ! -d "$UI_DIR" ]; then
  echo "❌ osk guard: ${UI_DIR} does not exist" >&2
  exit 1
fi
if [ ! -f "$OSK_JS" ]; then
  echo "❌ osk guard: ${OSK_JS} does not exist" >&2
  exit 1
fi

failed=0
checked=0
required=0

strip_html_comments() { perl -0777 -pe 's/<!--.*?-->//gs' "$1"; }
strip_js_comments() { perl -0777 -pe 's{//[^\n]*}{}g; s{/\*.*?\*/}{}gs' "$1"; }

# Whether a document contains something osk.js would open for. All three
# probes run in ONE slurped (-0777) perl pass, so `[^>]*` spans newlines:
# a multi-line <input> tag is this repo's prevailing house style (19
# templates under web/ui/ format them that way today), and a line-oriented
# grep can't see a `type=` sitting on a later line than its own `<input`.
# That mattered for real: the explicit-type probe used to be a `grep -E`,
# which silently missed every multi-line texty input, while the untyped
# probe below — already perl/slurped — correctly saw the `type=` across the
# newline and so declined to count it as untyped either. A tag written in
# the repo's own style therefore fell through BOTH probes and the page got
# a free pass (found in review of ut-docs#1096).
#
# Probe 1: texty <input> types, mirrored 1:1 from osk.js's own wantsOSK():
#          ['text', 'search', 'password', 'email', 'url', 'number', 'tel'].
# Probe 2: <textarea> — wantsOSK() returns true for it unconditionally.
# Probe 3: an <input> with no type= attribute at all defaults to
#          type="text" per the HTML spec, so it counts too.
has_texty_input() {
  printf '%s' "$1" | perl -0777 -ne '
    exit 0 if m{<input\b[^>]*\btype=["\x27]?(?:text|search|password|email|url|number|tel)}is;
    exit 0 if m{<textarea\b}is;
    exit 0 if m{<input\b(?![^>]*\btype=)[^>]*/?>}is;
    exit 1;
  '
}

# A LAYOUT is always required to load osk.js, whether or not the layout file
# itself happens to contain an <input>. web/ui/layouts/base.html contains
# none — every field on the ~29 pages it wraps lives in a page template or a
# partial that gets composed in at render time — so the input-aware rule
# above would leave the single most important document in the app
# unguarded: deleting its osk.js <script> would take the keyboard away from
# every base-layout page at once (strictly worse than the ut-docs#1096 bug
# this guard exists for) while the guard stayed green. Found in review.
is_layout() {
  case "$1" in
    */layouts/*) return 0 ;;
  esac
  return 1
}

while IFS= read -r -d '' tpl; do
  stripped="$(strip_html_comments "$tpl")"
  # Whole documents only: a partial has no <html> and inherits from its
  # layout (web/ui/layouts/base.html), which is checked as one of these
  # documents in its own right.
  printf '%s' "$stripped" | grep -qE '^[[:space:]]*(<!DOCTYPE|<html\b)' || continue

  checked=$((checked + 1))

  if ! is_layout "$tpl" && ! has_texty_input "$stripped"; then
    continue
  fi
  required=$((required + 1))

  if ! printf '%s' "$stripped" | grep -qE '<script[^>]*src="[^"]*\bosk\.js'; then
    rel="${tpl#"${ROOT_DIR}/"}"
    echo "❌ osk guard: ${rel} is a standalone document with a text-like" >&2
    echo "   input but never loads osk.js — the on-screen keyboard would" >&2
    echo "   never appear on this page on a keyboard-less touchscreen." >&2
    echo "   Fix: add the <script> tag (see web/ui/layouts/base.html or" >&2
    echo "   web/ui/pages/login.html)." >&2
    failed=1
  fi
done < <(find "$UI_DIR" -name '*.html' -print0)

if [ "$checked" -eq 0 ]; then
  # Fail closed: if this ever finds nothing to check, the templates moved or
  # the <html> detection drifted, and the guard is no longer guarding.
  echo "❌ osk guard: no standalone document was found under ${UI_DIR#"${ROOT_DIR}/"}." >&2
  echo "   Expected several (e.g. web/ui/layouts/base.html, login.html)." >&2
  exit 1
fi

stripped_js="$(strip_js_comments "$OSK_JS")"
for marker in 'wantsOSK' 'guardSweep' 'MutationObserver' 'LAYOUTS'; do
  if ! printf '%s' "$stripped_js" | grep -qF -- "$marker"; then
    echo "❌ osk guard: ${OSK_JS} is missing '${marker}' (outside comments)" >&2
    echo "   — part of the on-screen keyboard's own machinery looks to have" >&2
    echo "   been removed or renamed. See e2e/tests/osk-central-guard.spec.ts" >&2
    echo "   and e2e/tests/login.spec.ts for what actual behaviour this" >&2
    echo "   protects." >&2
    failed=1
  fi
done

if [ "$failed" -ne 0 ]; then
  exit 1
fi

echo "✓ osk guard: ${checked} standalone document(s) checked, ${required} needed osk.js and load it, and its machinery is intact"
