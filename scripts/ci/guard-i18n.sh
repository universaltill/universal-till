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
#   7. Every string-literal key argument passed to httpx.RenderError,
#      common.LocalizedError, common.LogAndLocalizedError or httpx.T itself
#      must resolve in en.json (ut-docs#1461) — checks 1-2 only validate
#      template {{ T "key" }} usage, so a typo'd key built in Go and handed
#      to one of these call shapes was invisible to any check and silently
#      fell back to rendering the raw key text at runtime.
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
#
#    On the http.Error exemption specifically -- why it's still open, not
#    narrowed (ut-docs#316, continued by ut-docs#893): the two most-repeated
#    http.Error literals ("manager or admin required"/"invalid upload" and
#    their variants, ~40 sites), 22 of catalog/handlers.go's 26
#    err.Error()-to-the-operator sites, and (ut-docs#893's first batch) all
#    26 remaining http.Error sites in audit_page.go, basket_page.go,
#    external_api.go, invoice_page.go (25 http.Error sites plus one
#    fmt.Fprintf-rendered err.Error() leak at /api/invoices/issue's 409
#    branch), journal_page.go, order_status.go, pending_pairings.go,
#    receipt_designer.go, sync_admin.go and sync_assets.go/sync_sales.go
#    now go through common.LocalizedError/LogAndLocalizedError (translated,
#    never raw Go/SQL text or an internal ID) -- the remaining 4 sites in
#    catalog/handlers.go are deliberately untouched, clean hand-written
#    validation errors, not this defect class. But ~294 more sites are NOT
#    yet swept, in two classes (2026-08-23 review of #893's first batch
#    found the original estimate here under-counted the second class by
#    ~101 sites -- corrected):
#    - ~63 more raw err.Error()-to-the-operator leaks, in backup_api.go,
#      buttons_api.go, eod_api.go, import_page.go, issue_report_page.go,
#      pairing_api.go, plugin_api.go, plugin_settings_page.go, pos_api.go,
#      refund_page.go, settings_page.go, sync_api.go and tax_codes_page.go
#      (same 13 files as before -- this class is fully scoped to them).
#    - ~231 one-off hardcoded-literal http.Error sites: ~130 more spread
#      across those same 13 files, plus ~101 in ~23 further files this
#      sweep hasn't touched at all (users_page.go, self_order_shop.go,
#      permission_settings_page.go, setup_page.go, auth_page.go,
#      registers_page.go, kitchen_stations_page.go, update_api.go,
#      translations_page.go, tables_page.go, pos_modifiers_api.go,
#      locations_page.go, plugin_page.go, order_tracking.go, help_page.go,
#      ask_api.go, promotions_page.go, print_api.go, plugins_store_page.go,
#      plugins_page.go, my_reports_page.go, discovery_api.go,
#      country_settings_page.go).
#    Narrowing this exemption today would fail CI on all of them at once,
#    well past what one card's diff should carry. The remaining sweep
#    stays tracked as ut-docs#893 rather than left implicit.
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
#    ut-docs#918 review finding 3: renderNotice(el, level, text) (app.js,
#    ut-docs#213/#238/#918) is a second way a literal can reach the same
#    .pos-notice surface without ever touching .textContent/.innerHTML
#    directly -- the jsassign_re above is blind to it, which would have let
#    the two pre-existing i18n:ignore literals on settings.html:666/668
#    silently stop being enforced the moment #918 moved them behind
#    renderNotice(). Same prose heuristic, applied to renderNotice's 3rd
#    (text) argument.
jsassign_re = re.compile(r'''\.(?:textContent|innerHTML)\s*=\s*(['"])((?:(?!\1)[^\\]|\\.)*)\1''')
rendernotice_re = re.compile(r'''renderNotice\([^,]+,\s*['"][a-z]+['"]\s*,\s*(['"])((?:(?!\1)[^\\]|\\.)*)\1''')
tag_re = re.compile(r'<[^>]*>')
jsassign_hits = []
for f in sorted(glob.glob("web/ui/**/*.html", recursive=True)):
    for i, line in enumerate(open(f, encoding="utf-8").read().splitlines(), 1):
        if "i18n:ignore" in line:
            continue
        for m in list(jsassign_re.finditer(line)) + list(rendernotice_re.finditer(line)):
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

# 7. Go-side i18n key literals passed to httpx.RenderError,
#    common.LocalizedError, common.LogAndLocalizedError, and httpx.T must
#    resolve in en.json (ut-docs#1461, follow-up from #1455's review).
#    Checks 1-2 above only validate template {{ T "key" }} usage; a
#    msgKey/key argument built in Go and passed as a string literal to one
#    of these four call shapes was invisible to any check, so a typo'd key
#    silently fell back to rendering the raw key text at runtime -- most
#    visibly on RenderError's full-page kiosk error screen, where the
#    typo'd key becomes the page's entire heading and body. Only a literal
#    double-quoted string argument can be verified statically; a key built
#    from a variable/expression (e.g. a classifyTenderError(err) helper, or
#    a plugin's entry.Label) can't be, and is silently skipped rather than
#    reported -- this narrows to what a static pass can actually prove,
#    same tradeoff as every other check in this script.
#
#    Argument parsing is paren/string-aware, not a plain comma split: a
#    real call site here passes a nested call as the locale argument (e.g.
#    httpx.T(httpx.ResolveLocale(w, r), "some.key")), and naively treating
#    the first comma as the argument boundary misreads that inner comma.
#    Every real call site found here is single-line; a call whose argument
#    list wraps onto a second line can't be verified by this pass (known
#    gap, same class as check 5's web/public/ note above) and is skipped,
#    not reported as missing.
def split_args(s):
    args, depth, cur, i, in_str = [], 0, [], 0, False
    while i < len(s):
        c = s[i]
        if in_str:
            cur.append(c)
            if c == '\\' and i + 1 < len(s):
                cur.append(s[i + 1])
                i += 2
                continue
            if c == '"':
                in_str = False
        elif c == '"':
            in_str = True
            cur.append(c)
        elif c in '([{':
            depth += 1
            cur.append(c)
        elif c in ')]}':
            depth -= 1
            cur.append(c)
        elif c == ',' and depth == 0:
            args.append(''.join(cur))
            cur = []
        else:
            cur.append(c)
        i += 1
    if cur:
        args.append(''.join(cur))
    return [a.strip() for a in args]

def literal_key(arg):
    m = re.fullmatch(r'"((?:[^"\\]|\\.)*)"', arg)
    return m.group(1).replace('\\"', '"') if m else None

# (prefix, index of the key argument, 0-based -- None means "work it out
# from the argument count", see bare T( below)
CALL_SPECS = [
    ("httpx.RenderError(", 3),           # w, r, status, msgKey, err
    ("common.LocalizedError(", 3),       # w, r, status, key
    ("common.LogAndLocalizedError(", 3), # w, r, status, key, logTag, err
    ("httpx.T(", 1),                     # locale, key
    ("T(", None),
]
# Bare T( (no "httpx."/other package qualifier) has two real shapes in this
# codebase, and both matter: internal/httpx's own definition calls itself as
# T(locale, key) (2 args); several handlers (import_page.go,
# catalog/handlers.go, invoice_page.go) bind a *locale* up front and build a
# single-argument closure -- `T := funcs["T"].(func(string) string)` or
# `T := func(k string) string { return httpx.T(locale, k) }` -- then call it
# as T(key) (1 arg) dozens of times each. An earlier version of this check
# only looked for the 2-arg shape, and only inside internal/httpx, on the
# theory that "T(" elsewhere would be a different identifier -- independent
# review (ut-docs#1461) found that theory false: it left ~80 real call
# sites across those three files completely unchecked. There is no OTHER
# meaning of a bare, unqualified "T(" call anywhere in this codebase (this
# was confirmed by inspection, not assumed), so this scans every file, and
# picks the key argument by whichever of the two known shapes matches the
# actual argument count -- 1 arg means it's the closure form (key is
# args[0]), 2 means it's httpx's own T(locale, key) (key is args[1]); any
# other count is a shape this check doesn't recognise and is skipped rather
# than guessed at.
BARE_T_RE = re.compile(r'(?<!\.)\bT\(')

key_call_hits = []
for f in sorted(glob.glob("internal/**/*.go", recursive=True)):
    if f.endswith("_test.go"):
        continue
    lines = open(f, encoding="utf-8").read().splitlines()
    for lineno, line in enumerate(lines, 1):
        if "i18n:ignore" in line:
            continue
        for prefix, key_idx in CALL_SPECS:
            start = 0
            while True:
                pos = line.find(prefix, start)
                if pos == -1:
                    break
                start = pos + len(prefix)
                if prefix == "T(" and BARE_T_RE.match(line, pos) is None:
                    continue
                depth, j, in_str = 1, pos + len(prefix), False
                while j < len(line) and depth > 0:
                    c = line[j]
                    if in_str:
                        if c == '\\':
                            j += 1
                        elif c == '"':
                            in_str = False
                    elif c == '"':
                        in_str = True
                    elif c == '(':
                        depth += 1
                    elif c == ')':
                        depth -= 1
                    j += 1
                if depth != 0:
                    continue  # call wraps past this line -- can't verify here
                args = split_args(line[pos + len(prefix):j - 1])
                idx = key_idx
                if idx is None:
                    idx = {1: 0, 2: 1}.get(len(args))
                    if idx is None:
                        continue  # unrecognised bare-T( shape -- can't verify
                if len(args) <= idx:
                    continue
                key = literal_key(args[idx])
                if key is not None and key not in base:
                    key_call_hits.append((f, lineno, prefix.rstrip("("), key))

if key_call_hits:
    fail = True
    print("guard-i18n: Go-side i18n key literal missing from en.json:")
    for f, i, call, k in key_call_hits:
        print(f"  {f}:{i}: {call}(...): {k!r}")

if fail:
    sys.exit(1)
print(f"✓ i18n guard: {len(used)} template keys resolve; all locales match en.json; "
      f"no hardcoded Go-side response strings found; no hand-written hx-vals literals found; "
      f"no hardcoded inline-JS status strings found; no hardcoded ToastMessage literals found; "
      f"no missing Go-side i18n key literals found")
PY
