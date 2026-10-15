package common

import (
	"context"
	"fmt"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#4343: an inactive-variant refusal gets its own message, not the
// generic "barcode attach failed". The repo is never touched on this path.
func TestFriendlyBarcodeConflict_VariantInactive(t *testing.T) {
	httpx.InitI18n(realI18n(t), "en")
	t.Cleanup(func() { httpx.InitI18n(nil, "en") })

	got := FriendlyBarcodeConflict(context.Background(), nil, "en",
		fmt.Errorf("x: %w", fmt.Errorf("%w: v1", data.ErrVariantInactive)))
	want := "This variant is switched off. Tick Active for it and save, then add the barcode."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
