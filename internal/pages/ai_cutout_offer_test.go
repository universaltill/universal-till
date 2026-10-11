package pages

// ut-docs#3127: the "Remove background" offer in the item, category and
// variant photo pickers. Each page renders the offer panel (the shared
// web/ui/partials/photo_cutout.html, driven by web/public/photo-cutout.js)
// whenever the viewer may save on that page (catalog_management); the
// engine is the shop's rembg service when configured, else the built-in
// on-device model (ut-docs#4137, ADR-0154).

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

// The variant photo input never uploads on pick while the offer is shown:
// the module submits on Use this / Use original photo. Since ut-docs#4137
// the offer is there whenever the viewer may edit the catalogue.
func TestCutoutOffer_VariantInputWaitsForTheOffer(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newCutoutOfferMux(t)
	on := cutoutGetAs(t, mux, "/api/catalog/item-variants?item_id=cut-itm", nil).Body.String()
	upload := on[strings.Index(on, `class="vg-img-upload"`):]
	upload = upload[:strings.Index(upload, "</form>")]
	if strings.Contains(upload, `data-change="request-submit"`) {
		t.Fatalf("the variant photo input must wait for the offer, got:\n%s", upload)
	}
}

// ut-docs#4137 (ADR-0154): with no rembg service configured, every picker
// still offers Remove background, on the built-in on-device engine; with
// the service configured, the panel names the service engine instead.
func TestCutoutOffer_EngineFollowsTheServiceConfig(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newCutoutOfferMux(t)
	for _, tc := range []struct {
		name, engine string
		configure    func()
	}{
		{"not configured → built-in", `data-cutout-engine="builtin"`, func() {}},
		{"rembg configured → service", `data-cutout-engine="service"`, func() { dp.AI, _ = fakeRembg(t, http.StatusOK, cutoutPNG(t)) }},
	} {
		tc.configure()
		for _, p := range cutoutOfferPages {
			t.Run(tc.name+"/"+p.name, func(t *testing.T) {
				rec := cutoutGetAs(t, mux, p.path, nil)
				if rec.Code != http.StatusOK {
					t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
				}
				body := rec.Body.String()
				if !strings.Contains(body, p.marker) || !strings.Contains(body, tc.engine) {
					t.Fatalf("expected the offer (%s) with %s, got:\n%s", p.marker, tc.engine, body)
				}
				// Only the built-in panel points at the vendored model, by a
				// versioned same-origin URL (never a CDN, ADR-0003).
				const model = `data-cutout-model="/public/vendor/bg-removal/u2netp.onnx?v=`
				if builtin := strings.Contains(tc.engine, "builtin"); strings.Contains(body, model) != builtin {
					t.Fatalf("built-in=%v but model URL present=%v", builtin, !builtin)
				}
			})
		}
	}
}

// A setting the till can't run (a model off the licence-checked allow-list)
// turns the rembg path off; the built-in engine takes over and nothing is
// sent anywhere (ADR-0154 Decision 2).
func TestCutoutOffer_InvalidServiceSettingFallsBackToBuiltIn(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	t.Setenv("UT_AI_IMAGE_PROVIDER", "self_hosted")
	t.Setenv("UT_AI_IMAGE_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("UT_AI_IMAGE_MODEL", "bria-rmbg")
	dp, _ := newSeededPagesDeps(t)
	req := httptest.NewRequest(http.MethodGet, "/catalog", nil)
	if got := aiCutoutOffered(req, dp); got != cutoutEngineBuiltin {
		t.Fatalf("aiCutoutOffered = %q, want %q", got, cutoutEngineBuiltin)
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
	if got := aiCutoutOffered(req, dp); got != "" {
		t.Fatalf("aiCutoutOffered: %q for a cashier, want none", got)
	}
	req = auth.WithUser(httptest.NewRequest(http.MethodGet, "/catalog", nil), manager)
	if got := aiCutoutOffered(req, dp); got != cutoutEngineService {
		t.Fatalf("aiCutoutOffered: %q for a manager with the service configured, want %q", got, cutoutEngineService)
	}
}
