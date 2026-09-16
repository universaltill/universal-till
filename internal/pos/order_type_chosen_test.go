package pos

import (
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#2282: the settings-driven "when to ask dine-in/takeaway" placement
// (top of basket / before the first item / at Pay) needs a way to tell "the
// cashier has never been asked this sale" apart from "the cashier explicitly
// chose dine-in" -- both read as OrderType == "" otherwise. Basket.OrderTypeChosen
// is that flag: false until SetOrderType is called for the first time this
// sale, reset on every new sale (Reset/resetLocked) and after a completed
// Tender, and treated as already-answered on a resumed held sale (the
// question was already settled when it was originally rung up / parked).

func newOrderTypeChosenService() *Service {
	return NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{
		"DRINK": {SKU: "DRINK", ItemID: "item-drink", Name: "Coffee", Qty: 1, PriceCents: 1000, TaxRateBP: 1900},
	})
}

func TestOrderTypeChosen_FalseUntilSetOrderTypeIsCalled(t *testing.T) {
	s := newOrderTypeChosenService()
	if s.Basket().OrderTypeChosen {
		t.Fatal("a fresh basket must start with OrderTypeChosen=false")
	}
	_, _ = s.Scan("DRINK")
	if s.Basket().OrderTypeChosen {
		t.Fatal("scanning an item must not itself mark the order type as chosen")
	}
	s.SetOrderType(OrderTypeTakeaway)
	if !s.Basket().OrderTypeChosen {
		t.Fatal("SetOrderType(takeaway) must mark OrderTypeChosen=true")
	}
}

// Explicitly choosing dine-in (the default value) must still count as a
// real choice -- the cashier answered the prompt, even though the value
// they picked matches the zero value.
func TestOrderTypeChosen_ExplicitDineInCounts(t *testing.T) {
	s := newOrderTypeChosenService()
	s.SetOrderType("")
	if !s.Basket().OrderTypeChosen {
		t.Fatal("SetOrderType(\"\") (explicit dine-in) must mark OrderTypeChosen=true")
	}
}

func TestOrderTypeChosen_ResetOnNewSale(t *testing.T) {
	s := newOrderTypeChosenService()
	s.SetOrderType(OrderTypeTakeaway)
	if !s.Basket().OrderTypeChosen {
		t.Fatal("setup: expected OrderTypeChosen=true before reset")
	}
	s.Reset()
	if s.Basket().OrderTypeChosen {
		t.Fatal("Reset (new sale) must clear OrderTypeChosen")
	}
}

func TestOrderTypeChosen_ClearedAfterTender(t *testing.T) {
	s := newOrderTypeChosenService()
	_, _ = s.Scan("DRINK")
	s.SetOrderType(OrderTypeTakeaway)
	if _, err := s.Tender(money.FromMinor(1190), "cash"); err != nil {
		t.Fatalf("tender: %v", err)
	}
	if s.Basket().OrderTypeChosen {
		t.Fatal("Tender (sale complete, basket reset for the next customer) must clear OrderTypeChosen")
	}
}

// A resumed held sale must not re-prompt: the question was already settled
// for whatever lines it carries (or it was parked empty, in which case
// there is nothing to prompt about yet either way).
func TestOrderTypeChosen_TrueAfterResumingAHeldSale(t *testing.T) {
	s := newOrderTypeChosenService()
	_, _ = s.Scan("DRINK")
	s.SetOrderType(OrderTypeTakeaway)
	snap := s.Snapshot()

	s2 := newOrderTypeChosenService()
	if s2.Basket().OrderTypeChosen {
		t.Fatal("setup: a fresh second basket must start unchosen")
	}
	s2.RestoreHeld(snap, HeldOrigin{ID: "held-1"})
	if !s2.Basket().OrderTypeChosen {
		t.Fatal("resuming a held sale must mark OrderTypeChosen=true (no re-prompt)")
	}
}
