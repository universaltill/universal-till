package pages

// Kitchen Display, HDMI-local slice (universaltill/ut-docs#544): the print
// path treats a 'both' station like a 'printer' (it gets a ticket) and a
// 'display'-only station as non-printing (its lines fall through to the
// default bucket exactly as before this card).

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// A 'both' station prints its ticket exactly like a 'printer' station.
func TestPrintKitchen_BothStationGetsTicket(t *testing.T) {
	dp, dbase := kitchenRoutingDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dbase.DB)

	grillPrn := printerFile(t, "grill.prn")
	defaultPrn := printerFile(t, "kitchen.prn")
	if err := dp.Settings.Set(ctx, keyPrinterKitchen, defaultPrn); err != nil {
		t.Fatal(err)
	}
	grill, err := repo.CreateKitchenStation(ctx, "Grill", data.KitchenDestinationBoth, grillPrn)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetItemStationRoutes(ctx, "itm-steak", []string{grill}); err != nil {
		t.Fatal(err)
	}
	seedKitchenSale(t, dbase, "R-2000", "itm-steak")

	total, failures, err := printKitchen(ctx, dp, "R-2000", "")
	if err != nil {
		t.Fatalf("printKitchen: %v", err)
	}
	if len(failures) != 0 || total != 1 {
		t.Fatalf("want exactly 1 ticket (Grill) with no failures, got total=%d failures=%+v", total, failures)
	}
	grillOut, _ := os.ReadFile(grillPrn)
	defOut, _ := os.ReadFile(defaultPrn)
	if !bytes.Contains(grillOut, []byte("Steak")) {
		t.Fatal("a 'both' station must receive its printed ticket")
	}
	if len(defOut) != 0 {
		t.Fatalf("the default printer must receive nothing (line was routed), got %d bytes", len(defOut))
	}
}

// A 'display'-only station prints nothing: its lines fall through to the
// default bucket (never silently vanish), same as before this card.
func TestPrintKitchen_DisplayOnlyStationFallsThroughToDefault(t *testing.T) {
	dp, dbase := kitchenRoutingDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dbase.DB)

	defaultPrn := printerFile(t, "kitchen.prn")
	if err := dp.Settings.Set(ctx, keyPrinterKitchen, defaultPrn); err != nil {
		t.Fatal(err)
	}
	screen, err := repo.CreateKitchenStation(ctx, "Pass Screen", data.KitchenDestinationDisplay, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetItemStationRoutes(ctx, "itm-steak", []string{screen}); err != nil {
		t.Fatal(err)
	}
	seedKitchenSale(t, dbase, "R-2001", "itm-steak")

	targets, err := buildKitchenTargets(ctx, dp, "R-2001")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || !targets[0].isDefault {
		t.Fatalf("want exactly the default bucket, got %+v", targets)
	}

	total, failures, err := printKitchen(ctx, dp, "R-2001", "")
	if err != nil {
		t.Fatalf("printKitchen: %v", err)
	}
	if len(failures) != 0 || total != 1 {
		t.Fatalf("want 1 ticket (default) and no failures, got total=%d failures=%+v", total, failures)
	}
	defOut, _ := os.ReadFile(defaultPrn)
	if !bytes.Contains(defOut, []byte("Steak")) {
		t.Fatal("a display-only station's lines must still reach the default kitchen printer")
	}
}

// kitchenPrintingEnabled: a 'both' station with an address is a real print
// destination; a display-only station alone is not.
func TestKitchenPrintingEnabled_DestinationTypes(t *testing.T) {
	dp, dbase := kitchenRoutingDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dbase.DB)

	if kitchenPrintingEnabled(ctx, dp) {
		t.Fatal("no legacy printer and no stations must read as disabled")
	}
	if _, err := repo.CreateKitchenStation(ctx, "Pass Screen", data.KitchenDestinationDisplay, ""); err != nil {
		t.Fatal(err)
	}
	if kitchenPrintingEnabled(ctx, dp) {
		t.Fatal("a display-only station is not a print destination")
	}
	if _, err := repo.CreateKitchenStation(ctx, "Grill", data.KitchenDestinationBoth, printerFile(t, "grill.prn")); err != nil {
		t.Fatal(err)
	}
	if !kitchenPrintingEnabled(ctx, dp) {
		t.Fatal("a 'both' station with an address must enable kitchen printing")
	}
}
