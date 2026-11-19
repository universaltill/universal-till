package pos

import "testing"

// ut-docs#6602 review M1: legs a reset handed off are not reversed again, and
// an attempt dropped by a reset is no longer live.
func TestHandedOffLegsAndLiveAttempt(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{})
	id := s.TenderAttemptID()
	if !s.TenderAttemptIs(id) {
		t.Fatal("fresh attempt must be live")
	}
	a := CapturedLeg{RequestID: id + ":0:x"}
	b := CapturedLeg{RequestID: id + ":1:x"}
	s.LockTender("sig", PendingTender{Captured: []CapturedLeg{a}})
	s.Reset()
	if s.TenderAttemptIs(id) {
		t.Fatal("a reset must retire the attempt")
	}
	if s.RotateTenderAttemptIDIf(id) {
		t.Fatal("a retired attempt must not rotate the next basket's state")
	}
	got := s.NotHandedOff([]CapturedLeg{a, b})
	if len(got) != 1 || got[0].RequestID != b.RequestID {
		t.Fatalf("only the leg not handed off may remain: %+v", got)
	}
}
