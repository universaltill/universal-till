package data

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ut-docs#6755: the external (own-terminal) reader row.

func TestExternalReader_CreateGeneratesRef(t *testing.T) {
	db := newCardReaderTestDB(t)
	repo := NewCardReaderRepo(db)
	c, err := repo.Create(context.Background(), CardReader{Name: "Counter terminal", MethodKey: PaymentMethodCardExternal, Kind: CardReaderKindExternal})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(c.ReaderRef, "ext-") || len(c.ReaderRef) != 20 || !ValidCardReaderRef(c.ReaderRef) {
		t.Fatalf("ref = %q", c.ReaderRef)
	}
	if c.Transport != CardReaderTransportNone || c.PluginID != "" || c.Kind != CardReaderKindExternal {
		t.Fatalf("row = %+v", c)
	}
}

func TestExternalReader_MethodKindPairing(t *testing.T) {
	db := newCardReaderTestDB(t)
	repo := NewCardReaderRepo(db)
	ctx := context.Background()
	if _, err := repo.Create(ctx, CardReader{Name: "A", MethodKey: "card", Kind: CardReaderKindExternal}); !errors.Is(err, ErrCardReaderRef) {
		t.Fatalf("external+card = %v, want ErrCardReaderRef", err)
	}
	if _, err := repo.Create(ctx, CardReader{Name: "B", MethodKey: PaymentMethodCardExternal, PluginID: "p", ReaderRef: "r1", Kind: CardReaderKindCloud}); !errors.Is(err, ErrCardReaderRef) {
		t.Fatalf("cloud+card_external = %v, want ErrCardReaderRef", err)
	}
	// regression: a cloud reader still needs a plugin.
	if _, err := repo.Create(ctx, CardReader{Name: "C", MethodKey: "sumup", ReaderRef: "r2", Kind: CardReaderKindCloud}); !errors.Is(err, ErrCardReaderRef) {
		t.Fatalf("cloud w/o plugin = %v, want ErrCardReaderRef", err)
	}
}

func TestExternalReader_NoUnattendedDefault(t *testing.T) {
	db := newCardReaderTestDB(t)
	repo := NewCardReaderRepo(db)
	ctx := context.Background()
	c, err := repo.Create(ctx, CardReader{Name: "T", MethodKey: PaymentMethodCardExternal, Kind: CardReaderKindExternal})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetUnattendedDefault(ctx, c.ID, true); err == nil {
		t.Fatal("unattended default on external must be refused")
	}
}
