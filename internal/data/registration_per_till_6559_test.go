package data

import "testing"

// ADR-0200 (ut-docs#6559): the registration.* settings (notice shown, stop
// switch, last compared version, last cloud answer) are this till's own.
// last_checkin_at is rewritten on every cloud tick: admin-synced, it would move
// the admin fingerprint each tick (the #2792 failure) and a main till's "stopped"
// or "notice shown" would leak onto replicas.
func TestRegistrationSettingsArePerTill(t *testing.T) {
	for _, k := range []string{
		"registration.notice_shown_at", "registration.notice_version",
		"registration.stopped", "registration.last_reported_version",
		"registration.last_checkin_at",
	} {
		if !perTillSetting(k) {
			t.Errorf("%s is admin-synced", k)
		}
	}
}
