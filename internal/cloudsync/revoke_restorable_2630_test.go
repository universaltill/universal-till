package cloudsync

import "testing"

func TestParseRevokeSignal_StoreDeletedCarriesRestorableUntil(t *testing.T) {
	body := []byte(`{"error":{"code":"device_revoked"},"data":{"reason":"store_deleted","recover":false,"restorable_until":"2031-12-20T12:00:00Z"}}`)
	sig := parseRevokeSignal(body)
	if sig == nil || sig.Reason != "store_deleted" || sig.Detail != "2031-12-20T12:00:00Z" {
		t.Fatalf("sig = %+v, want store_deleted with detail 2031-12-20T12:00:00Z", sig)
	}
	// Absent: tolerated.
	sig = parseRevokeSignal([]byte(`{"error":{"code":"device_revoked"},"data":{"reason":"store_deleted"}}`))
	if sig == nil || sig.Detail != "" {
		t.Fatalf("sig = %+v, want empty detail", sig)
	}
}
