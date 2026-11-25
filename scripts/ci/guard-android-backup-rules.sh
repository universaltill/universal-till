#!/usr/bin/env bash
#
# ut-docs#6915: the Android app must keep secrets/ and tls/ out of cloud
# backup and device-to-device transfer (same set as iOS #5140). Without
# secrets/install-id a transferred install is detected as restored
# (internal/enroll/restored.go) and asks to connect again. Checks the manifest
# points at both rule files, each excludes secrets, tls, keys + fiscal (both
# cloud-backup and device-transfer for API 31+), none <include>s anything, and
# the install marker still lives under secrets/. allowBackup stays "false".
# ut-docs#6918: keys/ (off-site backup key) and fiscal/ (signing-device
# credential) are excluded too, and their Go paths are pinned so moving either
# file out of its excluded folder fails here.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

MANIFEST="${1:-android/app/src/main/AndroidManifest.xml}"
RES_XML="$(dirname "${MANIFEST}")/res/xml"
RESTORED="internal/enroll/restored.go"
SERVICE="android/app/src/main/java/com/universaltill/pos/TillService.kt"
KEYSTORE="internal/offsitebackup/keystore.go"
CREDSTORE="internal/fiscal/tse_credential_store.go"

MANIFEST="${MANIFEST}" RES_XML="${RES_XML}" RESTORED="${RESTORED}" SERVICE="${SERVICE}" KEYSTORE="${KEYSTORE}" CREDSTORE="${CREDSTORE}" python3 - <<'PY'
import os
import re
import sys
import xml.etree.ElementTree as ET

A = "{http://schemas.android.com/apk/res/android}"
P = "guard-android-backup-rules:"
errors = []
need = {"secrets", "tls", "keys", "fiscal"}


def load(path):
    if not os.path.isfile(path):
        errors.append(f"{path} is missing")
        return None
    try:
        return ET.parse(path).getroot()
    except ET.ParseError as e:
        errors.append(f"{path} is not well-formed XML: {e}")
        return None


def check_excludes(el, label):
    if el is None:
        errors.append(f"{label} is missing")
        return
    if el.findall(".//include"):
        errors.append(f"{label} has an <include>; only <exclude> rules are allowed")
    got = {
        x.get(A + "path", x.get("path", "")).rstrip("/")
        for x in el.findall("exclude")
        if x.get("domain") == "file"
    }
    for p in sorted(need - got):
        errors.append(f"{label} does not exclude domain=file path=\"{p}\"")


manifest = load(os.environ["MANIFEST"])
if manifest is not None:
    app = manifest.find("application")
    if app is None:
        errors.append("manifest has no <application>")
    else:
        if app.get(A + "allowBackup") != "false":
            errors.append('android:allowBackup must stay "false"')
        if app.get(A + "dataExtractionRules") != "@xml/data_extraction_rules":
            errors.append("<application> lacks android:dataExtractionRules=\"@xml/data_extraction_rules\"")
        if app.get(A + "fullBackupContent") != "@xml/backup_rules":
            errors.append("<application> lacks android:fullBackupContent=\"@xml/backup_rules\"")

res = os.environ["RES_XML"]
ext = load(os.path.join(res, "data_extraction_rules.xml"))
if ext is not None:
    if ext.tag != "data-extraction-rules":
        errors.append(f"data_extraction_rules.xml root is <{ext.tag}>, must be <data-extraction-rules>")
    for block in ("cloud-backup", "device-transfer"):
        check_excludes(ext.find(block), f"data_extraction_rules.xml <{block}>")
bk = load(os.path.join(res, "backup_rules.xml"))
if bk is not None:
    if bk.tag != "full-backup-content":
        errors.append(f"backup_rules.xml root is <{bk.tag}>, must be <full-backup-content>")
    check_excludes(bk, "backup_rules.xml <full-backup-content>")

restored = os.environ["RESTORED"]
try:
    src = open(restored).read()
except OSError:
    src = ""
if not re.search(r'installMarkerRel\s*=\s*\[\]string\{"secrets",\s*"install-id"\}', src):
    errors.append(f"{restored}: installMarkerRel is no longer secrets/install-id; the exclusion would not trigger restored-install detection")

def read(path):
    try:
        return open(path).read()
    except OSError:
        return ""


def code_of(path):
    return "\n".join(l for l in read(path).splitlines() if not l.strip().startswith("//"))


ks = code_of(os.environ["KEYSTORE"])
if 'paths.Data("keys")' not in ks or not re.search(r'keyFileName\s*=\s*"offsite-backup.key"', ks):
    errors.append(f"{os.environ['KEYSTORE']}: the off-site backup key is no longer kept under keys/; it would travel in backups")
cs = code_of(os.environ["CREDSTORE"])
for var in ("signingDeviceCredentialRelPath", "legacyTSECredentialRelPath"):
    if not re.search(var + r'\s*=\s*\[\]string\{"fiscal",', cs) or not re.search(r'paths\.Data\(' + var + r'\.\.\.\)', cs):
        errors.append(f"{os.environ['CREDSTORE']}: {var} is no longer under fiscal/ (or no longer read via paths.Data); it would travel in backups")

service = os.environ["SERVICE"]
try:
    code = [l for l in open(service).read().splitlines() if not l.strip().startswith(("//", "*", "/*"))]
except OSError:
    code = []
if not any("Mobile.start(filesDir.absolutePath)" in l for l in code):
    errors.append(f"{service}: must call Mobile.start(filesDir.absolutePath); the domain=\"file\" rules depend on filesDir being the data root")

if errors:
    for e in errors:
        print(f"{P} {e}", file=sys.stderr)
    sys.exit(1)
print(f"{P} OK")
PY
