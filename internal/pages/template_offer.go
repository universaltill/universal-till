package pages

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Existing-shop offer (ADR-0190, ut-docs#6332 S8). A shop that predates
// templates (shop.type set, no ledger: TemplateLegacy) keeps today's layout
// exactly; its manager gets one quiet card on the Settings landing leading
// to the S7 preview. The card never applies anything. "Not now" hides it
// until the build version changes, "Never ask again" for good; both are
// shop-wide and the Shop type card keeps a "Tidy my menu" button either way.
// A legacy service shop (the old salon layout) gets no card: its layout is
// replayed unchanged until its manager chooses to tidy from the Shop type card.

// templateOfferKey stores {dismissed_until_version, never} shop-wide.
const templateOfferKey = "shop.template_offer"

// templateOfferVersion is the running build version; a seam so tests can
// change it ("Not now" lasts until this differs from the stored one).
var templateOfferVersion = func() string { return buildinfo.Version }

type templateOfferState struct {
	DismissedUntilVersion string `json:"dismissed_until_version,omitempty"`
	Never                 bool   `json:"never,omitempty"`
}

// templateOfferView is the card's input.
type templateOfferView struct {
	Type      string // shop type id
	TypeLabel string
	TypeLine  string
}

// templateTidyView is the legacy shop's "Tidy my menu for {type}" button on
// the Shop type card (nil outside legacy mode or on a satellite).
type templateTidyView struct{ Type, Text string }

// templateLegacyType is the shop type when the shop is in legacy mode on a
// till that may change it, else "".
func templateLegacyType(r *http.Request, d *common.Deps) string {
	if d == nil || d.Settings == nil || !canPerform(d, r, "settings") || tillFollowsMain(r.Context(), d) {
		return ""
	}
	st := d.TemplateState(r.Context())
	if st.Mode != common.TemplateLegacy || st.Resolved == nil {
		return ""
	}
	return st.ShopType
}

// templateTidyFor is the Shop type card's button while the shop is legacy.
func templateTidyFor(r *http.Request, d *common.Deps, locale string) *templateTidyView {
	typ := templateLegacyType(r, d)
	if typ == "" {
		return nil
	}
	return &templateTidyView{Type: typ, Text: strings.ReplaceAll(httpx.T(locale, "settings.template.offer.tidy_for_type"), "{type}", shopTypeLabel(locale, typ))}
}

// templateOfferFor returns the card for this request, or nil.
func templateOfferFor(r *http.Request, d *common.Deps, locale string) *templateOfferView {
	typ := templateLegacyType(r, d)
	if typ == "" {
		return nil
	}
	if d.TemplateState(r.Context()).Resolved.HasLegacy() {
		return nil // legacy salon layout: kept as is, no card
	}
	raw, _, err := d.Settings.Get(r.Context(), templateOfferKey)
	if err != nil {
		return nil
	}
	var s templateOfferState
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			s = templateOfferState{} // damaged record: fail open to the offer, it is reversible and quiet
		}
	}
	if s.Never || (s.DismissedUntilVersion != "" && s.DismissedUntilVersion == templateOfferVersion()) {
		return nil
	}
	label := shopTypeLabel(locale, typ)
	return &templateOfferView{Type: typ, TypeLabel: label, TypeLine: strings.ReplaceAll(httpx.T(locale, "settings.template.offer.type_line"), "{type}", label)}
}

func registerTemplateOffer(mux *http.ServeMux, d *common.Deps) {
	save := func(w http.ResponseWriter, r *http.Request, s templateOfferState) {
		if !requirePage(d, w, r, "settings") || !shopTypeMainOnly(w, r, d) {
			return
		}
		b, _ := json.Marshal(s)
		if err := d.Settings.Set(r.Context(), templateOfferKey, string(b)); err != nil { // settings-write:allow existing-shop offer dismissal (S8), main/single till only, gated above, shop-wide
			logging.L().Errorf("template offer: save: %v", err)
			shopTypeFragmentError(w, http.StatusInternalServerError, httpx.T(httpx.RequestLocale(r), "settings.template.offer.error"))
			return
		}
		// The buttons target the card's message line so a failure leaves the
		// card in place; success retargets to the whole card and empties it.
		w.Header().Set("HX-Retarget", "#template-offer")
		w.Header().Set("HX-Reswap", "outerHTML")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK) // empty body: htmx swaps the card away
	}
	mux.HandleFunc("POST /api/settings/shop-type/offer/not-now", func(w http.ResponseWriter, r *http.Request) {
		save(w, r, templateOfferState{DismissedUntilVersion: templateOfferVersion()})
	})
	mux.HandleFunc("POST /api/settings/shop-type/offer/never", func(w http.ResponseWriter, r *http.Request) {
		save(w, r, templateOfferState{Never: true})
	})
}
