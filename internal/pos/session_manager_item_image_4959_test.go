package pos

import "testing"

// ut-docs#4959: SetItemImage reaches every live session's lines of the item,
// leaves other items alone, and is nil-receiver-safe like the manager's other
// methods (bare-Deps handlers call it unguarded).
func TestSessionBasketManager_SetItemImageReachesEveryLiveSession_4959(t *testing.T) {
	m, _ := newTestSessionManager(t)
	_, a, _ := m.Create()
	_, b, _ := m.Create()
	a.AddLineWithModifiers(BasketLine{SKU: "A", Name: "Coffee", ItemID: "ia", PriceCents: 300}, 1, nil)
	b.AddLineWithModifiers(BasketLine{SKU: "A", Name: "Coffee", ItemID: "ia", PriceCents: 300}, 1, nil)
	b.AddLineWithModifiers(BasketLine{SKU: "T", Name: "Tea", ItemID: "it", PriceCents: 250, ImageURL: "/tea.png"}, 1, nil)

	m.SetItemImage("ia", "/coffee.png")

	if got := a.Basket().Lines[0].ImageURL; got != "/coffee.png" {
		t.Errorf("session a coffee ImageURL = %q, want /coffee.png", got)
	}
	for _, l := range b.Basket().Lines {
		want := map[string]string{"ia": "/coffee.png", "it": "/tea.png"}[l.ItemID]
		if l.ImageURL != want {
			t.Errorf("session b %s ImageURL = %q, want %q", l.ItemID, l.ImageURL, want)
		}
	}

	var nilMgr *SessionBasketManager
	nilMgr.SetItemImage("ia", "/x.png") // must not panic
}
