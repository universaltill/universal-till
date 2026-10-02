#!/usr/bin/env bash
#
# Guard: no inline event-handler JavaScript in the server-rendered UI
# (ut-docs#3325, CSP slice 2).
#
# Why: an inline on*= attribute (onclick=, onerror=, oninput=, ...) is a
# script-src-attr violation, and an hx-on* attribute is evaluated by htmx
# through Function() — an eval. Neither survives a script-src without
# 'unsafe-inline'/'unsafe-eval', which is where CSP slices 3/4
# (ut-docs#3326/#3327) take the policy; slice 1's report-only header
# (UT_CSP_REPORT_ONLY, /csp-report) shows them firing today. The behaviour
# lives in web/public/inline-actions.js instead, driven by data-* attributes
# through delegated listeners, so htmx-swapped markup needs no re-binding.
#
# Two invariants:
#   1. No template under web/ui contains `hx-on` or ` on<event>=` outside a
#      comment, except an exact `file:line` entry (path relative to the UI
#      dir, e.g. `pages/index.html:44`) in
#      scripts/ci/inline-handler-allowlist.txt. Each such entry needs a
#      same-file source comment saying why it cannot move. A whole-file
#      entry is not accepted.
#   2. web/public/inline-actions.js still registers its delegated listeners
#      (outside comments): click, capture-phase error (error does not
#      bubble), htmx:afterRequest and htmx:beforeRequest — a refactor could
#      otherwise delete the delegation without any Go test noticing. The
#      checks are per line, so the error registration must keep its `true`
#      capture flag on the addEventListener line: pass a named handler
#      (`addEventListener('error', onError, true)`), not a multi-line
#      function literal.
#
# Comments are stripped before matching — HTML <!-- -->, Go-template
# {{/* */}}, /* */ (inline <script>/<style>) and whole-line `//` JS
# comments — so prose explaining the old
# pattern does not count. Stripping keeps every newline, so reported line
# numbers (and allowlist entries) match the real file. Handlers built inside
# a <script> string ('hx-on::before-request="..."') are NOT comments and
# are caught.
#
# Explicit arguments run against fixtures (guard-no-inline-handlers_test.sh):
#   $1 UI dir, $2 inline-actions.js path, $3 allowlist path.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
UI_DIR="${ROOT_DIR}/web/ui"
ACTIONS_JS="${ROOT_DIR}/web/public/inline-actions.js"
ALLOWLIST="${ROOT_DIR}/scripts/ci/inline-handler-allowlist.txt"

if [ "$#" -ge 1 ]; then UI_DIR="$1"; fi
if [ "$#" -ge 2 ]; then ACTIONS_JS="$2"; fi
if [ "$#" -ge 3 ]; then ALLOWLIST="$3"; fi

if [ ! -d "$UI_DIR" ]; then
  echo "❌ inline-handler guard: ${UI_DIR} does not exist" >&2
  exit 1
fi

failed=0
checked=0
violations=0

# Replace every comment with only its newlines so line numbers survive.
strip_template_comments() {
  perl -0777 -pe '
    s{<!--.*?-->}{ (my $c = $&) =~ s/[^\n]//g; $c }gse;
    s{\{\{-?\s*/\*.*?\*/\s*-?\}\}}{ (my $c = $&) =~ s/[^\n]//g; $c }gse;
    s{/\*.*?\*/}{ (my $c = $&) =~ s/[^\n]//g; $c }gse;
    s{^([ \t]*)//[^\n]*}{$1}gm;
  ' "$1"
}
strip_js_comments() {
  perl -0777 -pe '
    s{/\*.*?\*/}{ (my $c = $&) =~ s/[^\n]//g; $c }gse;
    s{^([ \t]*)//[^\n]*}{$1}gm;
    s{[ \t]//[^\n]*}{}g;
  ' "$1"
}

# A missing allowlist is an empty one (fail safe: nothing is exempt). Read
# once, into a variable, so each lookup greps a here-string — a pipe into
# grep -q under pipefail can SIGPIPE its writer (guard-pipefail-grep-q.sh).
allow_entries=""
if [ -f "$ALLOWLIST" ]; then
  allow_entries="$(grep -vE '^[[:space:]]*(#|$)' "$ALLOWLIST" || true)"
fi
allowed() {
  [ -n "$allow_entries" ] && grep -qxF -- "$1" <<<"$allow_entries"
}

# An attribute name starts the line or follows whitespace, a closing quote
# (`class="a"onclick=`) or a Go action (`{{ end }}onclick=`); `=` must follow
# directly, so a script's `var online = ...` is not a hit. `-i` because HTML
# attribute names are case-insensitive. `hx-on` only as `hx-on:`, `hx-on-`
# or the legacy `hx-on=`, not the bare word in prose.
HANDLER_RE="hx-on([:-]|=)|(^|[[:space:]\"'}])on[[:alpha:]]+="

while IFS= read -r -d '' tpl; do
  checked=$((checked + 1))
  rel="${tpl#"${UI_DIR}/"}"
  while IFS= read -r hit; do
    [ -n "$hit" ] || continue
    line="${hit%%:*}"
    if allowed "${rel}:${line}"; then continue; fi
    violations=$((violations + 1))
    echo "❌ ${rel}:${line}: ${hit#*:}" | cut -c1-220 >&2
  done < <(strip_template_comments "$tpl" | grep -niE -- "$HANDLER_RE" || true)
done < <(find "$UI_DIR" -name '*.html' -print0)

if [ "$checked" -eq 0 ]; then
  echo "❌ inline-handler guard: no templates found under ${UI_DIR} — the" >&2
  echo "   guard is no longer guarding anything (fail closed)." >&2
  exit 1
fi

if [ "$violations" -ne 0 ]; then
  echo "❌ inline-handler guard: ${violations} inline handler(s) in ${UI_DIR#"${ROOT_DIR}/"}." >&2
  echo "   Move the behaviour into web/public/inline-actions.js and use a" >&2
  echo "   data-action / data-on-after-request / data-fallback attribute" >&2
  echo "   (ut-docs#3325). A genuinely unmovable case goes in" >&2
  echo "   scripts/ci/inline-handler-allowlist.txt as file:line, with a" >&2
  echo "   same-file comment explaining why." >&2
  failed=1
fi

if [ ! -f "$ACTIONS_JS" ]; then
  echo "❌ inline-handler guard: ${ACTIONS_JS} does not exist" >&2
  exit 1
fi

stripped_js="$(strip_js_comments "$ACTIONS_JS")"
q="['\"]"
handler='(function[[:space:]]*\([^)]*\)[[:space:]]*\{\}|[A-Za-z_$][A-Za-z0-9_$.]*)'
check_listener() {
  local name="$1" re="$2"
  if ! grep -qE -- "$re" <<<"$stripped_js"; then
    echo "❌ inline-handler guard: ${ACTIONS_JS} no longer registers its" >&2
    echo "   delegated '${name}' listener (outside comments). Templates rely" >&2
    echo "   on it instead of inline handlers (ut-docs#3325)." >&2
    failed=1
  fi
}
check_listener click "addEventListener\(${q}click${q}"
check_listener 'error (capture phase)' "addEventListener\(${q}error${q}[[:space:]]*,[[:space:]]*${handler}[[:space:]]*,[[:space:]]*true[[:space:]]*\)"
check_listener htmx:afterRequest "addEventListener\(${q}htmx:afterRequest${q}"
check_listener htmx:beforeRequest "addEventListener\(${q}htmx:beforeRequest${q}"

if [ "$failed" -ne 0 ]; then
  exit 1
fi

echo "✓ inline-handler guard: ${checked} template(s) free of inline handlers, and inline-actions.js delegation is intact"
