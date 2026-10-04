package catalogsync

import (
	"strconv"
	"testing"
	"time"
)

func newTestStamps() (*savedStamps, *time.Time) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	return &savedStamps{m: map[string]*savedStamp{}, now: func() time.Time { return now }}, &now
}

// ut-docs#3606: only a stamp this till's own successful save moved past is
// substituted, on the same main till and record.
func TestRecentSaves_SubstitutesOnlyThisTillsOwnPreviousSave(t *testing.T) {
	s, _ := newTestStamps()
	if got := s.base("http://main", "category", "cat1", "08:00"); got != "08:00" {
		t.Fatalf("nothing remembered: base = %q, want the shown stamp", got)
	}
	s.remember("http://main", "category", "cat1", "08:00", "09:00")
	for _, c := range []struct{ main, kind, id, shown, want string }{
		{"http://main", "category", "cat1", "08:00", "09:00"}, // the re-rendered form
		{"http://main", "category", "cat1", "09:00", "09:00"}, // pulled since: as shown
		{"http://main", "category", "cat1", "07:00", "07:00"}, // an older form: as shown (a conflict)
		{"http://main", "category", "cat1", "", ""},
		{"http://other", "category", "cat1", "08:00", "08:00"},
		{"http://main", "variant", "cat1", "08:00", "08:00"},
		{"http://main", "category", "cat2", "08:00", "08:00"},
	} {
		if got := s.base(c.main, c.kind, c.id, c.shown); got != c.want {
			t.Errorf("base(%s %s %s %q) = %q, want %q", c.main, c.kind, c.id, c.shown, got, c.want)
		}
	}
}

// Review finding (ut-docs#3606): three saves in a row — a form may show any
// stamp of this till's own chain (pre-save, or mid-chain after a partial
// pull) and still stand for the latest.
func TestRecentSaves_ChainCoversEveryStampThisTillsSavesMovedPast(t *testing.T) {
	s, _ := newTestStamps()
	s.remember("m", "category", "c", "08:00", "09:00")                               // save 1 (shown 08:00)
	s.remember("m", "category", "c", s.base("m", "category", "c", "08:00"), "10:00") // save 2 (still shows 08:00)
	for _, shown := range []string{"08:00", "09:00"} {
		if got := s.base("m", "category", "c", shown); got != "10:00" {
			t.Errorf("form showing %s: base = %q, want 10:00", shown, got)
		}
	}
	s.remember("m", "category", "c", s.base("m", "category", "c", "09:00"), "11:00") // save 3 (shows 09:00)
	for _, shown := range []string{"08:00", "09:00", "10:00"} {
		if got := s.base("m", "category", "c", shown); got != "11:00" {
			t.Errorf("after save 3, form showing %s: base = %q, want 11:00", shown, got)
		}
	}
}

// The chain restarts when a save's base is not the remembered latest stamp
// (someone else changed the record in between), so a form older than that
// change is sent as shown and refused as a conflict.
func TestRecentSaves_ChainRestartsAfterSomeoneElsesChange(t *testing.T) {
	s, _ := newTestStamps()
	s.remember("m", "item", "i", "08:00", "09:00")
	// Another till moved the record to 10:00; this till pulled it and saved.
	s.remember("m", "item", "i", "10:00", "11:00")
	if got := s.base("m", "item", "i", "08:00"); got != "08:00" {
		t.Fatalf("form older than another till's change: base = %q, want it sent as shown", got)
	}
	if got := s.base("m", "item", "i", "10:00"); got != "11:00" {
		t.Fatalf("form showing the pre-save stamp: base = %q, want 11:00", got)
	}
}

func TestRecentSaves_ExpireAndEvictOldest(t *testing.T) {
	s, now := newTestStamps()
	s.remember("m", "item", "first", "a", "b")
	*now = now.Add(time.Second)
	for i := 0; i < recentSavesMax; i++ {
		s.remember("m", "item", strconv.Itoa(i), "a", "b")
	}
	if len(s.m) > recentSavesMax {
		t.Fatalf("recentSaves holds %d entries, bound is %d", len(s.m), recentSavesMax)
	}
	if _, ok := s.m[savedStampKey("m", "item", "first")]; ok {
		t.Fatal("the oldest entry survived eviction")
	}
	if got := s.base("m", "item", "0", "a"); got != "b" {
		t.Fatalf("a recent entry was evicted: base = %q", got)
	}
	*now = now.Add(recentSavesTTL + time.Second)
	if got := s.base("m", "item", "0", "a"); got != "a" {
		t.Fatalf("expired entry still substituted: base = %q", got)
	}
}
