package shoptemplate

import (
	"reflect"
	"testing"
	"time"
)

func TestUndo_RoundTripAndExpiry(t *testing.T) {
	at := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	u := NewUndo("cafe", "retail", `{"id":"retail"}`, map[string]string{"sale.order_type_prompt": "top"}, map[string]string{"sale.order_type_prompt": "off"}, at)
	if got, want := u.ExpiresAt, at.Add(7*24*time.Hour).Format(time.RFC3339); got != want {
		t.Fatalf("ExpiresAt = %s, want %s (7 days)", got, want)
	}
	raw := u.Encode()

	got, ok := ParseUndo(raw, at.Add(7*24*time.Hour-time.Second))
	if !ok || !reflect.DeepEqual(got, u) {
		t.Fatalf("just before expiry: ok=%v got=%+v want %+v", ok, got, u)
	}
	if _, ok := ParseUndo(raw, at.Add(7*24*time.Hour)); ok {
		t.Fatal("at the expiry instant the record is gone")
	}
}

func TestUndo_GarbageAndEmptyAreNone(t *testing.T) {
	for _, raw := range []string{"", "{", "null", `{"to_type":""}`, `{"to_type":"cafe","expires_at":"yesterday"}`} {
		if _, ok := ParseUndo(raw, time.Now()); ok {
			t.Fatalf("ParseUndo(%q) must be none", raw)
		}
	}
}
