#!/usr/bin/env bash
#
# Guard: no inline event-handler attributes in web/ui (ut-docs#3325, CSP
# slice 2 of ut-docs#2913).
#
# Why: an inline handler -- onclick=, oninput=, onerror=, onsubmit=, ... --
# is exactly what CSP's `script-src-attr` directive governs, and htmx's
# hx-on / hx-on::<event> attributes are worse: htmx compiles their text
# with `new Function()`, which needs 'unsafe-eval'. Either one blocks the
# enforced policy that ut-docs#3327 will turn on; slice 1's report-only
# inventory (/csp-report, e2e/tests/csp-report-only-2913.spec.ts) is what
# found them. Every one of them now lives in web/public/inline-actions.js
# as a delegated listener driven by data-* attributes (data-action,
# data-fallback, data-after-request, ...) -- see that file's header for
# the vocabulary.
#
# Four invariants:
#   1. `grep -rniE "hx-on|(^|[^a-z0-9_-])on[a-z]+(=|[[:space:]]+=[[:space:]]*[\"'])" web/ui`
#      comes back empty -- a superset of the card's `\son[a-z]+=`, so a
#      handler built in a JS string (`'onclick="..."'`) is caught too, and so
#      is one with whitespace before its `=` and a quoted value
#      (`onclick ="..."`, valid HTML), while the data-* names (data-action,
#      data-before-request, ...) never match (none of them is itself "on"
#      immediately followed by letters) --
#      except for a line whose exact `web/ui/<path>:<line>` is listed in
#      scripts/ci/inline-handler-allowlist.txt (each entry needs a same-file
#      comment in the template saying why it can't be converted). This is a
#      deliberately RAW grep: comments count, and so does markup built
#      inside a <script> string. A lexical comment-stripper can be fooled
#      (a `<!--` in a JS string hides what follows), and a handler built in
#      a string is still a real handler once htmx.process() sees it.
#      Case-insensitive, because HTML attribute names are (ONCLICK= runs).
#   2. Every standalone document under web/ui (contains <html>, so it owns
#      its own <script> tags -- base.html, setup.html, self_order.html, ...)
#      loads inline-actions.js; without it every data-* hook on that page is
#      dead, silently. HTML comments are stripped for THIS check, so a
#      commented-out <script> tag doesn't count (guard-htmx-loaded.sh's
#      history).
#   3. web/public/inline-actions.js still registers its delegated
#      listeners -- 'click', 'error' (capture phase: `error` does not
#      bubble) and 'htmx:afterRequest' / 'htmx:beforeRequest' -- outside
#      comments, so a refactor can't delete the delegation while every
#      template keeps carrying attributes nothing reads.
#   4. No attribute in web/ui is named data-on<anything> (data-on-after-
#      request, data-only, ...): Go's html/template strips "data-" and then
#      JS-escapes any name that starts with "on", so a templated id inside
#      such a value renders JSON-quoted and never matches the real DOM id
#      (the ut-docs#3325 tester-pass regression, fixed by renaming the whole
#      hook family). Comments count here too, same as invariant 1.
#
# Static presence check only; the runtime behaviour is covered by the e2e
# suite (every converted dialog/tender/settings flow) and by
# e2e/tests/csp-report-only-2913.spec.ts, whose violation inventory must
# no longer list script-src-attr.
#
# 5. internal/pages/*.go sometimes hand-builds HTML (fmt.Fprintf/
#    b.WriteString with a backtick or quoted string) outside any web/ui
#    template, so invariant 1's scan can't see it (ut-docs#3506 -- #3510's
#    own Dev investigation found and fixed three live instances of this
#    without adding a guard against a fourth). Same violation class, but a
#    narrower pattern than invariant 1's: it requires a quote right after
#    `=` (`on[a-z]+="..."`, `on[a-z]+ ="..."`, `hx-on...="..."`) so it catches
#    the HTML shape a real browser runs as a handler, but deliberately never
#    matches the unrelated `el.onclick=function(){...}` DOM-property
#    assignment Go code also writes inside <script> blocks (that one's a
#    script-src concern for #3327, not script-src-attr -- #3506's own
#    finding) and never a comment merely mentioning hx-on/onclick in prose
#    (invariant 1's bare "hx-on" substring alternative would false-fire on
#    internal/pages' own doc comments, e.g. "fires from hx-on::after-request
#    AFTER ..." -- this check intentionally doesn't reuse that alternative).
#    Skips `_test.go` (fixtures/test subjects, not rendered pages -- same
#    carve-out as guard-plugin-menu-read.sh). Allowlist: same file as
#    invariant 1's, keys written as internal/pages/<path>:<line>.
#
#    Alpine.js x-on:/@click directives are explicitly OUT of scope here too,
#    for both web/ui and internal/pages: none exist anywhere in the repo
#    (checked at the time of this decision, ut-docs#3506) and they aren't a
#    script-src-attr violation under CSP3's 'unsafe-hashes'/'unsafe-eval'
#    model the way onclick= is, so adding detection now would guard against
#    a shape that may never need the same treatment. If one lands, #3327
#    (CSP slice 4 enforcement) is where its CSP fit gets decided first.
#
# Explicit arguments run it against fixtures (guard-no-inline-handlers_test.sh):
# $1 = UI dir, $2 = inline-actions.js, $3 = allowlist, $4 = internal/pages
# dir. Allowlist keys are written as web/ui/<path relative to the UI
# dir>:<line> or internal/pages/<path relative to the pages dir>:<line>.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
UI_DIR="${ROOT_DIR}/web/ui"
ACTIONS_JS="${ROOT_DIR}/web/public/inline-actions.js"
ALLOWLIST="${ROOT_DIR}/scripts/ci/inline-handler-allowlist.txt"
PAGES_DIR="${ROOT_DIR}/internal/pages"

if [ "$#" -ge 1 ]; then UI_DIR="$1"; fi
if [ "$#" -ge 2 ]; then ACTIONS_JS="$2"; fi
if [ "$#" -ge 3 ]; then ALLOWLIST="$3"; fi
if [ "$#" -ge 4 ]; then PAGES_DIR="$4"; fi

if [ ! -d "$UI_DIR" ]; then
  echo "❌ inline-handler guard: ${UI_DIR} does not exist" >&2
  exit 1
fi
if [ ! -f "$ACTIONS_JS" ]; then
  echo "❌ inline-handler guard: ${ACTIONS_JS} does not exist" >&2
  exit 1
fi
if [ ! -f "$ALLOWLIST" ]; then
  echo "❌ inline-handler guard: ${ALLOWLIST} does not exist" >&2
  exit 1
fi
if [ ! -d "$PAGES_DIR" ]; then
  echo "❌ inline-handler guard: ${PAGES_DIR} does not exist" >&2
  exit 1
fi

failed=0
# Two shapes of `=` (ut-docs#3325 review): glued to the name (`onclick=`,
# any value, quoted or not), or after whitespace when a QUOTED value follows
# (`onclick ="..."`, valid HTML that runs exactly like `onclick="..."`). The
# quote is what keeps a JS assignment inside a <script> block (`btn.onclick
# = function`, `var online = navigator.onLine`) from matching: those are
# script-src, not script-src-attr, and base.html has several.
PATTERN="hx-on|(^|[^a-z0-9_-])on[a-z]+(=|[[:space:]]+=[[:space:]]*[\"'])"

# ---- 1. no inline handlers -------------------------------------------------
# Allowlist: one `web/ui/<path>:<line>` per line; blank lines and # comments
# ignored.
allowed="$(grep -vE '^[[:space:]]*(#|$)' "$ALLOWLIST" | sed -E 's/[[:space:]]+$//' || true)"

# grep exits 1 for "no match" (the clean case) but 2 for a real error,
# which must fail the guard rather than read as "nothing found".
UI_DIR="${UI_DIR%/}"
rc=0
hits="$(grep -rniE "${PATTERN}" "$UI_DIR")" || rc=$?
if [ "$rc" -gt 1 ]; then
  echo "❌ inline-handler guard: grep failed (exit ${rc}) scanning ${UI_DIR}" >&2
  exit 1
fi
while IFS= read -r hit; do
  [ -n "$hit" ] || continue
  # "<UI_DIR>/partials/x.html:12:<text>" -> key "web/ui/partials/x.html:12"
  path="${hit#"$UI_DIR"/}"
  file="${path%%:*}"
  rest="${path#*:}"
  line="${rest%%:*}"
  key="web/ui/${file}:${line}"
  if [ -n "$allowed" ] && grep -qxF -- "$key" <<<"$allowed"; then
    continue
  fi
  if [ "$failed" -eq 0 ]; then
    echo "❌ inline-handler guard: inline event handlers found in web/ui (ut-docs#3325)." >&2
    echo "   CSP script-src-attr blocks on*= attributes and hx-on needs 'unsafe-eval'." >&2
    echo "   Use web/public/inline-actions.js's data-action / data-* /" >&2
    echo "   data-fallback attributes instead (vocabulary in its header). Comments" >&2
    echo "   count too -- reword them (e.g. 'after-request handler')." >&2
  fi
  echo "   ${key}: ${rest#*:}" >&2
  failed=1
done <<<"$hits"

# ---- 1b. no inline handlers in internal/pages/*.go hand-built markup ------
# Deliberately narrower than $PATTERN above (invariant 5 in the header):
# requires a quote right after `=` (an optional `\` first, since an
# interpreted Go string's source text has `onclick=\"...\"`, backslash then
# quote, where a backtick string just has `onclick="..."`), so
# `el.onclick=function(){...}` (a script-src, not script-src-attr, concern)
# never matches, and never reuses the bare "hx-on" substring alternative, so
# a doc comment that merely names an hx-on attribute doesn't either.
# Known residual gaps, not detected (independent review, ut-docs#3506): a
# handler built via %q/string concatenation rather than a literal quote, an
# unquoted attribute value, and a false positive on an ordinary Go
# assignment whose identifier happens to start with "on" and is followed by
# `= "..."` (e.g. `cfg.OnboardingStep = "x"` -- none exist in this repo
# today; allowlist or rename if one ever does rather than loosen this).
PAGES_PATTERN="hx-on[a-zA-Z:-]*[[:space:]]*=[[:space:]]*\\\\?[\"']|(^|[^a-zA-Z0-9_-])on[a-z]+[[:space:]]*=[[:space:]]*\\\\?[\"']"
PAGES_DIR="${PAGES_DIR%/}"
rc=0
# No pipe to a second grep here (ut-docs#3506 review): under `pipefail`, a
# `| grep -v` for the _test.go carve-out would make the *trailing* grep's
# exit code win, masking a real scan error (rc=2) from the first grep as
# rc=1 and silently skipping the guard-failure branch below. `--exclude`
# does the carve-out inside the one grep instead.
page_hits="$(grep -rniE --include='*.go' --exclude='*_test.go' "${PAGES_PATTERN}" "$PAGES_DIR")" || rc=$?
if [ "$rc" -gt 1 ]; then
  echo "❌ inline-handler guard: grep failed (exit ${rc}) scanning ${PAGES_DIR}" >&2
  exit 1
fi
printed_pages_header=0
while IFS= read -r hit; do
  [ -n "$hit" ] || continue
  path="${hit#"$PAGES_DIR"/}"
  file="${path%%:*}"
  rest="${path#*:}"
  line="${rest%%:*}"
  key="internal/pages/${file}:${line}"
  if [ -n "$allowed" ] && grep -qxF -- "$key" <<<"$allowed"; then
    continue
  fi
  if [ "$printed_pages_header" -eq 0 ]; then
    echo "❌ inline-handler guard: inline event handlers found in internal/pages/*.go hand-built markup (ut-docs#3506)." >&2
    echo "   Same class invariant 1 catches in web/ui: an HTML onclick=/on*=/hx-on" >&2
    echo "   attribute, this time built by Go code (fmt.Fprintf/b.WriteString) outside" >&2
    echo "   any template. Move it to a data-action/data-* hook read by" >&2
    echo "   web/public/inline-actions.js instead." >&2
    printed_pages_header=1
  fi
  echo "   ${key}: ${rest#*:}" >&2
  failed=1
done <<<"$page_hits"

# ---- 2. every standalone document loads inline-actions.js ------------------
strip_html_comments() { perl -0777 -pe 's/<!--.*?-->//gs; s/\{\{\/\*.*?\*\/\}\}//gs' "$1"; }
checked=0
while IFS= read -r -d '' tpl; do
  stripped="$(strip_html_comments "$tpl")"
  grep -qE '^[[:space:]]*(<!DOCTYPE|<html\b)' <<<"$stripped" || continue
  checked=$((checked + 1))
  if ! grep -qE '<script[^>]*src="[^"]*\binline-actions\.js' <<<"$stripped"; then
    echo "❌ inline-handler guard: ${tpl#"${UI_DIR}/"} is a standalone document but never" >&2
    echo "   loads inline-actions.js -- every data-action/data-*/data-fallback hook" >&2
    echo "   on it would silently do nothing. Add the <script defer> tag (see" >&2
    echo "   web/ui/layouts/base.html)." >&2
    failed=1
  fi
done < <(find "$UI_DIR" -name '*.html' -print0)

if [ "$checked" -eq 0 ]; then
  echo "❌ inline-handler guard: no standalone document found under ${UI_DIR} (fail closed)." >&2
  failed=1
fi

# ---- 3. the delegation itself is still there -------------------------------
stripped_js="$(perl -0777 -pe 's{/\*.*?\*/}{}gs; s{//[^\n]*}{}g' "$ACTIONS_JS")"
for ev in click error htmx:afterRequest htmx:beforeRequest; do
  if ! grep -qE "addEventListener\(['\"]${ev}['\"]" <<<"$stripped_js"; then
    echo "❌ inline-handler guard: ${ACTIONS_JS} no longer registers its '${ev}'" >&2
    echo "   listener (outside comments) -- the templates' data-* hooks for it are dead." >&2
    failed=1
  fi
done

# ---- 4. no data-on* attribute names (html/template JS-escapes them) --------
# Go's html/template strips "data-" and then treats any name starting with
# "on" as a JS event handler (html/template/attr.go attrType), so a
# `{{ }}` inside data-on-after-request="…" -- or data-only="…", same
# prefix -- renders JSON-quoted (`parked-move-&#34;h1&#34;`), and every
# byId() lookup against it silently fails (ut-docs#3325 tester pass, two
# live instances). The hooks were renamed to data-after-request etc.; this
# keeps the whole family out, whatever the attribute is for.
rc=0
onhits="$(grep -rniE '(^|[^a-z0-9_-])data-on[a-z0-9-]*[[:space:]]*=' "$UI_DIR")" || rc=$?
if [ "$rc" -gt 1 ]; then
  echo "❌ inline-handler guard: grep failed (exit ${rc}) scanning ${UI_DIR} for data-on*" >&2
  exit 1
fi
if [ -n "$onhits" ]; then
  echo "❌ inline-handler guard: data-on* attribute name(s) in web/ui (ut-docs#3325)." >&2
  echo "   html/template strips 'data-' and JS-escapes any attribute whose name then" >&2
  echo "   starts with 'on' -- a templated id inside it renders JSON-quoted and no" >&2
  echo "   getElementById() ever matches it. Name it without the 'on' prefix" >&2
  echo "   (data-after-request, not data-on-after-request); header of" >&2
  echo "   web/public/inline-actions.js." >&2
  while IFS= read -r hit; do
    [ -n "$hit" ] || continue
    path="${hit#"$UI_DIR"/}"
    echo "   web/ui/${path}" >&2
  done <<<"$onhits"
  failed=1
fi

if [ "$failed" -ne 0 ]; then
  exit 1
fi

echo "✓ inline-handler guard: no inline handlers in web/ui or internal/pages/*.go, ${checked} standalone document(s) load inline-actions.js, delegation intact"
