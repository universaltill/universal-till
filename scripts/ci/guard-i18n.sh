#!/usr/bin/env bash
#
# Enforces multilingual UI (docs reference/coding-standards.md):
#   1. Every {{ T "key" }} used in a template must exist in web/locales/en.json.
#   2. Every locale file must define exactly the same key set as en.json
#      (the base locale) — no missing translations, no orphan keys.
#   3. No Go-side literal replaces a template's own job: a raw English string
#      written straight to an HTTP response (w.Write/fmt.Fprintf(w, ...)) is
#      invisible to checks 1-2 above, which only ever look at web/ui/*.html
#      (found ut-docs#19 — /api/buttons/search's "Type 3+ characters" hint,
#      plus four plugin_api.go status strings, none going through httpx.T).
#   4. No hand-written hx-vals JSON literal replaces {{ jsonVals "k" .V }} —
#      breaks (invalid JSON, or a value escaping the attribute) for any
#      quoted/apostrophe-containing value (ut-docs#19).
#   5. No hardcoded prose literal assigned to .textContent/.innerHTML inside
#      an inline <script> block in web/ui/**/*.html — invisible to checks
#      1-2 (template-only) and check 3 (Go-response-only) alike (ut-docs#205).
#      Known gap, not yet covered: shipped JS under web/public/ (outside this
#      check's web/ui/ glob) can carry the same class of hardcoded string —
#      see ut-docs#205's own follow-up card before assuming this check is
#      exhaustive.
#   6. No hardcoded prose literal assigned straight to pos.Basket.ToastMessage
#      (the sale screen's single notification field, ut-docs#213) — invisible
#      to check 3, which only scans literals passed directly to
#      w.Write/fmt.Fprint*, not a struct-field assignment (ut-docs#237: five
#      real cases shipped in pos_api.go until #213 localized them; nothing
#      stopped a sixth from shipping the same way).
# Fails CI so a new page/string can't ship untranslated.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

BASE="web/locales/en.json"
[ -f "$BASE" ] || { echo "guard-i18n: $BASE missing" >&2; exit 1; }

python3 - <<'PY'
import glob, json, re, sys

fail = False

def keys(path):
    return set(json.load(open(path)).keys())

base = keys("web/locales/en.json")

# 1. template T "key" usage must resolve in the base locale.
used = set()
for f in glob.glob("web/ui/**/*.html", recursive=True):
    used |= set(re.findall(r'\{\{-?\s*T\s+"([^"]+)"', open(f).read()))
# Go-side menu labels (nav.*) live in the base menu; assume any nav.* is used.
missing = sorted(k for k in used if k not in base)
if missing:
    fail = True
    print("guard-i18n: template keys missing from en.json:")
    for k in missing:
        print("  -", k)

# 2. every locale file must match the base key set exactly.
for path in sorted(glob.glob("web/locales/*.json")):
    if path.endswith("en.json"):
        continue
    ks = keys(path)
    only_base = sorted(base - ks)
    only_loc = sorted(ks - base)
    if only_base or only_loc:
        fail = True
        print(f"guard-i18n: {path} differs from en.json:")
        for k in only_base:
            print("  missing:", k)
        for k in only_loc:
            print("  orphan :", k)


# 3. Go-side hardcoded user-facing strings (ut-docs#19): a string literal
#    passed directly as the body of an HTTP response write is invisible to
#    checks 1-2, which only scan web/ui/*.html. Heuristic, deliberately
#    narrow to keep false positives near zero: only literal double-quoted
#    strings (not backtick raw strings, not a variable built up elsewhere)
#    passed directly as the argument to w.Write([]byte("...")),
#    fmt.Fprintf/Fprint/Fprintln(w, "...") on the SAME line -- calls this
#    codebase uses specifically to write an HTML/HTMX-fragment response body
#    (not http.Error, not a JSON "message" field -- both already established
#    elsewhere in this codebase as not going through T(), a separate,
#    knowingly-deferred class, not this guard's scope) -- and only literals
#    that look like actual prose (two adjacent alphabetic words), so a
#    content-type string, a single technical token, or a single-word status
#    like "Saved" never flags (a real gap, not a false-positive risk: the
#    heuristic trades recall for near-zero false positives on a first pass,
#    same tradeoff every regex-based guard in this repo makes). Genuinely
#    catches only the shape of bug this card found, not every way a string
#    could evade a line-based regex -- a real audit still needs a human
#    occasionally, this just keeps the exact class found here from
#    regressing silently. A reviewed exception can be marked `// i18n:ignore`
#    on the same line.
call_re = re.compile(
    r'(?:w\.Write\(\[\]byte\(|fmt\.Fprintf\(\s*w,\s*|fmt\.Fprint(?:ln)?\(\s*w,\s*)"((?:[^"\\]|\\.)*)"'
)
prose_re = re.compile(r'[A-Za-z]{2,}\s+[A-Za-z]{2,}')

go_hits = []
for f in sorted(glob.glob("internal/**/*.go", recursive=True)):
    if f.endswith("_test.go"):
        continue
    for i, line in enumerate(open(f, encoding="utf-8").read().splitlines(), 1):
        if "i18n:ignore" in line:
            continue
        for m in call_re.finditer(line):
            literal = m.group(1).replace('\\"', '"')
            if prose_re.search(literal):
                go_hits.append((f, i, literal))

if go_hits:
    fail = True
    print("guard-i18n: hardcoded user-facing strings in Go HTTP responses (route through httpx.T):")
    for f, i, literal in go_hits:
        print(f"  {f}:{i}: {literal!r}")

# 4. Hand-written hx-vals JSON literals (ut-docs#19): the exact shape of bug
#    this card fixed in 6 templates -- hx-vals='{"k":"{{ .V }}"}' instead of
#    hx-vals='{{ jsonVals "k" .V }}' -- breaks (invalid JSON, or a value
#    escaping the attribute) for any quoted/apostrophe-containing value.
#    A plain grep can't tell a genuinely-safe hardcoded-constant hx-vals
#    (e.g. basket.html's {"order_type":"takeaway"}, no template action
#    inside) from an unsafe one: flag only an hx-vals value that starts with
#    a literal JSON-object skeleton ({" ...) -- as opposed to starting with
#    the {{ jsonVals ... }} template action itself, the already-fixed
#    pattern -- AND still contains a {{ ... }} action somewhere inside it
#    (a raw field interpolated into that hardcoded skeleton).
hxvals_re = re.compile(r"hx-vals='([^']*)'")
hxvals_hits = []
for f in sorted(glob.glob("web/ui/**/*.html", recursive=True)):
    for i, line in enumerate(open(f, encoding="utf-8").read().splitlines(), 1):
        if "i18n:ignore" in line:
            continue
        for m in hxvals_re.finditer(line):
            val = m.group(1)
            if val.startswith('{"') and "{{" in val:
                hxvals_hits.append((f, i, line.strip()))

if hxvals_hits:
    fail = True
    print("guard-i18n: hand-written hx-vals JSON literal (use {{ jsonVals \"k\" .V }} instead):")
    for f, i, line in hxvals_hits:
        print(f"  {f}:{i}: {line}")

# 5. Hardcoded inline-JS status text (ut-docs#205): a <script> block inside a
#    web/ui page/partial can set .textContent/.innerHTML to a raw English
#    literal -- invisible to checks 1-2 (template-only) and check 3
#    (Go-response-only). Same prose heuristic as check 3 (near-zero false
#    positives over exhaustive recall), applied to the assigned literal
#    after stripping any HTML tags out of it first: without stripping, a
#    literal that's actually a markup skeleton being built up in JS (e.g.
#    innerHTML = '<ul style="list-style:none; ...">') false-positives on
#    attribute-name pairs like "ul style" that aren't prose at all; with
#    stripping, that false positive disappears while a literal that's a
#    real user-facing string wrapped in a tag (e.g.
#    '<p class="muted">No matches.</p>') still correctly flags on "No
#    matches." A reviewed exception (existing debt not in a given card's
#    migration scope) is the same same-line `i18n:ignore` used above.
jsassign_re = re.compile(r'''\.(?:textContent|innerHTML)\s*=\s*(['"])((?:(?!\1)[^\\]|\\.)*)\1''')
tag_re = re.compile(r'<[^>]*>')
jsassign_hits = []
for f in sorted(glob.glob("web/ui/**/*.html", recursive=True)):
    for i, line in enumerate(open(f, encoding="utf-8").read().splitlines(), 1):
        if "i18n:ignore" in line:
            continue
        for m in jsassign_re.finditer(line):
            literal = m.group(2)
            stripped = tag_re.sub(' ', literal)
            if prose_re.search(stripped):
                jsassign_hits.append((f, i, literal))

if jsassign_hits:
    fail = True
    print("guard-i18n: hardcoded user-facing string in inline <script> (route through a template-populated JS lookup, e.g. bugreport_panel.html's `var T = {...}` pattern):")
    for f, i, literal in jsassign_hits:
        print(f"  {f}:{i}: {literal!r}")

# 6. Go-side ToastMessage literal assignments (ut-docs#237): a raw English
#    string assigned straight to pos.Basket.ToastMessage (the sale screen's
#    single notification field, ut-docs#213) is invisible to check 3 above,
#    which only scans literals passed directly to w.Write/fmt.Fprint* — a
#    struct-field assignment is a different shape entirely, and Go has two
#    idiomatic syntaxes for it: `b.ToastMessage = "..."` and a composite
#    literal's `ToastMessage: "..."` key. Same narrow, low-false-positive
#    heuristic as check 3: only a literal double-quoted string assigned
#    directly (either syntax) or passed as a literal Sprintf format string
#    on the same line (ToastMessage: fmt.Sprintf("...", ...)) can match —
#    an httpx.T(...) call (e.g. pos_api.go:846) or a plain identifier (the
#    msg/toast/message variables this codebase already threads through from
#    a caller that itself used httpx.T, e.g. pos_api.go:657, hold_api.go:136,
#    self_order_shop.go:462, pos_modifiers_api.go:122) never matches, so
#    those don't need re-auditing here. Only prose literals (same
#    two-adjacent-word heuristic as check 3) flag. A reviewed exception can
#    be marked `// i18n:ignore` on the same line.
toast_re = re.compile(
    r'\bToastMessage\s*[:=]\s*(?:fmt\.Sprintf\(\s*)?"((?:[^"\\]|\\.)*)"'
)
toast_hits = []
for f in sorted(glob.glob("internal/**/*.go", recursive=True)):
    if f.endswith("_test.go"):
        continue
    for i, line in enumerate(open(f, encoding="utf-8").read().splitlines(), 1):
        if "i18n:ignore" in line:
            continue
        for m in toast_re.finditer(line):
            literal = m.group(1).replace('\\"', '"')
            if prose_re.search(literal):
                toast_hits.append((f, i, literal))

if toast_hits:
    fail = True
    print("guard-i18n: hardcoded literal assigned to ToastMessage (route through httpx.T):")
    for f, i, literal in toast_hits:
        print(f"  {f}:{i}: {literal!r}")

if fail:
    sys.exit(1)
print(f"✓ i18n guard: {len(used)} template keys resolve; all locales match en.json; "
      f"no hardcoded Go-side response strings found; no hand-written hx-vals literals found; "
      f"no hardcoded inline-JS status strings found; no hardcoded ToastMessage literals found")
PY
