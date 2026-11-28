package pages

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/shoptemplate"
)

// ut-docs#6332 S8: a shop that predates templates (shop.type set, no ledger)
// is offered the tidy-up once on the Settings landing. The card never applies
// anything: "See what changes" opens the S7 preview. "Not now" hides it until
// the build version changes; "Never ask again" hides it for good.

const offerCard = `data-testid="template-offer"`

func offerVersion(t *testing.T, v string) {
	t.Helper()
	old := templateOfferVersion
	templateOfferVersion = func() string { return v }
	t.Cleanup(func() { templateOfferVersion = old })
}

// legacyShop returns a till whose shop.type is retail with no ledger.
func legacyShop(t *testing.T, typ string) (*http.ServeMux, *common.Deps) {
	t.Helper()
	offerVersion(t, "1.0.0")
	mux, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	stSet(t, d, common.KeyShopType, typ)
	d.ReloadTemplate(t.Context())
	return mux, d
}

func TestTemplateOffer_LegacyManagerSeesCardWithThreeActions(t *testing.T) {
	mux, _ := legacyShop(t, "retail")
	body := getAs(mux, "/settings", &mgrUser).Body.String()
	for _, want := range []string{
		offerCard, "Tidy your menu for your shop type?", "Your shop type: Retail shop",
		`hx-get="/api/settings/shop-type/preview?to=retail"`, "See what changes",
		`hx-post="/api/settings/shop-type/offer/not-now"`, "Not now",
		`hx-post="/api/settings/shop-type/offer/never"`, "Never ask again",
		"Tidy my menu for Retail shop", // the Shop type card keeps the way in
	} {
		if !strings.Contains(body, want) {
			t.Errorf("legacy settings lacks %q", want)
		}
	}
	if strings.Contains(body, `aria-labelledby="template-offer-title"`) == false {
		t.Error("the card is a labelled section")
	}
	if strings.Contains(body, `role="dialog"`) && strings.Contains(body, `template-offer-title" role="dialog"`) {
		t.Error("never a dialog")
	}
}

func TestTemplateOffer_NotOnMenuOrForOtherStates(t *testing.T) {
	mux, d := legacyShop(t, "retail")
	if body := getPage(t, mux, "/menu").Body.String(); strings.Contains(body, offerCard) {
		t.Error("the offer must never show on the menu launcher")
	}
	if body := getAs(mux, "/settings", &cashUser).Body.String(); strings.Contains(body, offerCard) {
		t.Error("a cashier never sees the offer")
	}
	// Satellite: no card.
	stSet(t, d, "sync.primary_url", "http://192.0.2.10:8080")
	if body := getAs(mux, "/settings", &mgrUser).Body.String(); strings.Contains(body, offerCard) || strings.Contains(body, "Tidy my menu for") {
		t.Error("a satellite gets no offer and no tidy button")
	}
}

func TestTemplateOffer_NoTypeActiveAndSalonLegacyGetNoCard(t *testing.T) {
	mux, d := legacyShop(t, "retail")
	stSet(t, d, common.KeyShopType, "")
	if body := getAs(mux, "/settings", &mgrUser).Body.String(); strings.Contains(body, offerCard) {
		t.Error("no shop type: no offer")
	}
	stSet(t, d, common.KeyShopType, "service") // legacy service shop keeps the salon layout
	d.ReloadTemplate(t.Context())
	if body := getAs(mux, "/settings", &mgrUser).Body.String(); strings.Contains(body, offerCard) {
		t.Error("a legacy service shop (salon layout) gets no offer card")
	}
	stChange(t, mux, "retail") // accepted: active
	if body := getAs(mux, "/settings", &mgrUser).Body.String(); strings.Contains(body, offerCard) || strings.Contains(body, "Tidy my menu for") {
		t.Error("an active shop has no offer and no tidy button")
	}
}

func TestTemplateOffer_CardNeverWrites(t *testing.T) {
	mux, d := legacyShop(t, "retail")
	getAs(mux, "/settings", &mgrUser)
	rec := getAs(mux, "/api/settings/shop-type/preview?to=retail", &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview from the card: %d", rec.Code)
	}
	if got := stGet(t, d, shoptemplate.LedgerKey); got != "" {
		t.Fatalf("seeing the offer or its preview wrote a ledger: %q", got)
	}
}

func TestTemplateOffer_AcceptThroughConfirmEndsLegacyAndOffer(t *testing.T) {
	mux, d := legacyShop(t, "retail")
	stChange(t, mux, "retail") // the preview's confirm, same type
	if !strings.Contains(stGet(t, d, shoptemplate.LedgerKey), `"id":"retail"`) {
		t.Fatalf("accepting must write the retail ledger, got %q", stGet(t, d, shoptemplate.LedgerKey))
	}
	if got := stGet(t, d, "sale.order_type_prompt"); got != "" {
		t.Fatalf("accepting with nothing ticked wrote a value: %q", got)
	}
}

func TestTemplateOffer_NotNowHidesUntilVersionChanges(t *testing.T) {
	mux, d := legacyShop(t, "retail")
	rec := postForm(mux, "/api/settings/shop-type/offer/not-now", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("not-now: %d %q, want 200 empty (card swapped away)", rec.Code, rec.Body.String())
	}
	if got := stGet(t, d, templateOfferKey); got != `{"dismissed_until_version":"1.0.0"}` {
		t.Fatalf("stored %q, want dismissed_until_version 1.0.0", got)
	}
	if body := getAs(mux, "/settings", &mgrUser).Body.String(); strings.Contains(body, offerCard) {
		t.Error("same version: card must stay hidden")
	}
	if body := getAs(mux, "/settings", &mgrUser).Body.String(); !strings.Contains(body, "Tidy my menu for Retail shop") {
		t.Error("the Shop type card keeps its way in after Not now")
	}
	offerVersion(t, "1.1.0")
	if body := getAs(mux, "/settings", &mgrUser).Body.String(); !strings.Contains(body, offerCard) {
		t.Error("a new build version must bring the card back")
	}
}

func TestTemplateOffer_NeverAskAgainSuppressesAcrossVersions(t *testing.T) {
	mux, d := legacyShop(t, "retail")
	if rec := postForm(mux, "/api/settings/shop-type/offer/never", url.Values{}, &mgrUser); rec.Code != http.StatusOK {
		t.Fatalf("never: %d", rec.Code)
	}
	if got := stGet(t, d, templateOfferKey); got != `{"never":true}` {
		t.Fatalf("stored %q", got)
	}
	offerVersion(t, "9.9.9")
	if body := getAs(mux, "/settings", &mgrUser).Body.String(); strings.Contains(body, offerCard) {
		t.Error("never ask again must survive a version change")
	}
}

func TestTemplateOffer_EndpointsRefuseCashierAndSatellite(t *testing.T) {
	mux, d := legacyShop(t, "retail")
	for _, p := range []string{"not-now", "never"} {
		if rec := postForm(mux, "/api/settings/shop-type/offer/"+p, url.Values{}, &cashUser); rec.Code != http.StatusForbidden {
			t.Errorf("cashier %s = %d, want 403", p, rec.Code)
		}
	}
	if got := stGet(t, d, templateOfferKey); got != "" {
		t.Fatalf("a refused cashier request wrote %q", got)
	}
	stSet(t, d, "sync.primary_url", "http://192.0.2.10:8080")
	if rec := postForm(mux, "/api/settings/shop-type/offer/never", url.Values{}, &mgrUser); rec.Code != http.StatusConflict {
		t.Errorf("satellite = %d, want 409", rec.Code)
	}
}

func TestTemplateOffer_UnreadableStateShowsTheCard(t *testing.T) {
	mux, d := legacyShop(t, "retail")
	stSet(t, d, templateOfferKey, "{not json")
	if body := getAs(mux, "/settings", &mgrUser).Body.String(); !strings.Contains(body, offerCard) {
		t.Error("a damaged record must fail open to the offer, not hide it forever")
	}
}

func TestTemplateOffer_DismissShowsWordAndIcon(t *testing.T) {
	mux, _ := legacyShop(t, "retail")
	body := getAs(mux, "/settings", &mgrUser).Body.String()
	if !strings.Contains(body, `<span aria-hidden="true">×</span> Not now`) {
		t.Error("Not now shows the word with its icon, never an icon alone")
	}
}
