#!/usr/bin/env bash
#
# ut-docs#414: the Android wrapper's own native chrome (status text,
# notification text, the launcher label) was English-only while the web UI
# inside its WebView is fully translated (en/fa/tr/ar, enforced for the web
# side by guard-i18n.sh) -- a Farsi shop saw a Farsi till inside an English
# frame. This mirrors guard-i18n.sh's key-parity check for
# android/app/src/main/res/values*/strings.xml instead of web/locales/*.json:
#   1. Every locale directory (values-fa, values-tr, values-ar, ...) defines
#      exactly the same key set as the base values/strings.xml -- no missing
#      translation, no orphan key.
#   2. Every %N$s-style placeholder in a translated string matches the base
#      string's placeholders exactly (same positions, same count) -- a
#      translation that drops or reorders a placeholder crashes
#      String.format at runtime instead of failing loudly at CI time.
# Fails CI so a new key (or a new locale directory) can't ship
# out of parity, same guarantee guard-i18n.sh gives the web side.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

BASE="android/app/src/main/res/values/strings.xml"
[ -f "$BASE" ] || { echo "guard-android-i18n: $BASE missing" >&2; exit 1; }

python3 - <<'PY'
import glob
import re
import sys
import xml.etree.ElementTree as ET

fail = False
BASE = "android/app/src/main/res/values/strings.xml"

# Locale-shaped resource qualifiers only (independent review, 2026-08-07):
# `values-*` also covers non-locale config qualifiers this app's own theme
# already invites (Theme.AppCompat.DayNight -> values-night/) plus the usual
# density/orientation/screen-size/API-level set (values-v33, values-land,
# values-sw600dp, ...) -- glob-matching ALL of `values-*` treated every one
# of those as a "locale missing every key", a guaranteed false failure the
# moment any of them is ever added. Matches Android's own qualifier grammar
# for language[-region] (old style, e.g. "fa", "en-rUS") and BCP-47 style
# ("b+en+US") -- the only two shapes a real locale qualifier takes.
LOCALE_QUALIFIER = re.compile(r'^(?:[a-z]{2,3}(?:-r[A-Z]{2})?|b\+[a-zA-Z0-9+]+)$')

def load(path):
    """name -> (kind, value) where kind is "string" (raw text value) or
    "plural" (dict of quantity -> text). Uses itertext(), not el.text alone
    (independent review, 2026-08-07): el.text truncates at the first child
    element, so a string using inline markup (<b>, <xliff:g>) would have
    silently lost everything after that tag on BOTH sides of the
    comparison -- not just the locale's, the BASE's own placeholder
    detection too, which then blamed a locale for a placeholder mismatch
    the base string itself only appeared to be missing."""
    tree = ET.parse(path)
    out = {}
    for el in tree.getroot().findall("string"):
        out[el.get("name")] = ("string", "".join(el.itertext()))
    for el in tree.getroot().findall("plurals"):
        quantities = {
            item.get("quantity"): "".join(item.itertext())
            for item in el.findall("item")
        }
        out[el.get("name")] = ("plural", quantities)
    return out

def placeholders(text):
    # Android format strings: %1$s, %2$d, etc. Position-numbered only (this
    # file never uses bare %s) -- sorted so order-only differences still
    # compare equal to a same-set check, but a MISSING or ADDED placeholder
    # (a real crash risk) does not.
    return sorted(re.findall(r'%\d+\$[a-zA-Z]', text))

base = load(BASE)
base_keys = set(base.keys())

candidates = sorted(glob.glob("android/app/src/main/res/values-*/strings.xml"))
locale_dirs = [
    p for p in candidates
    if LOCALE_QUALIFIER.match(p.split("/")[-2].removeprefix("values-"))
]
skipped = sorted(set(candidates) - set(locale_dirs))
if skipped:
    print("guard-android-i18n: skipping non-locale qualifier(s) (not a "
          f"translation gap): {', '.join(p.split('/')[-2] for p in skipped)}")
if not locale_dirs:
    print("guard-android-i18n: no values-<locale>/strings.xml directories found "
          "-- nothing to check (this is a real gap, not a guard bug, if "
          "translations were expected to already exist)")
    sys.exit(0)

for path in locale_dirs:
    keys = load(path)
    missing = sorted(base_keys - keys.keys())
    orphan = sorted(keys.keys() - base_keys)
    if missing:
        fail = True
        print(f"guard-android-i18n: {path} is missing keys: {', '.join(missing)}")
    if orphan:
        fail = True
        print(f"guard-android-i18n: {path} has orphan keys not in {BASE}: {', '.join(orphan)}")
    for key in base_keys & keys.keys():
        base_kind, base_val = base[key]
        loc_kind, loc_val = keys[key]
        if base_kind != loc_kind:
            fail = True
            print(f"guard-android-i18n: {path}'s '{key}' is a {loc_kind}, "
                  f"base's is a {base_kind} -- same name must be the same "
                  f"resource type on both sides")
            continue
        if base_kind == "string":
            base_ph, loc_ph = placeholders(base_val), placeholders(loc_val)
            if base_ph != loc_ph:
                fail = True
                print(f"guard-android-i18n: {path}'s '{key}' has placeholders {loc_ph}, "
                      f"base has {base_ph} -- a mismatched/dropped placeholder crashes "
                      f"String.format at runtime instead of failing here")
        else:
            # plurals: quantity SETS legitimately differ across languages
            # (CLDR plural categories vary -- Arabic has six, English two;
            # Android falls back to "other" for a missing category, so
            # that's not a bug). What must still hold: every quantity that
            # DOES exist on both sides has matching placeholders, same
            # crash-class check as a plain string.
            for qty in set(base_val) & set(loc_val):
                base_ph, loc_ph = placeholders(base_val[qty]), placeholders(loc_val[qty])
                if base_ph != loc_ph:
                    fail = True
                    print(f"guard-android-i18n: {path}'s '{key}' quantity=\"{qty}\" has "
                          f"placeholders {loc_ph}, base has {base_ph}")

if fail:
    sys.exit(1)
print(f"✓ android-i18n guard: {len(base_keys)} keys match across "
      f"{len(locale_dirs)} locale(s) ({', '.join(sorted(p.split('/')[-2].removeprefix('values-') for p in locale_dirs))}), "
      f"placeholders intact")
PY
