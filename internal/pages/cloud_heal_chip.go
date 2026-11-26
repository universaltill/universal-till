package pages

import (
	"fmt"
	"time"

	"github.com/universaltill/universal-till/internal/credheal"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ADR-0217 §4.7: the heal engine's states as plain words in the status bar.
// "Reconnecting" shows from the first refused call (credheal.Report sets it
// before the engine's goroutine runs); a state that needs a person carries
// exactly one link. Never a modal; selling is never touched.

// healChipClock is a var so tests can pin "now" for the waiting date.
var healChipClock = time.Now

// healChipView is what the partial renders.
type healChipView struct {
	Text, Button, Href string
}

// healChip returns the chip for a status, false when the heal chip stays out
// (state ok, or pair_again, which keeps today's chips).
func healChip(locale string, st credheal.Status, hasOwner bool) (healChipView, bool) {
	if !st.ShowsChip() {
		return healChipView{}, false
	}
	t := func(k string) string { return httpx.T(locale, "status.cloud_heal."+k) }
	named := func(key, fallback string) string {
		name := st.Detail
		if name == "" {
			name = t(fallback)
		}
		return fmt.Sprintf(t(key), name)
	}
	switch st.State {
	case credheal.StateHealing:
		return healChipView{Text: t("reconnecting")}, true
	case credheal.StateWaiting:
		until := st.Until
		if until.IsZero() {
			return healChipView{Text: t("reconnecting")}, true
		}
		return healChipView{Text: fmt.Sprintf(t("waiting"), httpx.FormatShortDateTime(until.Local(), healChipClock().Local(), locale))}, true
	}
	switch st.Reason {
	case credheal.ReasonRemoved:
		return healChipView{Text: t("removed"), Button: t("btn_enter_code"), Href: "/tills?tab=this"}, true
	case credheal.ReasonReplaced:
		return healChipView{Text: named("replaced", "unnamed_till"), Button: t("btn_join"), Href: "/tills?tab=add"}, true
	case credheal.ReasonMoved:
		return healChipView{Text: named("moved", "unnamed_shop"), Button: t("btn_open"), Href: "/settings#registration"}, true
	case credheal.ReasonStoreDeleted:
		return healChipView{Text: t("shop_deleted")}, true
	case credheal.ReasonRoleConflict:
		return healChipView{Text: t("role_conflict"), Button: t("btn_join"), Href: "/tills?tab=add"}, true
	case credheal.ReasonInUseElsewhere:
		return healChipView{Text: t("in_use_elsewhere"), Button: t("btn_open"), Href: "/settings#registration"}, true
	}
	// cannot_prove, and anything the engine adds later: the safe words.
	if hasOwner {
		return healChipView{Text: t("cannot_prove"), Button: t("btn_code"), Href: "/tills?tab=this"}, true
	}
	return healChipView{Text: t("cannot_prove_no_account"), Button: t("btn_new_shop"), Href: "/settings#registration"}, true
}
