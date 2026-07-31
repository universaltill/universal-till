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
#    narrow to keep false positives near zero: only w.Write([]byte("...")),
#    fmt.Fprintf(w, "..."), fmt.Fprint(w, "...") -- calls this codebase uses
#    specifically to write an HTML/HTMX-fragment response body (not
#    http.Error, not a JSON "message" field -- both already established
#    elsewhere in this codebase as not going through T(), a separate,
#    knowingly-deferred class, not this guard's scope) -- and only literals
#    that look like actual prose (two adjacent alphabetic words), so a
#    content-type string or a single technical token never flags. A
#    genuine, reviewed exception can be marked `// i18n:ignore` on the same
#    line.
call_re = re.compile(
    r'(?:w\.Write\(\[\]byte\(|fmt\.Fprintf\(\s*w,\s*|fmt\.Fprint\(\s*w,\s*)"((?:[^"\\]|\\.)*)"'
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

if fail:
    sys.exit(1)
print(f"✓ i18n guard: {len(used)} template keys resolve; all locales match en.json; no hardcoded Go-side response strings")
PY
