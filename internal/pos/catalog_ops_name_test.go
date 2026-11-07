package pos

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#5858: the admin catalogue wrappers refuse control/invisible
// characters in item and variant names; data.CreateItem (directives) stays
// unchecked, see internal/data/singleline_name_paths_test.go.
func TestAdminItemAndVariantNamesRefuseControlChars(t *testing.T) {
	ctx := context.Background()
	db := setupCatalogDB(t)
	defer db.Close()

	for label, bad := range map[string]string{"esc": "Co\x1bla", "rlo": "Co\u202ela"} {
		t.Run(label, func(t *testing.T) {
			if _, err := CreateItem(ctx, db, ItemInput{ID: "bad", SKU: "BAD", Name: bad, BasePrice: 100, IsActive: true}); !errors.Is(err, data.ErrNameInvalidCharacters) {
				t.Fatalf("CreateItem err = %v", err)
			}
		})
	}
	const rtl = "\u200f שם \u2067x\u2069 "
	id, err := CreateItem(ctx, db, ItemInput{ID: "ok", SKU: "OK", Name: rtl, BasePrice: 100, IsActive: true})
	if err != nil {
		t.Fatalf("CreateItem RTL name: %v", err)
	}
	for label, bad := range map[string]string{"esc": "Co\x1bla", "rlo": "Co\u202ela"} {
		t.Run("update "+label, func(t *testing.T) {
			if _, err := UpdateItemReturningWasActive(ctx, db, ItemInput{ID: id, SKU: "OK", Name: bad, BasePrice: 100, IsActive: true}); !errors.Is(err, data.ErrNameInvalidCharacters) {
				t.Fatalf("update err = %v", err)
			}
		})
	}
	it, found, err := data.NewCatalogRepo(db).GetItem(ctx, id)
	if err != nil || !found || it.Name != "\u200f שם \u2067x\u2069" {
		t.Fatalf("stored item = %+v, %v", it, err)
	}

	for label, bad := range map[string]string{"esc": "5\x1b0ml", "rlo": "5\u202e0ml"} {
		t.Run("variant "+label, func(t *testing.T) {
			if _, err := CreateVariant(ctx, db, VariantInput{ID: "vbad", ItemID: id, Name: bad, Price: 100, IsActive: true}); !errors.Is(err, data.ErrNameInvalidCharacters) {
				t.Fatalf("CreateVariant err = %v", err)
			}
		})
	}
	vid, err := CreateVariant(ctx, db, VariantInput{ID: "vok", ItemID: id, Name: "500ml", Price: 100, IsActive: true})
	if err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}
	if err := UpdateVariant(ctx, db, VariantInput{ID: vid, ItemID: id, Name: "5\x1b0ml", Price: 100, IsActive: true}); !errors.Is(err, data.ErrNameInvalidCharacters) {
		t.Fatalf("UpdateVariant err = %v", err)
	}
}
