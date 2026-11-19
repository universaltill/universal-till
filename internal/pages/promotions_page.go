package pages

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// redirectPromotions answers a promotions mutation's SUCCESS with a
// navigation back to GET /promotions. Mirrors locations_page.go's
// redirectLocations (ut-docs#3661 adopting ut-docs#2010's pattern): the
// dialog's forms are hx-boosted, so a bare 303 would be followed by the
// boosted form's own fetch and land the page's HTML in the message region;
// HX-Redirect forces a real navigation. A non-htmx caller gets the plain 303.
func redirectPromotions(w http.ResponseWriter, r *http.Request, target string) {
	if isHtmxDialogRequest(r) {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// renderPromotionsDialogError answers a REFUSED promotions mutation: a 400
// text/html message fragment for the dialog's aria-live region (htmx), or the
// pre-#3661 redirect-with-?err= for a non-htmx caller. See
// renderLocationsDialogError for the full reasoning.
func renderPromotionsDialogError(w http.ResponseWriter, r *http.Request, errKey string) {
	if !isHtmxDialogRequest(r) {
		redirectPromotions(w, r, "/promotions?err="+errKey)
		return
	}
	msg := httpx.T(httpx.RequestLocale(r), errKey)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	httpx.RenderPartial("ui/partials/record_dialog_msg.html", map[string]any{"msg": msg})(w, r)
}

// registerPromotions wires the promo-code admin page (ut-docs#634): create/
// edit/deactivate/list promo codes (the promotions table itself already has
// real read/redeem logic via POSRepo.FindActivePromo, used by
// /api/pos/scan's discount-code path -- this only adds management around
// that, and never touches FindActivePromo or the scan handler).
// Manager/admin only, same requireManager shape as locations_page.go and
// users_page.go. The promo code itself is the PRIMARY KEY and is immutable
// once created -- edit never rewrites it; deactivate/reactivate is a soft
// is_active toggle, never a hard delete, so redemption history survives.
func registerPromotions(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)

	// requireManager gates on the "settings" action (039's catalog) via
	// canPerform — see country_settings_page.go's identical
	// requireManager for why (ut-docs#901/#902): the old raw
	// IsManager() check never saw canPerform's UT_AUTH=off escape hatch,
	// so this page 403'd permanently under the dev/CI auth-bypass. No
	// change to gated (UT_AUTH on) behavior.
	requireManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "settings") {
			httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	audit := func(r *http.Request, actorID, code, action string, payload any) {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "promotion", code, action, payload, now, "")
	}

	renderPromotions := func(w http.ResponseWriter, r *http.Request, errKey string) {
		list, err := posRepo.ListPromotionsForAdmin(r.Context())
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		locale := httpx.RequestLocale(r)
		views := make([]promotionView, 0, len(list))
		for _, p := range list {
			views = append(views, newPromotionView(p, locale))
		}
		httpx.Render("ui/pages/promotions.html", map[string]any{
			"title":      httpx.T(locale, "page.title.promotions"),
			"theme":      d.ResolvedTheme(r),
			"menuItems":  d.MenuSnapshot(),
			"promotions": views,
			"errKey":     errKey,
		})(w, r)
	}

	// parsePromotionForm reads type/value/description/dates/customer from an
	// already-ParseForm'd request. The value field's name depends on type
	// ("value_amount" is entered in the shop's display currency and
	// converted to minor units here -- same money-boundary-conversion
	// convention as catalog price entry; "value_percent" is entered as a
	// plain percentage and converted to basis points, 1% = 100, matching
	// DISC10's seed row and FindActivePromo's own interpretation in
	// pos_api.go, which this handler never touches).
	parsePromotionForm := func(r *http.Request) (data.PromotionInput, string, bool) {
		typ := strings.TrimSpace(r.PostFormValue("type"))
		var value int64
		switch typ {
		case "amount":
			// ut-docs#2925: ParseMoneyMajor, not strconv.ParseFloat -- the
			// field accepts a decimal comma ("3,50" from a German keyboard)
			// and "1e3"/"0x10" are refused. Currency.Decimals-aware
			// (ut-docs#1400: a hardcoded *100 stored a 100x-too-large value
			// on a 0-decimal shop).
			minor, err := httpx.ParseMoneyMajor(r.PostFormValue("value_amount"), httpx.ActiveCurrency().Decimals)
			if err != nil || minor <= 0 {
				return data.PromotionInput{}, "promotions.error.value_invalid", false
			}
			value = money.FromMinor(minor).Minor()
		case "percent":
			// ut-docs#2954: ParsePercentBP, not strconv.ParseFloat -- a
			// German "1,5" is accepted and "1e3"/"NaN" are refused.
			bp, err := httpx.ParsePercentBP(r.PostFormValue("value_percent"))
			// A percent discount over 100% is never a real promotion: the
			// engine clamps the basket total at zero, so 500% and 100% are
			// indistinguishable at the till while the stored value lies
			// about what the shop intended. Same 0 < pct <= 100 range
			// settings_page.go's payment-fee percent already enforces.
			if err != nil || bp <= 0 || bp > 10000 {
				return data.PromotionInput{}, "promotions.error.value_invalid", false
			}
			value = bp
		default:
			return data.PromotionInput{}, "promotions.error.value_invalid", false
		}
		startsAt, ok := parsePromoDate(r.PostFormValue("starts_at"))
		if !ok {
			return data.PromotionInput{}, "promotions.error.dates_invalid", false
		}
		endsAt, ok := parsePromoDate(r.PostFormValue("ends_at"))
		if !ok {
			return data.PromotionInput{}, "promotions.error.dates_invalid", false
		}
		if startsAt != "" && endsAt != "" && endsAt < startsAt {
			return data.PromotionInput{}, "promotions.error.dates_invalid", false
		}
		// ut-docs#5574: an absent customer_id means "unchanged"; a present
		// empty value is an explicit clear; an id is validated in the repo.
		_, present := r.PostForm["customer_id"]
		return data.PromotionInput{
			KeepCustomer: !present,
			Type:         typ,
			Value:        value,
			Description:  strings.TrimSpace(r.PostFormValue("description")),
			StartsAt:     startsAt,
			EndsAt:       endsAtInclusive(endsAt),
			CustomerID:   strings.TrimSpace(r.PostFormValue("customer_id")),
		}, "", true
	}

	mux.HandleFunc("GET /promotions", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireManager(w, r); !ok {
			return
		}
		renderPromotions(w, r, httpx.QueryErrKey(r))
	})

	// Customer lookup for the promotion dialog's "only for this customer"
	// picker (ut-docs#5589). Gated on "settings" like the rest of Promotions,
	// NOT "data_management": GET /api/data/customers is the GDPR erasure
	// picker (carries email, data_management) and stays as it is. This one
	// returns id/name/phone only. JSON, not requireManager's HTML error page,
	// because the dialog's script does r.json().
	mux.HandleFunc("GET /api/promotions/customers", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.RequestLocale(r)
		if !canPerform(d, r, "settings") {
			dataAPIRespond(w, http.StatusForbidden, false, httpx.T(locale, "common.error.manager_or_admin_required"))
			return
		}
		list, err := posRepo.SearchCustomersForPicker(r.Context(), r.URL.Query().Get("q"), 25)
		if err != nil {
			logging.L().Errorf("promotions: customer picker: %v", err)
			dataAPIRespond(w, http.StatusInternalServerError, false, httpx.T(locale, "common.error.server"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"customers": list}, "error": nil})
	})

	mux.HandleFunc("POST /api/promotions", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		_ = r.ParseForm()
		code := strings.TrimSpace(r.PostFormValue("code"))
		if code == "" {
			renderPromotionsDialogError(w, r, "promotions.error.required")
			return
		}
		in, errKey, ok := parsePromotionForm(r)
		if !ok {
			renderPromotionsDialogError(w, r, errKey)
			return
		}
		if err := posRepo.CreatePromotion(r.Context(), code, in); err != nil {
			if errors.Is(err, data.ErrPromotionCodeExists) {
				renderPromotionsDialogError(w, r, "promotions.error.code_exists")
				return
			}
			if errors.Is(err, data.ErrPromotionCustomerUnknown) {
				renderPromotionsDialogError(w, r, "promotions.error.customer_unknown")
				return
			}
			renderPromotionsDialogError(w, r, "promotions.error.create")
			return
		}
		audit(r, actor.ID, code, "promotion_create", nil)
		redirectPromotions(w, r, "/promotions")
	})

	mux.HandleFunc("POST /api/promotions/{code}/edit", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		code := r.PathValue("code")
		_ = r.ParseForm()
		in, errKey, ok := parsePromotionForm(r)
		if !ok {
			renderPromotionsDialogError(w, r, errKey)
			return
		}
		changes, err := posRepo.UpdatePromotion(r.Context(), code, in)
		if err != nil {
			if errors.Is(err, data.ErrPromotionCustomerUnknown) {
				renderPromotionsDialogError(w, r, "promotions.error.customer_unknown")
				return
			}
			renderPromotionsDialogError(w, r, "promotions.error.update")
			return
		}
		audit(r, actor.ID, code, "promotion_edit", map[string]any{"changes": changes})
		redirectPromotions(w, r, "/promotions")
	})

	mux.HandleFunc("POST /api/promotions/{code}/active", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		code := r.PathValue("code")
		_ = r.ParseForm()
		activate := r.PostFormValue("active") == "1"
		if err := posRepo.SetPromotionActive(r.Context(), code, activate); err != nil {
			renderPromotionsDialogError(w, r, "promotions.error.update")
			return
		}
		action := "promotion_deactivate"
		if activate {
			action = "promotion_activate"
		}
		audit(r, actor.ID, code, action, nil)
		redirectPromotions(w, r, "/promotions")
	})
}

// promoDateLayout is the wire format of the page's <input type="date">
// fields, and the ISO-8601 date form the promotions table stores.
const promoDateLayout = "2006-01-02"

// parsePromoDate validates one date field from the form. Empty means "no
// bound" and is allowed. Anything else must be a real ISO-8601 calendar
// date: SQLite's datetime() returns NULL for junk, and FindActivePromo's
// `datetime(starts_at) <= CURRENT_TIMESTAMP` then silently evaluates NULL,
// which would leave a promo that looks saved but can never be redeemed.
func parsePromoDate(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", true
	}
	if len(s) > len(promoDateLayout) {
		// An already-stored end-of-day bound round-tripping through an edit.
		s = s[:len(promoDateLayout)]
	}
	if _, err := time.Parse(promoDateLayout, s); err != nil {
		return "", false
	}
	return s, true
}

// endsAtInclusive turns the end date the shop picked into the instant it
// actually stops being valid. FindActivePromo filters on
// `datetime(ends_at) >= CURRENT_TIMESTAMP`, and datetime('2026-08-31') is
// 2026-08-31 00:00:00 -- so a bare date would expire the promo at the START
// of its own last day, making a same-day promo (starts today, ends today)
// impossible to redeem at all. Storing the end of that day is the shop
// owner's plain reading of "ends 31 August" and needs no change to
// FindActivePromo, whose query is untouched.
func endsAtInclusive(date string) string {
	if date == "" {
		return ""
	}
	return date + " 23:59:59"
}

// promoDateOnly trims a stored bound back to its calendar date, for the
// <input type="date"> prefill and the list column -- the inverse of
// endsAtInclusive, and also tolerant of legacy/DB-authored rows that carry
// a full timestamp.
func promoDateOnly(stored string) string {
	if len(stored) > len(promoDateLayout) {
		return stored[:len(promoDateLayout)]
	}
	return stored
}

// promotionView is a promotion as the template needs it: PromotionAdmin
// plus pre-formatted display/edit-prefill strings, computed here at the
// UI-form boundary (never in internal/data -- the repo layer keeps value as
// a raw int64, per the promotions table's own established convention).
type promotionView struct {
	data.PromotionAdmin
	// PathCode is the code as one escaped URL path segment, for the row's
	// data-record-action / data-record-destructive-action. Those data-*
	// attributes are not URL context to html/template (a plain form action
	// was), so a raw "10%OFF" there reached the server as an invalid escape
	// (400) and a "#"/"?"/"/" cut the path short (ut-docs#3661 review).
	PathCode         string
	ValueDisplay     string // "£5.00" or "10.00%" ("10,00%" on comma locales), for the read-only list column
	ValueAmountMajor string // prefill for the amount-type edit input; "" if type is percent
	ValuePercent     string // prefill for the percent-type edit input; "" if type is amount
	StartsAtDate     string // calendar date only, for the <input type="date"> prefill
	EndsAtDate       string // ditto; strips endsAtInclusive's end-of-day time
	// CustomerLabel is what the dialog's customer field shows on edit: ""
	// (unbound), "Deleted customer", or the customer's name (ut-docs#5574).
	CustomerLabel string
}

// ut-docs#4097 (#4086 precedent, buildTaxOverrideRows): the percent strings are
// localized so a comma-locale till shows/prefills "7,50"; the save path
// already accepts either separator (#2954).
func newPromotionView(p data.PromotionAdmin, locale string) promotionView {
	v := promotionView{PromotionAdmin: p, PathCode: url.PathEscape(p.Code)}
	v.StartsAtDate = promoDateOnly(p.StartsAt)
	v.EndsAtDate = promoDateOnly(p.EndsAt)
	switch {
	case p.CustomerMissing:
		v.CustomerLabel = httpx.T(locale, "promotions.customer.deleted")
	case p.CustomerID != "":
		v.CustomerLabel = p.CustomerName
	}
	if p.Type == "percent" {
		pct := float64(p.Value) / 100
		v.ValuePercent = httpx.LocalizeMajor(fmt.Sprintf("%.2f", pct), locale)
		v.ValueDisplay = v.ValuePercent + "%"
	} else {
		v.ValueDisplay = money.FromMinor(p.Value).Format()
		// ut-docs#1290: was hardcoded %.2f against /100, silently wrong on a
		// 0-decimal currency (IRR/IRT/IQD/AFN/JPY) -- 500 minor units
		// rendered as "5.00" instead of "500". Same fix as ut-docs#1274's
		// CarryForwardDisplay.
		v.ValueAmountMajor = httpx.FormatMajorPlain(p.Value, httpx.ActiveCurrency().Decimals)
	}
	return v
}
