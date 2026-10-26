package enroll

import (
	"context"
	"strings"

	"github.com/universaltill/universal-till/internal/logging"
)

// A replaced till (ut-docs#5058; design §3.3 T2; ADR-0117 amendment
// 2026-10-26). The owner replaced this till as the shop's main till on my.:
// its credential is tombstoned and every cloud call answers 401
// device_revoked with data {reason: "replaced", replaced_at, replaced_by}.
// The till records that here, persisted, because it must act on it from the
// moment it starts — before any check-in — and for good:
//   - it stops advertising itself as a main till on the shop network and
//     refuses LAN joins, links and journals, so it never fights the new
//     main till for the role (its credential cannot claim it back either);
//   - it keeps selling offline (ADR-0116 D6, decision D4), with a chip and a
//     banner that say "Replaced by <name>".
//
// The keys sit under till_identity.* (db.TillCloudIdentityPrefixes): they
// belong to this device's cloud identity, so they never travel in a join
// snapshot, and a fresh pairing (resetIdentityForPair) clears them — a new
// identity is not the replaced one. A restored backup still carries them
// until ut-docs#5069 (restore keeps the live till's own identity rows) has
// landed: restoring the old till's backup onto its replacement before then
// makes the replacement read as replaced (Settings → Pair clears it).
const (
	keyReplaced   = "till_identity.replaced"
	keyReplacedBy = "till_identity.replaced_by"
	keyReplacedAt = "till_identity.replaced_at"
)

// Replaced is the persisted replaced state: By is the successor's name as
// the cloud named it ("" until it has checked in), At the RFC 3339 instant.
type Replaced struct {
	By, At string
}

// MarkReplaced records that the cloud said this till was replaced. Cloud
// text is clamped; an empty name never erases a known one (the cloud sends
// "" until the successor's first check-in). Failures are logged: the next
// refused call marks it again.
func MarkReplaced(ctx context.Context, kv Settings, by, at string) {
	by, at = clampText(by, 80), clampText(at, 40)
	cur, _ := ReplacedState(ctx, kv)
	if by == "" {
		by = cur.By
	}
	if at == "" {
		at = cur.At
	}
	if cur.By == by && cur.At == at {
		if v, _, _ := kv.Get(ctx, keyReplaced); v == "1" {
			return
		}
	}
	for _, kvp := range [][2]string{{keyReplacedBy, by}, {keyReplacedAt, at}, {keyReplaced, "1"}} {
		if err := kv.Set(ctx, kvp[0], kvp[1]); err != nil {
			logging.L().Warnf("enrolment: record replaced state %s: %v", kvp[0], err)
			return
		}
	}
	logging.L().Warnf("enrolment: the shop's owner replaced this till (by %q); it no longer acts as main till (ut-docs#5058)", by)
}

// ReplacedState reports whether this till was replaced, and by whom. A read
// error reads as not replaced: the state only ever withholds LAN roles, and
// the next refused cloud call records it again.
func ReplacedState(ctx context.Context, kv Settings) (Replaced, bool) {
	if kv == nil {
		return Replaced{}, false
	}
	v, _, err := kv.Get(ctx, keyReplaced)
	if err != nil || strings.TrimSpace(v) != "1" {
		return Replaced{}, false
	}
	by, _, _ := kv.Get(ctx, keyReplacedBy)
	at, _, _ := kv.Get(ctx, keyReplacedAt)
	return Replaced{By: strings.TrimSpace(by), At: strings.TrimSpace(at)}, true
}

// clearReplaced drops the replaced state (a fresh pairing).
func clearReplaced(ctx context.Context, kv Settings) {
	for _, k := range []string{keyReplaced, keyReplacedBy, keyReplacedAt} {
		if err := kv.Set(ctx, k, ""); err != nil {
			logging.L().Warnf("enrolment: clear %s: %v", k, err)
		}
	}
}
