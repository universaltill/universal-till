package pages

// ut-docs#3127: the "Remove background" offer in the item, category and
// variant photo pickers. Each page renders the offer panel (the shared
// web/ui/partials/photo_cutout.html, driven by web/public/photo-cutout.js)
// only when background removal is configured AND the viewer may save on
// that page (catalog_management) — otherwise the page renders exactly as
// before (docs repo architecture/ai-product-photo.md §6 "Gate").

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/catalog"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// The markers each page's offer panel carries: the panel is bound to the
// page's own file input by id (item, category) or by selector (variants).
const (
	itemCutoutMarker     = `data-cutout-input="image-file"`
	categoryCutoutMarker = `data-cutout-input="category-image-file"`
	variantCutoutMarker  = `data-cutout-inputs=".vg-img-upload input[type=file]"`
	cutoutScriptMarker   = `class="photo-cutout"`
)

// newCutoutOfferMux mounts the three pages on seeded Deps, wired the way
// Init wires them, with one item + one variant to render the panel for.
func newCutoutOfferMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	t.Setenv("UT_AI_IMAGE_ENDPOINT", "") // never pick up a developer's own rembg
	dp, db := newSeededPagesDeps(t)
	wireCutoutOffer(dp)
	mux := http.NewServeMux()
	catalog.Register(mux, dp)
	registerCategories(mux, dp)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "cut-itm", SKU: "CUT1", Name: "Mug", BasePrice: 500, TaxCodeID: "tax_std", IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "cut-var", ItemID: "cut-itm", SKU: "CUT1-L", Name: "Large", Price: 600, IsActive: true})
	return mux, dp
}

func cutoutGetAs(t *testing.T, mux http.Handler, path string, u *auth.User) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if u != nil {
		req = auth.WithUser(req, *u)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

var cutoutOfferPages = []struct {
	name, path, marker string
}{
	{"item form", "/catalog", itemCutoutMarker},
	{"category form", "/categories", categoryCutoutMarker},
	{"variants panel", "/api/catalog/item-variants?item_id=cut-itm", variantCutoutMarker},
}

func TestCutoutOffer_ShownWhenConfiguredAndAllowed(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newCutoutOfferMux(t)
	dp.AI, _ = fakeRembg(t, http.StatusOK, cutoutPNG(t))
	for _, p := range cutoutOfferPages {
		t.Run(p.name, func(t *testing.T) {
			rec := cutoutGetAs(t, mux, p.path, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, p.marker) {
				t.Fatalf("expected the Remove background offer (%s), got:\n%s", p.marker, body)
			}
			// Every string the module shows comes from the page, translated.
			for _, want := range []string{"Remove background", "Use original photo", "Use this", "Retake",
				`data-msg-unavailable="Background removal isn&#39;t available right now. You can use the photo as it is."`} {
				if !strings.Contains(body, want) {
					t.Errorf("offer panel missing %q", want)
				}
			}
		})
	}
}

// The variant photo input auto-submits on pick today. With the offer on,
// the pick must NOT upload before the operator has chosen (the module
// submits on Use this / Use original photo); with it off, unchanged.
func TestCutoutOffer_VariantInputWaitsForTheOfferOnlyWhenOn(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newCutoutOfferMux(t)
	const path = "/api/catalog/item-variants?item_id=cut-itm"

	off := cutoutGetAs(t, mux, path, nil).Body.String()
	if !strings.Contains(off, `data-change="request-submit"`) {
		t.Fatalf("AI off: the variant photo input must still upload on pick, got:\n%s", off)
	}

	dp.AI, _ = fakeRembg(t, http.StatusOK, cutoutPNG(t))
	on := cutoutGetAs(t, mux, path, nil).Body.String()
	upload := on[strings.Index(on, `class="vg-img-upload"`):]
	upload = upload[:strings.Index(upload, "</form>")]
	if strings.Contains(upload, `data-change="request-submit"`) {
		t.Fatalf("AI on: the variant photo input must wait for the offer, got:\n%s", upload)
	}
}

func TestCutoutOffer_AbsentWhenNotConfigured(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newCutoutOfferMux(t)
	for _, p := range cutoutOfferPages {
		t.Run(p.name, func(t *testing.T) {
			rec := cutoutGetAs(t, mux, p.path, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if strings.Contains(body, p.marker) || strings.Contains(body, cutoutScriptMarker) {
				t.Fatalf("AI off: expected no offer panel, got:\n%s", body)
			}
		})
	}
}

// A cashier lacks catalog_management: no page shows them the control (each
// page refuses them outright), and the helper every page asks says no even
// with the service configured. A manager gets it.
func TestCutoutOffer_CashierNeverSeesIt(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, dp := newCutoutOfferMux(t)
	dp.AuthSvc = auth.NewService(dp.Db)
	dp.AI, _ = fakeRembg(t, http.StatusOK, cutoutPNG(t))
	authRepo := data.NewAuthRepo(dp.Db)
	cashierID, err := authRepo.CreateUser(t.Context(), "offer-cashier", "Cashier", "cashier")
	if err != nil {
		t.Fatal(err)
	}
	managerID, err := authRepo.CreateUser(t.Context(), "offer-manager", "Manager", "manager")
	if err != nil {
		t.Fatal(err)
	}
	cashier := auth.User{ID: cashierID, Role: "cashier"}
	manager := auth.User{ID: managerID, Role: "manager"}

	for _, p := range cutoutOfferPages {
		t.Run(p.name, func(t *testing.T) {
			rec := cutoutGetAs(t, mux, p.path, &cashier)
			if strings.Contains(rec.Body.String(), p.marker) {
				t.Fatalf("cashier was shown the Remove background offer on %s", p.path)
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("cashier: expected 403 on %s, got %d", p.path, rec.Code)
			}
			rec = cutoutGetAs(t, mux, p.path, &manager)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), p.marker) {
				t.Fatalf("manager: expected the offer on %s, got %d", p.path, rec.Code)
			}
		})
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/catalog", nil), cashier)
	if aiCutoutOffered(req, dp) {
		t.Fatal("aiCutoutOffered: true for a cashier")
	}
	req = auth.WithUser(httptest.NewRequest(http.MethodGet, "/catalog", nil), manager)
	if !aiCutoutOffered(req, dp) {
		t.Fatal("aiCutoutOffered: false for a manager with the service configured")
	}
}
