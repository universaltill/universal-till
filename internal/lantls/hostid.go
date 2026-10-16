package lantls

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Host id (ADR-0114 §7's "on restore to new hardware", ut-docs#4450). The
// LAN key travels only with a copy of the whole data directory (a disk
// image, an SD-card clone, a copied data folder) — in-app backups never
// carry <data>/tls/ — so a load that finds the directory on a different
// machine than last time starts a rotation.
//
// Each platform reads one id (hostid_<os>.go); none is ever the cause of a
// failed load: an unreadable or unknown id is "", which never rotates.
// A machine whose machine-id is regenerated at every boot (a read-only or
// volatile /etc) restarts the rotation at every start and never completes
// one; the current key keeps being served.

// hostIdentity returns this machine's id and the name of its source
// ("dt-serial", "machine-id", …); ("", "") when unknown. A var for tests.
var hostIdentity = readHostID

// hostTagKey makes the stored tag specific to this use, so rotation.json
// never holds the raw id (machine-id(5): hash it with an app-specific key).
var hostTagKey = []byte("universal-till lantls host id v1")

// hostTag is what rotation.json stores: "<source>:<hex HMAC-SHA256 of the
// id>"; "" when either part is empty. The id is not stored in clear; a
// short one (a Pi serial) is not secret either, since the key is public.
func hostTag(source, raw string) string {
	if source == "" || raw == "" {
		return ""
	}
	m := hmac.New(sha256.New, hostTagKey)
	m.Write([]byte(source + "\x00" + raw))
	return source + ":" + hex.EncodeToString(m.Sum(nil))
}

// cleanHostID trims a raw id as read from a file (devicetree strings end
// in NUL) and drops values that identify no machine: empty, all zeros,
// or systemd's "uninitialized" first-boot placeholder.
func cleanHostID(s string) string {
	s = strings.TrimSpace(strings.Trim(s, "\x00"))
	if s == "" || s == "uninitialized" || strings.Trim(s, "0-") == "" {
		return ""
	}
	return s
}
