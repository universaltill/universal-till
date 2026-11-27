package data

// The self-heal engine's saved state (ADR-0222 §4.4, internal/credheal). All
// keys sit under "till_identity.", which is per till: never admin-synced to a
// replica, never copied by a join snapshot (db.TillCloudIdentityPrefixes),
// kept from a restore, and cleared by an identity reset. None holds a token,
// key, nonce or hash.
const (
	// CredHealPrefix is the family prefix.
	CredHealPrefix = "till_identity.heal."

	CredHealStateKey         = "till_identity.heal.state"           // healing | waiting | needs_person ("" = ok)
	CredHealReasonKey        = "till_identity.heal.reason"          // plain-words reason code
	CredHealDetailKey        = "till_identity.heal.detail"          // cloud text (a till or shop name), clamped
	CredHealSinceKey         = "till_identity.heal.since"           // RFC 3339
	CredHealUntilKey         = "till_identity.heal.until"           // RFC 3339, waiting
	CredHealNextAttemptAtKey = "till_identity.heal.next_attempt_at" // RFC 3339
	CredHealAttemptsKey      = "till_identity.heal.attempts"        // consecutive transient failures
	CredHealLastHealedAtKey  = "till_identity.heal.last_healed_at"  // RFC 3339
	CredHealHealsKey         = "till_identity.heal.heals"           // unix seconds of recent heals, comma list
	CredHealRefusedKey       = "till_identity.heal.refused"         // rounds ended in recover_refused since the last heal
	CredHealCodeKey          = "till_identity.heal.code"            // the 401 code that last drove the state
)
