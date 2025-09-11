package pos

import (
	"reflect"
	"testing"
)

func newTestService() *Service {
	prices := map[string]BasketLine{
		"A": {SKU: "A", Name: "Coffee", Qty: 1, PriceCents: 250},
		"B": {SKU: "B", Name: "Tea", Qty: 1, PriceCents: 200},
		"C": {SKU: "C", Name: "Cake", Qty: 1, PriceCents: 350},
	}
	return NewServiceWithResolver(Config{}, mapResolver(prices))
}

func TestScanKnownItemsUpdatesTotalsAndTax(t *testing.T) {
	svc := newTestService()
	if _, err := svc.Scan("A"); err != nil {
		t.Fatalf("scan A: %v", err)
	}
	b, err := svc.Scan("B")
	if err != nil {
		t.Fatalf("scan B: %v", err)
	}
	if len(b.Lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(b.Lines))
	}
	if b.Subtotal != 450 {
		t.Errorf("expected subtotal 450, got %d", b.Subtotal)
	}
	if b.Tax != 90 {
		t.Errorf("expected tax 90, got %d", b.Tax)
	}
	if b.Total != 540 {
		t.Errorf("expected total 540, got %d", b.Total)
	}
}

func TestScanQtyIncrementsExistingLine(t *testing.T) {
	svc := newTestService()
	if _, err := svc.Scan("A"); err != nil {
		t.Fatalf("scan A: %v", err)
	}
	b, err := svc.ScanQty("A", 2)
	if err != nil {
		t.Fatalf("scan qty: %v", err)
	}
	if len(b.Lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(b.Lines))
	}
	if b.Lines[0].Qty != 3 {
		t.Errorf("expected qty 3, got %d", b.Lines[0].Qty)
	}
	if b.Subtotal != 750 {
		t.Errorf("expected subtotal 750, got %d", b.Subtotal)
	}
	if b.Tax != 150 {
		t.Errorf("expected tax 150, got %d", b.Tax)
	}
	if b.Total != 900 {
		t.Errorf("expected total 900, got %d", b.Total)
	}
}

func TestUnknownSKULeavesBasketUnchanged(t *testing.T) {
	svc := newTestService()
	b1, err := svc.Scan("A")
	if err != nil {
		t.Fatalf("scan A: %v", err)
	}
	expected := *b1
	b2, err := svc.Scan("Z")
	if err != nil {
		t.Fatalf("scan unknown: %v", err)
	}
	if !reflect.DeepEqual(expected, *b2) {
		t.Errorf("basket changed: expected %+v got %+v", expected, *b2)
	}
}
