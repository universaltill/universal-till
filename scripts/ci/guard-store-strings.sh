#!/usr/bin/env bash
#
# Store-build string guard (ADR-0194 §2.3, ut-docs#6401, #6462). A Google Play
# or App Store build may not steer a user to buy or renew a plan outside the
# store: no checkout/plans URL, no plan price, no buy/subscribe/upgrade/renew
# wording about a plan. The till hides those surfaces by channel (hide AND
# refuse, internal/pages); this guard keeps the TEXT clean:
#
#   - web/help/en/*.md     with <!-- plan-purchase --> ... <!-- /plan-purchase -->
#                          blocks removed (those blocks are not shown on store builds)
#   - web/release-notes/en/*.md   same blocks removed
#   - web/locales/en.json  every value, except the keys listed in
#                          scripts/ci/store-strings-exempt.txt (surfaces that
#                          do not exist on store builds)
#   - web/ui/**/*.html and web/public/*.js   hard-coded text, minus comments and
#                          minus {{ if planpurchaseallowed|accountlinkoutallowed|
#                          inappupdateallowed }} bodies (not rendered on store builds)
#   - web/help/{ar,de,fa,tr}, web/release-notes/{ar,de,fa,tr}, web/locales/{ar,de,fa,tr}.json
#                          the language-independent patterns (addresses, prices) plus
#                          the block markers; a translation may not have fewer
#                          plan-purchase blocks than the English topic it translates.
#
# Block markers are parsed EXACTLY like internal/manual and internal/releasenotes:
# the marker alone on its own line (trimmed); a line that starts with "<!--" and
# mentions plan-purchase but is not exactly a marker, a nested/unmatched/unclosed
# marker is an error. A marker at the end of a text line is neither a marker nor
# an error there, so the text stays scanned here too.
# The exempt list is shrink-only: a key that no longer exists, a key that no
# longer needs the exemption, or more than MAX_EXEMPT entries fails.
#
# Plan STATUS wording ("paid until", "subscription ended") is allowed on every
# channel; wording that tells the reader to buy, subscribe, upgrade or renew is
# not. When in-app purchases exist (#6512) this guard must allow strings that
# name Apple or Google subscriptions and still block web checkout wording.
#
# Reviewed exception, same line: store-strings:allow <reason>  (help/notes only).
# Usage: guard-store-strings.sh [repo-root]
set -euo pipefail

ROOT="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
cd "${ROOT}"

exec python3 -I - "${ROOT}" <<'PY'
import glob, json, os, re, sys

root = sys.argv[1]
os.chdir(root)

# Shrink-only ceiling for scripts/ci/store-strings-exempt.txt. Lowering it is
# the only edit this constant ever gets.
MAX_EXEMPT = 11

VERBS = r"(?:buy|buying|purchase|purchasing|subscribe|subscribing|upgrade|upgrading|renew|renewing|pay for|choose a plan|change plan|checkout)"
NOUNS = r"(?:plan|plans|subscription|licen[cs]e|cloud|tier)"
STEER_DOMAIN = r"(?:website|web site|webpage|our site|universaltill\.com|account page)"
PLAN_PATH = r"(?:plans?|checkout|pricing|billing|subscribe|subscription|upgrade|buy|stores/\S*/plan\S*)"
CUR = r"(?:£|€|GBP|EUR|USD|pounds?|euros?)"

# Language-independent: addresses and prices (also applied to translations).
URL_PATTERNS = [
    ("checkout/plans URL", re.compile(r"https?://\S*(?:plans|checkout|pricing|billing|subscribe|/buy)|(?<![\w-])/(?:plans|checkout|pricing)\b", re.I)),
    # schemeless: universaltill.com/pricing, my.universaltill.com/billing, any host/plans
    ("checkout/plans address without scheme", re.compile(r"\b[a-z0-9-]+(?:\.[a-z0-9-]+)*\.(?:com|net|org|io|uk|eu|app)/\S*?\b" + PLAN_PATH + r"\b", re.I)),
]
AMOUNT = r"(?:[£€]\s?\d[\d.,]*|\b\d[\d.,]*\s?(?:£|€|GBP|EUR|USD)(?![A-Za-z])|\b(?:GBP|EUR|USD)\s?\d[\d.,]*|\b\d[\d.,]*\s(?:pounds?|euros?)\b|\$\s?\d[\d.,]*)"
PLAN_CTX = (r"\b(?:plans?|subscriptions?|monthly|annual|yearly|per\s+(?:month|year|till|shop|store)|a\s+(?:month|year)|/\s?(?:mo|month|yr|year)|tiers?|licen[cs]es?|premium|paid|billing|cloud"
    r"|monat(?:lich)?|jahr|tarif(?:e)?|abo|abonnement|ay|ayl[ıi]k|y[ıi]l|y[ıi]ll[ıi]k|abonelik|شهر|شهري|سنة|سنوي|خطة|اشتراك|ماه|ماهانه|سال|سالانه|طرح|اشتراک)")
PRICE_PATTERNS = [
    # an amount in any phrasing next to plan/period words, in either order
    # ("Paid plans start at £9.99", "Just £9.99 monthly", "9.99 GBP per month").
    # A bare £2 in an item example is not a plan price and stays allowed.
    ("price", re.compile(AMOUNT + r"[^.\n]{0,60}" + PLAN_CTX + r"|" + PLAN_CTX + r"[^.\n]{0,60}" + AMOUNT, re.I)),
]
# English wording.
WORDING_PATTERNS = [
    ("wording", re.compile(r"\b" + VERBS + r"\b[^.\n]{0,50}\b" + NOUNS + r"\b|\b" + NOUNS + r"\b[^.\n]{0,30}\b" + VERBS + r"\b", re.I)),
    ("plan call to action", re.compile(r"\b(?:choose a plan|change plan|renew or change|renew current)\b", re.I)),
    ("verb-only call to action", re.compile(r"\b(?:upgrade (?:now|today|to|your)|buy (?:now|it|more|a|the)|subscribe (?:now|today|to)|renew (?:it|this|now|your|in)|pay online|sign up for (?:a|the|pro|cloud))\b", re.I)),
    ("paid plan steering", re.compile(r"\b(?:paid|premium)\s+(?:plans?|features?|tiers?|subscriptions?|cloud)\b[^.\n]{0,60}\b" + STEER_DOMAIN + r"\b", re.I)),
    ("steering to the website", re.compile(r"\b(?:get|unlock|add|start|buy|order|pay|renew|upgrade|subscribe)\b[^.\n]{0,40}\b(?:on|at|from|via|through|in) (?:our|the|your) (?:website|web site|account)\b", re.I)),
    ("phone hand-off to pay", re.compile(r"\b(?:scan|phone|qr)\b[^.\n]{0,40}\b(?:renew|buy|upgrade|subscribe)\b", re.I)),
]
PATTERNS = URL_PATTERNS + PRICE_PATTERNS + WORDING_PATTERNS
LANG_FREE = URL_PATTERNS + PRICE_PATTERNS
OTHER_LANGS = ["ar", "de", "fa", "tr"]

OPEN, CLOSE = "<!-- plan-purchase -->", "<!-- /plan-purchase -->"
fails = 0

def err(path, line, msg):
    global fails
    fails += 1
    loc = f",line={line}" if line else ""
    print(f"::error file={path}{loc}::{msg}")

def split_blocks(text, path):
    """Same state machine as splitPlanPurchase in internal/manual and
    internal/releasenotes. Returns (visible_text_with_hidden_lines_blanked,
    block_count). Errors are reported, never swallowed."""
    out, inside, blocks = [], False, 0
    for i, line in enumerate(text.split("\n"), 1):
        t = line.strip()
        if t == OPEN:
            if inside:
                err(path, i, f"nested {OPEN} (the app refuses this file)")
            inside = True
            blocks += 1
            out.append("")
        elif t == CLOSE:
            if not inside:
                err(path, i, f"{CLOSE} without an opener (the app refuses this file)")
            inside = False
            out.append("")
        else:
            if t.startswith("<!--") and "plan-purchase" in t:
                err(path, i, f"malformed plan-purchase marker {t!r}: want {OPEN} or {CLOSE} alone on its line")
            out.append("" if inside else line)
    if inside:
        err(path, 0, f"{OPEN} is never closed (the app refuses this file)")
    return "\n".join(out), blocks

def scan(label, text, path, patterns=PATTERNS):
    for lineno, line in enumerate(text.split("\n"), 1):
        if "store-strings:allow" in line:
            continue
        for name, rx in patterns:
            m = rx.search(line)
            if m:
                err(path, lineno, f"{label}: {name} on a store build: \"{m.group(0)[:80]}\" (ADR-0194 §2.3). Mark a purchase-only block with {OPEN}, or reword.")

# --- templates: drop comments and the bodies of channel-gated ifs -----------
GATE = re.compile(r"^(?:and\s+)?(planpurchaseallowed|accountlinkoutallowed|inappupdateallowed)\b")
ACTION = re.compile(r"\{\{-?\s*(.*?)\s*-?\}\}", re.S)

def template_visible(src):
    """Text a store build renders from a template: template comments,
    HTML comments and actions removed; bodies of `if planpurchaseallowed`
    (etc.) removed, `else` bodies and `if not <gate>` bodies kept."""
    src = re.sub(r"\{\{-?\s*/\*.*?\*/\s*-?\}\}", lambda m: "\n" * m.group(0).count("\n"), src, flags=re.S)
    src = re.sub(r"<!--.*?-->", lambda m: "\n" * m.group(0).count("\n"), src, flags=re.S)
    # keep only the text-bearing attributes of a tag (class names and test ids are not text)
    def tag_text(m):
        keep = re.findall(r"\b(?:title|aria-label|alt|placeholder)=\"([^\"]*)\"", m.group(0))
        return " ".join(keep) + "\n" * m.group(0).count("\n")
    src = re.sub(r"<[a-zA-Z/][^<>]*>", tag_text, src)
    out, stack, pos, hidden = [], [], 0, 0   # stack entries: bool "this frame hides its body"
    def emit(chunk):
        out.append("\n" * chunk.count("\n") if hidden else chunk)
    for m in ACTION.finditer(src):
        emit(src[pos:m.start()])
        pos = m.end()
        a = m.group(1)
        word = a.split(None, 1)[0] if a.split() else ""
        if word in ("if", "range", "with", "define", "block"):
            hides = False
            if word == "if":
                g = GATE.match(a[2:].strip())
                hides = bool(g)
            stack.append(hides)
            hidden += 1 if hides else 0
            # `if not gate`: the body is visible, the else body hidden (handled at else)
            if word == "if" and re.match(r"^not\s+(planpurchaseallowed|accountlinkoutallowed|inappupdateallowed)\b", a[2:].strip()):
                stack[-1] = ("not", False)
        elif word == "else":
            if stack:
                top = stack[-1]
                if top is True:           # body hidden -> else visible
                    stack[-1] = ("else", False); hidden -= 1
                elif isinstance(top, tuple) and top[0] == "not":   # body visible -> else hidden
                    stack[-1] = ("notelse", True); hidden += 1
        elif word == "end":
            if stack:
                top = stack.pop()
                if top is True or top == ("notelse", True):
                    hidden -= 1
        out.append("\n" * m.group(0).count("\n"))
    emit(src[pos:])
    return "".join(out)

def js_visible(src):
    src = re.sub(r"/\*.*?\*/", lambda m: "\n" * m.group(0).count("\n"), src, flags=re.S)
    return "\n".join(re.sub(r"(?<![:\"'\\])//.*$", "", l) for l in src.split("\n"))

scanned = 0
en_blocks = {}
for kind in ("help", "release-notes"):
    for f in sorted(glob.glob(f"web/{kind}/en/*.md")):
        scanned += 1
        vis, n = split_blocks(open(f, encoding="utf-8").read(), f)
        en_blocks[(kind, os.path.basename(f))] = n
        scan(kind, vis, f)
    for lang in OTHER_LANGS:
        for f in sorted(glob.glob(f"web/{kind}/{lang}/*.md")):
            scanned += 1
            vis, n = split_blocks(open(f, encoding="utf-8").read(), f)
            scan(f"{kind} ({lang})", vis, f, LANG_FREE)
            want = en_blocks.get((kind, os.path.basename(f)), 0)
            if n < want:
                err(f, 0, f"{lang} translation has {n} plan-purchase block(s), the English source has {want}: add the markers (marker-only edit, no translation change)")

exempt = []
if os.path.exists("scripts/ci/store-strings-exempt.txt"):
    for line in open("scripts/ci/store-strings-exempt.txt", encoding="utf-8"):
        line = line.split("#", 1)[0].strip()
        if line:
            exempt.append(line)
exempt_set = set(exempt)
if len(exempt) > MAX_EXEMPT:
    err("scripts/ci/store-strings-exempt.txt", 0, f"{len(exempt)} exempt keys, ceiling is {MAX_EXEMPT}: the list is shrink-only (reword the string or hide the surface instead)")

loc = json.load(open("web/locales/en.json", encoding="utf-8"))
for key, val in loc.items():
    if key in exempt_set or not isinstance(val, str):
        continue
    scanned += 1
    for name, rx in PATTERNS:
        m = rx.search(val)
        if m:
            err("web/locales/en.json", 0, f"locale key {key}: {name} on a store build: \"{m.group(0)[:80]}\". Reword, or (only if the string never renders on a store build) list the key in scripts/ci/store-strings-exempt.txt.")
for lang in OTHER_LANGS:
    p = f"web/locales/{lang}.json"
    if not os.path.exists(p):
        continue
    for key, val in json.load(open(p, encoding="utf-8")).items():
        if key in exempt_set or not isinstance(val, str):
            continue
        scanned += 1
        for name, rx in LANG_FREE:
            m = rx.search(val)
            if m:
                err(p, 0, f"locale key {key}: {name} on a store build: \"{m.group(0)[:80]}\"")

for key in sorted(exempt_set):
    if key not in loc:
        err("scripts/ci/store-strings-exempt.txt", 0, f"exempt key {key} no longer exists in en.json: remove the line")
    elif isinstance(loc[key], str) and not any(rx.search(loc[key]) for _, rx in PATTERNS):
        err("scripts/ci/store-strings-exempt.txt", 0, f"exempt key {key} no longer needs the exemption (its text is clean): remove the line")

for f in sorted(glob.glob("web/ui/**/*.html", recursive=True)):
    scanned += 1
    scan("template", template_visible(open(f, encoding="utf-8").read()), f)
for f in sorted(glob.glob("web/public/*.js")):
    scanned += 1
    scan("script", js_visible(open(f, encoding="utf-8").read()), f)

if not glob.glob("web/help/en/*.md") or not loc:
    print("::error::guard-store-strings: found no help topics or no en.json strings - the scan is broken")
    sys.exit(1)
if fails:
    sys.exit(1)
print(f"guard-store-strings: ok ({scanned} sources)")
PY
