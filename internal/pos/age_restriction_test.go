package pos

import (
	"testing"
	"time"
)

func TestValidAgeVerificationOutcome(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{string(AgeVerificationAccepted), true},
		{string(AgeVerificationRefused), true},
		{"", false},
		{"Accepted", false},
		{"pending", false},
		{"accepted ", false},
	} {
		if got := ValidAgeVerificationOutcome(tc.in); got != tc.want {
			t.Errorf("ValidAgeVerificationOutcome(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestDOBBeforeCutoff pins the permanent birth-date-cutoff mechanism
// (ut-docs#3340 AC3): a restriction expressed as "born before <date>" rather
// than a rolling age. The 2009-01-01 cutoff is a FIXTURE only — it lives in
// this _test.go file and never in production code; core ships no
// jurisdiction's date (ADR-0050).
func TestDOBBeforeCutoff(t *testing.T) {
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	cutoff := day(2009, time.January, 1)
	for _, tc := range []struct {
		name string
		dob  time.Time
		want bool
	}{
		{"day before cutoff is allowed", day(2008, time.December, 31), true},
		{"long before cutoff is allowed", day(1970, time.June, 15), true},
		{"born ON the cutoff is not allowed", day(2009, time.January, 1), false},
		{"day after cutoff is not allowed", day(2009, time.January, 2), false},
		{"well after cutoff is not allowed", day(2015, time.March, 3), false},
		{"last instant before cutoff is allowed", cutoff.Add(-time.Nanosecond), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DOBBeforeCutoff(tc.dob, cutoff); got != tc.want {
				t.Fatalf("DOBBeforeCutoff(%s, %s) = %v, want %v", tc.dob.Format(time.RFC3339Nano), cutoff.Format(time.DateOnly), got, tc.want)
			}
		})
	}
}

// ageResolver is a minimal PriceResolver for the Service-level ID-check
// tests: BEER is restricted (as the real resolver would set from
// items.age_restricted), BREAD is not.
type ageResolver map[string]BasketLine

func (m ageResolver) Resolve(code string) (BasketLine, bool) {
	l, ok := m[code]
	return l, ok
}

func newAgeService() *Service {
	return NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, ageResolver{
		"BEER":  {SKU: "BEER", Name: "Lager 4x440ml", Qty: 1, PriceCents: 500, ItemID: "itm-beer", AgeRestricted: true},
		"BREAD": {SKU: "BREAD", Name: "Bread", Qty: 1, PriceCents: 150, ItemID: "itm-bread"},
	})
}

func lineKeyFor(t *testing.T, s *Service, sku string) string {
	t.Helper()
	for _, l := range s.Basket().Lines {
		if l.SKU == sku {
			return l.LineKey
		}
	}
	t.Fatalf("no line for %s", sku)
	return ""
}

func TestRecordAgeCheck_PublishesOutcomePerItem(t *testing.T) {
	s := newAgeService()
	if _, err := s.Scan("BEER"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan("BREAD"); err != nil {
		t.Fatal(err)
	}
	if !s.Basket().Lines[0].AgeRestricted {
		t.Fatal("the resolver's AgeRestricted flag must reach the basket line")
	}
	beerKey := lineKeyFor(t, s, "BEER")

	b, ok := s.RecordAgeCheck(beerKey, AgeVerificationRefused, "user1")
	if !ok || b.AgeChecks["itm-beer"] != "refused" {
		t.Fatalf("refused check: ok=%v published=%v", ok, b.AgeChecks)
	}
	// Changing the answer (customer then shows ID) replaces it — one entry
	// per item, first-recorded order kept.
	b, ok = s.RecordAgeCheck(beerKey, AgeVerificationAccepted, "user2")
	if !ok || b.AgeChecks["itm-beer"] != "accepted" {
		t.Fatalf("accepted check: ok=%v published=%v", ok, b.AgeChecks)
	}
	checks := s.AgeChecks()
	if len(checks) != 1 || checks[0].Outcome != AgeVerificationAccepted || checks[0].CashierID != "user2" || checks[0].ItemName != "Lager 4x440ml" {
		t.Fatalf("AgeChecks: %+v", checks)
	}

	// Not restricted / unknown line / bad outcome: refused, nothing recorded.
	if _, ok := s.RecordAgeCheck(lineKeyFor(t, s, "BREAD"), AgeVerificationAccepted, "user1"); ok {
		t.Fatal("an unrestricted line must not accept an ID check")
	}
	if _, ok := s.RecordAgeCheck("no-such-key", AgeVerificationAccepted, "user1"); ok {
		t.Fatal("an unknown line key must be refused")
	}
	if _, ok := s.RecordAgeCheck(beerKey, AgeVerificationOutcome("maybe"), "user1"); ok {
		t.Fatal("an outcome outside the vocabulary must be refused")
	}
	if len(s.AgeChecks()) != 1 {
		t.Fatalf("refused calls must record nothing, got %+v", s.AgeChecks())
	}
}

// A published AgeChecks map is never mutated in place: a Basket copy taken
// before a later answer keeps reading the earlier one.
func TestRecordAgeCheck_PublishedMapIsImmutable(t *testing.T) {
	s := newAgeService()
	if _, err := s.Scan("BEER"); err != nil {
		t.Fatal(err)
	}
	key := lineKeyFor(t, s, "BEER")
	first, _ := s.RecordAgeCheck(key, AgeVerificationRefused, "user1")
	s.RecordAgeCheck(key, AgeVerificationAccepted, "user1")
	if first.AgeChecks["itm-beer"] != "refused" {
		t.Fatalf("an earlier Basket copy must not see a later write, got %v", first.AgeChecks)
	}
}

// TestAgeChecks_ClearedOnResetAndResume pins the documented "held sales
// re-ask" behaviour (Service.ageChecks' doc comment; help topic
// age-restricted-sales). ut-docs#3340 review: an earlier version recorded
// its "stale" check on a non-existent line key after Reset — a no-op — so
// the resume assertion held vacuously. Here every check is a REAL accepted
// outcome on a real line for the SAME item the resumed sale carries, so a
// RestoreHeld that kept ageChecks would leave the resumed line looking
// verified and the assertions below would fail.
func TestAgeChecks_ClearedOnResetAndResume(t *testing.T) {
	needsCheck := func(t *testing.T, s *Service) {
		t.Helper()
		b := s.Basket()
		if len(b.Lines) != 1 || !b.Lines[0].AgeRestricted {
			t.Fatalf("a resumed line must still be age restricted: %+v", b.Lines)
		}
		if len(s.AgeChecks()) != 0 || len(b.AgeChecks) != 0 {
			t.Fatalf("a resumed basket must not inherit an ID-check outcome: checks=%+v published=%v", s.AgeChecks(), b.AgeChecks)
		}
		if _, ok := UnresolvedAgeRestrictedLine(b.Lines, nil, s.AgeChecks()); !ok {
			t.Fatal("the resumed restricted line must need an ID check again before tender")
		}
	}

	t.Run("hold, reset, new sale checks the same item, resume", func(t *testing.T) {
		s := newAgeService()
		if _, err := s.Scan("BEER"); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.RecordAgeCheck(lineKeyFor(t, s, "BEER"), AgeVerificationAccepted, "user1"); !ok {
			t.Fatal("recording a check on the real restricted line must succeed")
		}
		if _, ok := UnresolvedAgeRestrictedLine(s.Basket().Lines, nil, s.AgeChecks()); ok {
			t.Fatal("precondition: the accepted check must clear the line before it is held")
		}
		snap := s.Snapshot() // hold
		if len(snap.Lines) != 1 || !snap.Lines[0].AgeRestricted {
			t.Fatalf("the snapshot must carry the line's AgeRestricted flag: %+v", snap.Lines)
		}

		s.Reset()
		if len(s.AgeChecks()) != 0 || len(s.Basket().AgeChecks) != 0 {
			t.Fatal("Reset must clear every recorded ID check — they belong to one sale")
		}

		// Another customer's sale checks ID for the very same item, so a
		// live accepted check for itm-beer exists when the held sale resumes.
		if _, err := s.Scan("BEER"); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.RecordAgeCheck(lineKeyFor(t, s, "BEER"), AgeVerificationAccepted, "user2"); !ok {
			t.Fatal("recording the second sale's check must succeed")
		}
		if len(s.AgeChecks()) != 1 {
			t.Fatalf("precondition: a live check must exist before resume, got %+v", s.AgeChecks())
		}

		s.RestoreHeld(snap, HeldOrigin{})
		needsCheck(t, s)
	})

	t.Run("resume straight over a checked basket", func(t *testing.T) {
		s := newAgeService()
		if _, err := s.Scan("BEER"); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.RecordAgeCheck(lineKeyFor(t, s, "BEER"), AgeVerificationAccepted, "user1"); !ok {
			t.Fatal("recording a check on the real restricted line must succeed")
		}
		snap := s.Snapshot()
		if len(s.AgeChecks()) != 1 {
			t.Fatalf("precondition: the check must be live before resume, got %+v", s.AgeChecks())
		}
		// No Reset in between: RestoreHeld itself must drop the check, even
		// when it is the held sale's own earlier answer.
		s.RestoreHeld(snap, HeldOrigin{})
		needsCheck(t, s)
	})
}

func TestMarkAgeRestricted_BackstopFlagsLegacyLines(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, ageResolver{
		// A line whose flag was lost (e.g. a pre-flag held-sale payload).
		"OLD": {SKU: "OLD", Name: "Cider", Qty: 1, PriceCents: 300, ItemID: "itm-cider"},
	})
	if _, err := s.Scan("OLD"); err != nil {
		t.Fatal(err)
	}
	s.MarkAgeRestricted(map[string]bool{"itm-cider": true})
	if !s.Basket().Lines[0].AgeRestricted {
		t.Fatal("MarkAgeRestricted must flag a line whose item the DB says is restricted")
	}
	s.MarkAgeRestricted(map[string]bool{})
	if !s.Basket().Lines[0].AgeRestricted {
		t.Fatal("MarkAgeRestricted only ever adds the flag (fail closed)")
	}
}

func TestUnresolvedAgeRestrictedLine(t *testing.T) {
	beer := BasketLine{LineKey: "k1", ItemID: "itm-beer", Name: "Beer", AgeRestricted: true}
	bread := BasketLine{LineKey: "k2", ItemID: "itm-bread", Name: "Bread"}
	cider := BasketLine{LineKey: "k3", ItemID: "itm-cider", Name: "Cider"} // flag only from the DB
	lines := []BasketLine{bread, beer, cider}
	restricted := map[string]bool{"itm-cider": true}

	if l, ok := UnresolvedAgeRestrictedLine(lines, restricted, nil); !ok || l.LineKey != "k1" {
		t.Fatalf("no checks: want first restricted line k1, got %v %+v", ok, l)
	}
	refused := []AgeCheck{{ItemID: "itm-beer", Outcome: AgeVerificationRefused}, {ItemID: "itm-cider", Outcome: AgeVerificationAccepted}}
	if l, ok := UnresolvedAgeRestrictedLine(lines, restricted, refused); !ok || l.LineKey != "k1" {
		t.Fatalf("refused beer must still block, got %v %+v", ok, l)
	}
	acceptedBeer := []AgeCheck{{ItemID: "itm-beer", Outcome: AgeVerificationAccepted}}
	if l, ok := UnresolvedAgeRestrictedLine(lines, restricted, acceptedBeer); !ok || l.LineKey != "k3" {
		t.Fatalf("DB-flagged cider with no check must block, got %v %+v", ok, l)
	}
	all := []AgeCheck{{ItemID: "itm-beer", Outcome: AgeVerificationAccepted}, {ItemID: "itm-cider", Outcome: AgeVerificationAccepted}}
	if _, ok := UnresolvedAgeRestrictedLine(lines, restricted, all); ok {
		t.Fatal("every restricted item accepted: nothing should block")
	}
	if _, ok := UnresolvedAgeRestrictedLine([]BasketLine{bread}, nil, nil); ok {
		t.Fatal("an unrestricted basket must never block")
	}
}

func TestAgeVerificationsForSale(t *testing.T) {
	lines := []BasketLine{{ItemID: "itm-beer"}, {ItemID: "itm-bread"}}
	checks := []AgeCheck{
		{ItemID: "itm-beer", Outcome: AgeVerificationAccepted},
		{ItemID: "itm-wine", Outcome: AgeVerificationAccepted}, // put back: not sold
		{ItemID: "itm-vape", Outcome: AgeVerificationRefused},  // removed after refusal: still logged
	}
	got := AgeVerificationsForSale(lines, checks)
	if len(got) != 2 || got[0].ItemID != "itm-beer" || got[1].ItemID != "itm-vape" {
		t.Fatalf("want beer (accepted, sold) + vape (refused), got %+v", got)
	}
	if got := AgeVerificationsForSale(lines, nil); len(got) != 0 {
		t.Fatalf("no checks: want none, got %+v", got)
	}
}
