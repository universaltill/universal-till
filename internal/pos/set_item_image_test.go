package pos

import "testing"

// ut-docs#4838: a photo set while a sale is open must reach the open
// basket's rows, including through the per-sale scan cache.
func TestSetItemImage_RefreshesOpenSale_4838(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{
		"LAGER": {SKU: "LAGER", Name: "Lager", Qty: 1, PriceCents: 450, ItemID: "itm-lager", TaxCodeID: "tax_std"},
		"BREAD": {SKU: "BREAD", Name: "Bread", Qty: 1, PriceCents: 100, ItemID: "itm-bread", TaxCodeID: "tax_std"},
	})
	s.Scan("LAGER")
	s.Scan("BREAD")

	s.SetItemImage("itm-lager", "/x.png")
	b := s.Basket()
	if b.Lines[0].ImageURL != "/x.png" || s.Lines()[0].ImageURL != "/x.png" {
		t.Fatalf("open line did not get the photo: basket=%q lines=%q", b.Lines[0].ImageURL, s.Lines()[0].ImageURL)
	}
	if b.Lines[1].ImageURL != "" {
		t.Fatalf("other item's line was touched: %q", b.Lines[1].ImageURL)
	}

	// Repeat scan hits the scan cache; the merged line keeps the photo.
	s.Scan("LAGER")
	b = s.Basket()
	if len(b.Lines) != 2 || b.Lines[0].Qty != 2 || b.Lines[0].ImageURL != "/x.png" {
		t.Fatalf("after cached rescan: %+v", b.Lines[0])
	}

	s.SetItemImage("itm-lager", "")
	if got := s.Basket().Lines[0].ImageURL; got != "" {
		t.Fatalf("clear left %q", got)
	}
	s.Scan("LAGER")
	if got := s.Basket().Lines[0].ImageURL; got != "" {
		t.Fatalf("cache kept removed photo: %q", got)
	}
	s.SetItemImage("", "/y.png") // no-op
}

// ut-docs#4838 review: the cached line carries the photo from first ring-up,
// so a photo removed mid-sale must also leave the scan cache — otherwise a
// repeat tap merges the old photo back onto the row.
func TestSetItemImage_ClearsCachedPhoto_4838(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{
		"LAGER": {SKU: "LAGER", Name: "Lager", Qty: 1, PriceCents: 450, ItemID: "itm-lager", TaxCodeID: "tax_std", ImageURL: "/old.png"},
	})
	s.Scan("LAGER")
	s.SetItemImage("itm-lager", "")
	s.Scan("LAGER")
	if got := s.Basket().Lines[0]; got.Qty != 2 || got.ImageURL != "" {
		t.Fatalf("rescan after clear: qty=%v image=%q, want qty 2 and no photo", got.Qty, got.ImageURL)
	}

	s.SetItemImage("itm-lager", "/new.png")
	s.Scan("LAGER")
	if got := s.Basket().Lines[0].ImageURL; got != "/new.png" {
		t.Fatalf("rescan after replace: %q, want /new.png", got)
	}
}
