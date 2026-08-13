package pages

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
)

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

	requireManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		u, ok := auth.FromContext(r.Context())
		if !ok || !u.IsManager() {
			http.Error(w, "manager or admin role required", http.StatusForbidden)
			return auth.User{}, false
		}
		return u, true
	}

	audit := func(r *http.Request, actorID, code, action string) {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "promotion", code, action, nil, now, "")
	}

	renderPromotions := func(w http.ResponseWriter, r *http.Request, errKey string) {
		list, err := posRepo.ListPromotionsForAdmin(r.Context())
		if err != nil {
			http.Error(w, "failed to load promotions", http.StatusInternalServerError)
			return
		}
		views := make([]promotionView, 0, len(list))
		for _, p := range list {
			views = append(views, newPromotionView(p))
		}
		httpx.Render("ui/pages/promotions.html", map[string]any{
			"title":      "Promotions",
			"theme":      d.CurrentState().Theme,
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
			major, err := strconv.ParseFloat(strings.TrimSpace(r.PostFormValue("value_amount")), 64)
			if err != nil || major <= 0 {
				return data.PromotionInput{}, "promotions.error.value_invalid", false
			}
			value = money.FromMinor(int64(math.Round(major * 100))).Minor()
		case "percent":
			pct, err := strconv.ParseFloat(strings.TrimSpace(r.PostFormValue("value_percent")), 64)
			if err != nil || pct <= 0 {
				return data.PromotionInput{}, "promotions.error.value_invalid", false
			}
			value = int64(math.Round(pct * 100))
		default:
			return data.PromotionInput{}, "promotions.error.value_invalid", false
		}
		startsAt := strings.TrimSpace(r.PostFormValue("starts_at"))
		endsAt := strings.TrimSpace(r.PostFormValue("ends_at"))
		if startsAt != "" && endsAt != "" && endsAt < startsAt {
			return data.PromotionInput{}, "promotions.error.dates_invalid", false
		}
		return data.PromotionInput{
			Type:        typ,
			Value:       value,
			Description: strings.TrimSpace(r.PostFormValue("description")),
			StartsAt:    startsAt,
			EndsAt:      endsAt,
			CustomerID:  strings.TrimSpace(r.PostFormValue("customer_id")),
		}, "", true
	}

	mux.HandleFunc("GET /promotions", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireManager(w, r); !ok {
			return
		}
		renderPromotions(w, r, r.URL.Query().Get("err"))
	})

	mux.HandleFunc("POST /api/promotions", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		_ = r.ParseForm()
		code := strings.TrimSpace(r.PostFormValue("code"))
		if code == "" {
			http.Redirect(w, r, "/promotions?err=promotions.error.required", http.StatusSeeOther)
			return
		}
		in, errKey, ok := parsePromotionForm(r)
		if !ok {
			http.Redirect(w, r, "/promotions?err="+errKey, http.StatusSeeOther)
			return
		}
		if err := posRepo.CreatePromotion(r.Context(), code, in); err != nil {
			if errors.Is(err, data.ErrPromotionCodeExists) {
				http.Redirect(w, r, "/promotions?err=promotions.error.code_exists", http.StatusSeeOther)
				return
			}
			http.Redirect(w, r, "/promotions?err=promotions.error.create", http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, code, "promotion_create")
		http.Redirect(w, r, "/promotions", http.StatusSeeOther)
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
			http.Redirect(w, r, "/promotions?err="+errKey, http.StatusSeeOther)
			return
		}
		if err := posRepo.UpdatePromotion(r.Context(), code, in); err != nil {
			http.Redirect(w, r, "/promotions?err=promotions.error.update", http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, code, "promotion_edit")
		http.Redirect(w, r, "/promotions", http.StatusSeeOther)
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
			http.Redirect(w, r, "/promotions?err=promotions.error.update", http.StatusSeeOther)
			return
		}
		action := "promotion_deactivate"
		if activate {
			action = "promotion_activate"
		}
		audit(r, actor.ID, code, action)
		http.Redirect(w, r, "/promotions", http.StatusSeeOther)
	})
}

// promotionView is a promotion as the template needs it: PromotionAdmin
// plus pre-formatted display/edit-prefill strings, computed here at the
// UI-form boundary (never in internal/data -- the repo layer keeps value as
// a raw int64, per the promotions table's own established convention).
type promotionView struct {
	data.PromotionAdmin
	ValueDisplay     string // "£5.00" or "10.00%", for the read-only list column
	ValueAmountMajor string // prefill for the amount-type edit input; "" if type is percent
	ValuePercent     string // prefill for the percent-type edit input; "" if type is amount
}

func newPromotionView(p data.PromotionAdmin) promotionView {
	v := promotionView{PromotionAdmin: p}
	if p.Type == "percent" {
		pct := float64(p.Value) / 100
		v.ValueDisplay = fmt.Sprintf("%.2f%%", pct)
		v.ValuePercent = fmt.Sprintf("%.2f", pct)
	} else {
		v.ValueDisplay = money.FromMinor(p.Value).Format()
		v.ValueAmountMajor = fmt.Sprintf("%.2f", float64(p.Value)/100)
	}
	return v
}
