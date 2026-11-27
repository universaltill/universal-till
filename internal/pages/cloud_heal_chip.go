package pages

import (
	"fmt"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/credheal"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ADR-0217 §4.7 and the UX notes: the heal engine's states as plain words.
// One view feeds four places so they never disagree: the status-bar chip
// (Short as the words, Sentence as the title), the banner on Tills > This
// till, the notice in Settings > Till registration and the Backups line.
// "Reconnecting" shows from the first refused call (credheal.Report sets it
// before the engine's goroutine runs). A state that needs a person has ONE
// button. Never a modal; selling is never touched.

// healChipClock is a var so tests can pin "now" for the waiting date.
var healChipClock = time.Now

// Where the one button lands. The code field is Settings > Till registration's
// "Reconnect" block, rendered open by ?reconnect=1 (ux-notes 3.11, option a).
const (
	healHrefCode     = "/settings?reconnect=1#pair-details"
	healHrefRegister = "/settings#registration"
	healHrefJoin     = "/tills?tab=add&need=join"
	healHrefThis     = "/tills?tab=this"
)

// healView is what the partials render.
type healView struct {
	Reason   string // the credheal reason (or "reconnecting"), for tests and styling
	Waiting  bool   // the till fixes itself: no amber, no banner
	Short    string // the chip's words
	Sentence string // title, banner body, registration notice
	Button   string // "" = no button
	Href     string // the button's target; the chip's target when there is no button
}

// healChip returns the view for a status, false when the heal chip stays out
// (state ok, or pair_again, which keeps today's chips).
func healChip(locale string, st credheal.Status, hasOwner bool) (healView, bool) {
	if !st.ShowsChip() {
		return healView{}, false
	}
	t := func(k string) string { return httpx.T(locale, "status.cloud_heal."+k) }
	reconnecting := healView{Reason: credheal.ReasonReconnecting, Waiting: true, Short: t("reconnecting_short"), Sentence: t("reconnecting"), Href: healHrefThis}
	switch st.State {
	case credheal.StateHealing:
		return reconnecting, true
	case credheal.StateWaiting:
		if st.Until.IsZero() {
			return reconnecting, true
		}
		date := httpx.FormatShortDateTime(st.Until.Local(), healChipClock().Local(), locale)
		return healView{Reason: st.Reason, Waiting: true, Short: fmt.Sprintf(t("waiting_short"), date), Sentence: fmt.Sprintf(t("waiting"), date), Href: healHrefThis}, true
	}
	switch st.Reason {
	case credheal.ReasonRemoved:
		return healView{Reason: st.Reason, Short: t("removed_short"), Sentence: t("removed"), Button: t("btn_enter_code"), Href: healHrefCode}, true
	case credheal.ReasonReplaced:
		v := healView{Reason: st.Reason, Short: t("replaced_unnamed_short"), Sentence: t("replaced_unnamed"), Button: t("btn_join"), Href: healHrefJoin}
		if st.Detail != "" {
			v.Short, v.Sentence = fmt.Sprintf(t("replaced_short"), st.Detail), fmt.Sprintf(t("replaced"), st.Detail)
		}
		return v, true
	case credheal.ReasonMoved:
		// The cloud names the target by its store id, never a display name:
		// never rendered, the till says "another shop" (review 2026-11-27).
		name := t("unnamed_shop")
		return healView{Reason: st.Reason, Short: fmt.Sprintf(t("moved_short"), name), Sentence: fmt.Sprintf(t("moved"), name), Button: t("btn_open"), Href: healHrefRegister}, true
	case credheal.ReasonStoreDeleted:
		// ux-notes 3.6 asks for a "Check again" button; it needs a new engine
		// route that this slice may not add (internal/credheal is T1's), so
		// the sentence stands alone as in design 4.7: at most one button.
		return healView{Reason: st.Reason, Short: t("shop_deleted_short"), Sentence: t("shop_deleted"), Href: healHrefThis}, true
	case credheal.ReasonRoleConflict:
		return healView{Reason: st.Reason, Short: t("role_conflict_short"), Sentence: t("role_conflict"), Button: t("btn_join"), Href: healHrefJoin}, true
	case credheal.ReasonInUseElsewhere:
		return healView{Reason: st.Reason, Short: t("in_use_elsewhere_short"), Sentence: t("in_use_elsewhere"), Button: t("btn_open"), Href: healHrefCode}, true
	}
	// cannot_prove, and anything the engine adds later: the safe words.
	if hasOwner {
		return healView{Reason: credheal.ReasonCannotProve, Short: t("cannot_prove_short"), Sentence: t("cannot_prove"), Button: t("btn_code"), Href: healHrefCode}, true
	}
	return healView{Reason: credheal.ReasonCannotProve, Short: t("cannot_prove_no_account_short"), Sentence: t("cannot_prove_no_account"), Button: t("btn_new_shop"), Href: healHrefRegister}, true
}

// healNoticeView is the heal engine's sentence for Settings > Till registration
// (a manager only; the code field and the register blocks are on that page, so
// the notice carries a button only when it points somewhere else). nil = none.
func healNoticeView(r *http.Request, d *common.Deps, locale string) map[string]any {
	if d == nil || d.Settings == nil || !canPerform(d, r, "settings") {
		return nil
	}
	hv, show := healChip(locale, healChipStatus(), storeHasOwner(r.Context(), d.Settings))
	if !show {
		return nil
	}
	out := map[string]any{"sentence": hv.Sentence, "reason": hv.Reason}
	if hv.Href == healHrefJoin {
		out["button"], out["href"] = hv.Button, hv.Href
	}
	return out
}

// healNeedsCode says the state's sentence tells the person to type a code on
// Settings > Till registration, so that page opens the code field itself
// (a manager arriving by the menu must not hunt for a folded section).
func healNeedsCode(st credheal.Status) bool {
	if !st.ShowsChip() || st.State == credheal.StateHealing || st.State == credheal.StateWaiting {
		return false
	}
	switch st.Reason {
	case credheal.ReasonReplaced, credheal.ReasonRoleConflict, credheal.ReasonStoreDeleted:
		return false
	}
	return true
}
