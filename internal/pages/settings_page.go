package pages

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/barcode"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/data/seeddata"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pages/settingsnav"
	"github.com/universaltill/universal-till/internal/plugins/builtinlayouts"
	"github.com/universaltill/universal-till/internal/pos"
)

// isPiKioskAppliance reports whether the wired WindowController is the Pi
// kiosk appliance's systemd controller — the source of truth for the
// Display card's topology note (review of ut-docs#1039, finding 8),
// because it is the same fact pages.Init decided window control on.
func isPiKioskAppliance(wc common.WindowController) bool {
	_, ok := wc.(common.KioskSystemdWindowController)
	return ok
}

// windowControlTopology reports which of the three mutually-exclusive
// window-control topologies (ADR-0064, ut-docs#1039 finding 8) this till is
// actually in — shellAttached: a desktop shell is holding a live control
// poll; piKioskAppliance: kiosk is real and systemd-driven, read off the
// controller pages.Init actually wired. ut-docs#1060: extracted from the
// main /settings render so GET /ui/settings/window-mode-status's own
// standalone poll response computes the exact same two facts the same way
// — one copy of this logic, not two independently-maintained ones. Nil-safe
// for bare-Deps tests.
func windowControlTopology(d *common.Deps) (shellAttached, piKioskAppliance bool) {
	return d.Shell != nil && d.Shell.Attached(common.ShellAttachedWindow), isPiKioskAppliance(d.WindowCtl)
}

// shortDeviceID trims a long "till-<uuid>" id to a readable prefix for display.
func shortDeviceID(id string) string {
	if len(id) > 16 {
		return id[:16] + "…"
	}
	return id
}

// demoBasketMatch names which live basket demoDataInLiveBasket found a
// demo item/customer in, so the caller can tell the manager specifically
// which basket to clear (ut-docs#746 — a single combined bool left a
// kiosk-basket match reported as a generic "current basket," which the
// manager at the Settings screen (not the kiosk) has no way to act on).
type demoBasketMatch string

const (
	noBasketMatch      demoBasketMatch = ""
	cashierBasketMatch demoBasketMatch = "cashier"
	kioskBasketMatch   demoBasketMatch = "kiosk"
)

// demoDataInLiveBasket reports which currently in-progress basket — cashier
// and/or self-order kiosk, both passed in — references a demo catalogue
// item/variant or a demo customer, if either does. ut-docs#633: unlike a
// HELD (parked) sale, a live basket has no held_sales row for
// remove_demo.sql/remove_demo_customers_promos.sql's own safety check to
// catch, so "Remove sample data" must guard against it directly here —
// otherwise the referenced row disappears and tender later FK-fails with
// no clear way to recover. nil engines (KioskEngine is nil in some test
// harnesses, per ADR-0020) are skipped, not treated as a match. Checked in
// a fixed order (cashier before kiosk) so the result is deterministic even
// if both baskets happen to match at once.
//
// ut-docs#746: deliberately over-blocks — this only checks list
// membership, not the shared seeddata removal scripts' own full
// "untouched" predicates: remove_demo.sql's item rule (PRISTINE
// sku/name/base_price, no sale_lines/stock_movements/held_sales
// reference, live or archived) and remove_demo_customers_promos.sql's
// customer rule (no sales/held_sales reference or promotion targeting,
// live or archived — a differently-shaped check, see that script's own
// header). Reimplementing either predicate here would duplicate a
// safety-critical rule in a second language, and the two copies drifting
// apart is a worse failure mode than an occasional over-cautious block —
// so a demo item/customer merely sitting in a basket blocks removal even
// when the SQL script itself would have kept it anyway.
//
// ut-docs#745: deliberately does NOT check an applied demo promo code
// (PROMO50/PROMO500/DISC10), unlike the item/variant lines and the
// customer above. This is safe today, not an oversight: sale_discounts
// (see 001_init.sql) records only the resulting discount amount for a
// redeemed promo, not which code produced it, so removing a demo
// promotion mid-basket can't leave a dangling reference the way an
// item/customer FK could — there's nothing in this basket for the removed
// row to break. remove_demo_customers_promos.sql's own header spells out
// the same gap for its "untouched" check. If promo-code redemption ever
// becomes durable (there's no promotions management UI yet, so it hasn't
// needed to), this stops being true silently — a change there should
// revisit this function too.
func demoDataInLiveBasket(cashier, kiosk *pos.Service) demoBasketMatch {
	baskets := []struct {
		kind demoBasketMatch
		e    *pos.Service
	}{
		{cashierBasketMatch, cashier},
		{kioskBasketMatch, kiosk},
	}
	for _, b := range baskets {
		if b.e == nil {
			continue
		}
		if slices.Contains(seeddata.DemoCustomerIDs, b.e.CustomerID()) {
			return b.kind
		}
		for _, l := range b.e.Lines() {
			if slices.Contains(seeddata.ItemIDs, l.ItemID) || slices.Contains(seeddata.VariantIDs, l.VariantID) {
				return b.kind
			}
		}
	}
	return noBasketMatch
}

// settingsAudit writes the audit entry for one settings mutation wired to
// checkOrElevate (ut-docs#796, mechanism ADR-0052/ut-docs#557): plain
// InsertAudit attributed to the session user on the allowed path,
// InsertAuditElevated (dual attribution — approver acted, session user was
// blocked) once an approver's PIN got the request past the gate.
// Best-effort like backup_api.go's precedent — the settings write itself
// already succeeded by the time this runs, so a failed audit insert doesn't
// fail the request — but logged rather than silently swallowed (same
// posture as the ADR-0048 fiscal-toggle audit in the upsert handler).
func settingsAudit(r *http.Request, repo *data.POSRepo, elev elevationCheck, entityType, entityID, action string, payload map[string]any) {
	now := time.Now().UTC().Format(time.RFC3339)
	var err error
	if elev.Outcome == elevated {
		err = repo.InsertAuditElevated(r.Context(), nil, elev.ApproverID, elev.ActorID, entityType, entityID, action, payload, now, "")
	} else {
		err = repo.InsertAudit(r.Context(), nil, elev.ActorID, entityType, entityID, action, payload, now, "")
	}
	if err != nil {
		logging.L().Errorf("settings audit: %s %s/%s failed: %v", action, entityType, entityID, err)
	}
}

// settingsActorID resolves the audit actor for a plain (non-elevation)
// manager-gated handler: the session user when one exists, else "system"
// (the UT_AUTH=off dev/CI bypass has no session to attribute — "system" is
// the seeded fallback actor, migration 003, so the FK still holds).
func settingsActorID(r *http.Request) string {
	if u, ok := auth.FromContext(r.Context()); ok && u.ID != "" {
		return u.ID
	}
	return "system"
}

// renderTSEProvisioningBlock re-renders the Settings TSE provisioning block
// (web/ui/partials/tse_provisioning_block.html) for an htmx outerHTML swap —
// the retry endpoint's response shape (ut-docs#1174 item A). A nil/cleared
// state renders an empty 200 body, the same "swap the block away" contract
// the dismiss endpoint already has.
func renderTSEProvisioningBlock(w http.ResponseWriter, r *http.Request, st *tseProvisioningState) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	view := tseProvisioningViewFor(st)
	if view == nil {
		return
	}
	httpx.RenderPartial("ui/partials/tse_provisioning_block.html", map[string]any{
		"tseProvisioning": view,
		"tseRetryable":    tseProvisioningRetryable(st),
		"tseCanDismiss":   !tseProvisioningDismissBlocked(st),
	})(w, r)
}

// settingsRespondSaved answers success on an elevation-wired handler whose
// plain-session contract is a bare 204: unchanged (204) for an ordinarily
// authorized session, but a real confirmation body once elevation was
// involved — the elevation dialog's retry lands its response in an
// hx-target, and a 204 never swaps under htmx, so the approver would get no
// feedback at all (same reasoning as eod_api.go's report-retention handler,
// ut-docs#794 review finding). X-UT-Response: ok (ut-docs#796 review
// finding #4) is what elevation_prompt.html's own retry-form handler
// actually keys its post-close reload off — without it, the dialog closes
// but shop-type/till-name/till-register/save/upsert's own form still shows
// the pre-change value, exactly the staleness those forms' plain
// window.location.reload() exists to prevent on the ordinary 204 path.
func settingsRespondSaved(w http.ResponseWriter, r *http.Request, elev elevationCheck) {
	if elev.Outcome == elevated {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-UT-Response", "ok")
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(httpx.ResolveLocale(w, r), "elevation.approved"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeKeptDemoItemsHTML renders the "kept" list ut-docs#1840 AC2/AC3 asks
// for — WHICH sample items were kept and the ACTUAL reason per row (never
// again a single count with one blanket "already in use" that's simply
// false for an edited-but-otherwise-untouched item), plus, for exactly the
// reason a merchant can act on, inline resolution buttons (AC3). Nothing is
// written when items is empty (the common case — most removals keep
// nothing).
func writeKeptDemoItemsHTML(b *strings.Builder, locale string, items []data.KeptDemoItem) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, `<p class="muted">%s</p><ul class="demo-kept-list">`, html.EscapeString(httpx.T(locale, "settings.data.demo_kept_list_intro")))
	for _, it := range items {
		id := html.EscapeString(it.ID)
		fmt.Fprintf(b, `<li class="demo-kept-item" data-testid="demo-kept-item" data-item-id="%s">`, id)
		fmt.Fprintf(b, `<strong>%s</strong> <span class="muted">(%s)</span> — `,
			html.EscapeString(it.Name), html.EscapeString(it.SKU))
		switch it.Reason {
		case data.KeptReasonEdited:
			fmt.Fprintf(b, `<span>%s</span> `, html.EscapeString(httpx.T(locale, "settings.data.demo_kept_reason_edited")))
			fmt.Fprintf(b, `<button class="btn secondary" data-testid="demo-item-remove-anyway" `+
				`hx-post="/api/settings/demo-item/%s/remove" hx-confirm="%s" `+
				`hx-target="%s" hx-swap="innerHTML" hx-disabled-elt="this">%s</button> `,
				id, html.EscapeString(httpx.T(locale, "settings.data.demo_item_remove_anyway_confirm")),
				html.EscapeString(demoItemMsgSelector(it.ID)), html.EscapeString(httpx.T(locale, "settings.data.demo_item_remove_anyway_btn")))
			fmt.Fprintf(b, `<button class="btn secondary" data-testid="demo-item-keep-own" `+
				`hx-post="/api/settings/demo-item/%s/keep" `+
				`hx-target="%s" hx-swap="innerHTML" hx-disabled-elt="this">%s</button>`,
				id, html.EscapeString(demoItemMsgSelector(it.ID)), html.EscapeString(httpx.T(locale, "settings.data.demo_item_keep_own_btn")))
		case data.KeptReasonHeld:
			fmt.Fprintf(b, `<span>%s</span>`, html.EscapeString(httpx.T(locale, "settings.data.demo_kept_reason_held")))
		default: // data.KeptReasonHistory
			// ut-docs#1840 AC4 / review finding F1: name the real mechanism
			// (deactivate the item from Catalog, then run Catalog cleanup
			// from Settings → Data — manager only) rather than a positional
			// "below", which is simply wrong here: Catalog cleanup lives on
			// THIS SAME /settings page, in a manager-only block ABOVE the
			// Sample Data section, not below it, and this section itself is
			// visible to non-managers too (gated on sampleCount, not
			// isManager) — so a cashier reading "below" has no such section
			// on their page at all. The reason text names the location
			// explicitly instead; the button still links to /catalog, which
			// is genuinely step one (deactivating the item).
			fmt.Fprintf(b, `<span>%s</span> <a class="btn secondary" href="/catalog">%s</a>`,
				html.EscapeString(httpx.T(locale, "settings.data.demo_kept_reason_history")),
				html.EscapeString(httpx.T(locale, "settings.data.demo_go_to_catalog_btn")))
		}
		fmt.Fprintf(b, ` <span id="%s" class="muted" aria-live="polite"></span></li>`, html.EscapeString(demoItemMsgID(it.ID)))
	}
	b.WriteString(`</ul>`)
}

// demoItemMsgID is the DOM id shared by a kept-item row's message span, the
// row's own buttons' hx-target, and the elevation prompt's hxTarget for the
// two per-item endpoints (RemoveDemoItem/KeepDemoItemAsOwn) — one definition
// so the three call sites can't drift apart.
func demoItemMsgID(itemID string) string { return "demo-item-msg-" + itemID }

// demoItemMsgSelector is demoItemMsgID as an htmx hx-target: the
// attribute-selector form, not a bare "#"+id (ut-docs#1840 review finding
// F3, mirroring dismiss-pending-base-plugin's own
// `[id="pending-plugin-msg-%s"]` above) — itemID is never validated against
// CSS identifier syntax, so a bare "#"+id could break on a value that would
// need escaping there.
func demoItemMsgSelector(itemID string) string {
	return fmt.Sprintf(`[id="%s"]`, demoItemMsgID(itemID))
}

// demoPromoMsgID/demoPromoMsgSelector mirror demoItemMsgID/
// demoItemMsgSelector exactly (ut-docs#1858), for the promo code's own
// per-record endpoints (RemoveDemoPromo/KeepDemoPromoAsOwn) — a promo code
// is not validated against CSS identifier syntax either, so the same
// attribute-selector escaping applies.
func demoPromoMsgID(code string) string { return "demo-promo-msg-" + code }

func demoPromoMsgSelector(code string) string {
	return fmt.Sprintf(`[id="%s"]`, demoPromoMsgID(code))
}

// writeKeptDemoCustomersHTML renders the "kept" list for demo customers
// (ut-docs#1858, mirroring writeKeptDemoItemsHTML) — named + reasoned, never
// a blanket count. No row here ever gets resolution buttons: every
// KeptDemoCustomer reason is a genuine, unresolvable live reference (see
// that type's own doc comment), unlike an item or promo's ReasonEdited.
func writeKeptDemoCustomersHTML(b *strings.Builder, locale string, customers []data.KeptDemoCustomer) {
	if len(customers) == 0 {
		return
	}
	fmt.Fprintf(b, `<p class="muted">%s</p><ul class="demo-kept-list">`, html.EscapeString(httpx.T(locale, "settings.data.demo_kept_customers_intro")))
	for _, c := range customers {
		fmt.Fprintf(b, `<li class="demo-kept-customer" data-testid="demo-kept-customer" data-customer-id="%s">`, html.EscapeString(c.ID))
		fmt.Fprintf(b, `<strong>%s</strong> — `, html.EscapeString(c.Name))
		var reasonKey string
		switch c.Reason {
		case data.KeptReasonHeld:
			reasonKey = "settings.data.demo_kept_reason_customer_held"
		case data.KeptReasonTargeted:
			reasonKey = "settings.data.demo_kept_reason_customer_targeted"
		default: // data.KeptReasonHistory
			reasonKey = "settings.data.demo_kept_reason_customer_sold"
		}
		fmt.Fprintf(b, `<span>%s</span></li>`, html.EscapeString(httpx.T(locale, reasonKey)))
	}
	b.WriteString(`</ul>`)
}

// writeKeptDemoPromosHTML renders the "kept" list for demo promo codes
// (ut-docs#1858, mirroring writeKeptDemoItemsHTML) — named + reasoned,
// with the same "remove anyway"/"keep as my own" resolution items got,
// offered only for ReasonEdited (ReasonTargeted, like an item's
// ReasonHistory/ReasonHeld, is never resolvable — it's a real reference).
func writeKeptDemoPromosHTML(b *strings.Builder, locale string, promos []data.KeptDemoPromo) {
	if len(promos) == 0 {
		return
	}
	fmt.Fprintf(b, `<p class="muted">%s</p><ul class="demo-kept-list">`, html.EscapeString(httpx.T(locale, "settings.data.demo_kept_promos_intro")))
	for _, p := range promos {
		code := html.EscapeString(p.Code)
		fmt.Fprintf(b, `<li class="demo-kept-promo" data-testid="demo-kept-promo" data-promo-code="%s">`, code)
		fmt.Fprintf(b, `<strong>%s</strong> <span class="muted">(%s)</span> — `,
			code, html.EscapeString(p.Description))
		switch p.Reason {
		case data.KeptReasonEdited:
			fmt.Fprintf(b, `<span>%s</span> `, html.EscapeString(httpx.T(locale, "settings.data.demo_kept_reason_promo_edited")))
			fmt.Fprintf(b, `<button class="btn secondary" data-testid="demo-promo-remove-anyway" `+
				`hx-post="/api/settings/demo-promo/%s/remove" hx-confirm="%s" `+
				`hx-target="%s" hx-swap="innerHTML" hx-disabled-elt="this">%s</button> `,
				code, html.EscapeString(httpx.T(locale, "settings.data.demo_promo_remove_anyway_confirm")),
				html.EscapeString(demoPromoMsgSelector(p.Code)), html.EscapeString(httpx.T(locale, "settings.data.demo_promo_remove_anyway_btn")))
			fmt.Fprintf(b, `<button class="btn secondary" data-testid="demo-promo-keep-own" `+
				`hx-post="/api/settings/demo-promo/%s/keep" `+
				`hx-target="%s" hx-swap="innerHTML" hx-disabled-elt="this">%s</button>`,
				code, html.EscapeString(demoPromoMsgSelector(p.Code)), html.EscapeString(httpx.T(locale, "settings.data.demo_promo_keep_own_btn")))
		default: // data.KeptReasonTargeted
			fmt.Fprintf(b, `<span>%s</span>`, html.EscapeString(httpx.T(locale, "settings.data.demo_kept_reason_promo_targeted")))
		}
		fmt.Fprintf(b, ` <span id="%s" class="muted" aria-live="polite"></span></li>`, html.EscapeString(demoPromoMsgID(p.Code)))
	}
	b.WriteString(`</ul>`)
}

// disableDemoRowButtonsScript is appended to a successful remove/keep
// response (ut-docs#1840 review finding F9): without it, the row's OTHER
// button stays clickable after one resolves the item, and clicking it next
// returns a confusing "already gone" for an item just deliberately kept (or
// vice versa). Same allowScriptTags convention this codebase already uses
// for self-contained post-swap behavior (see elevation_prompt.html's own
// dialog.show() and GetLowStock's low-stock badge script) — the script tag
// lands inside the row's own message span (this handler's only swap
// target), so `.closest` reaches the row without needing an id. rowClass
// (ut-docs#1858: generalized from a hardcoded ".demo-kept-item" so the
// promo rows below can share this instead of duplicating it) is the row
// li's own class — ".demo-kept-item" or ".demo-kept-promo".
func disableDemoRowButtonsScript(rowClass string) string {
	return `<script>(function(s){var li=s.closest('` + rowClass + `');if(li){li.querySelectorAll('button').forEach(function(b){b.disabled=true;});}})(document.currentScript)</script>`
}

// filterSettingsNavForRender drops any settingsnav.Row this specific
// request's own `.card` conditionals (below, in registerSettings' /settings
// handler) will NOT actually render this time — ut-docs#1913. Without this,
// a gated card's title would leak into #settings-nav-index (and therefore
// the response body) even when its `.card` is genuinely absent from the
// DOM, which is exactly what
// TestSettingsPage_DataCardHiddenFromCashierWhenNothingPending pins must
// never happen for a cashier session with nothing pending. Keeps whatever
// ORDER settingsnav.Resolve produced (a `layout` plugin's reorder still
// applies) — this only removes rows, never reorders the survivors.
func filterSettingsNavForRender(rows []settingsnav.Row, isManager, hasPayMethods, showDataCard bool) []settingsnav.Row {
	hiddenThisRequest := map[string]bool{
		"settings-issuereport": !isManager,
		"settings-menulayout":  !isManager,
		"settings-payments":    !hasPayMethods,
		"settings-data":        !showDataCard,
		"settings-all":         !isManager,
	}
	out := make([]settingsnav.Row, 0, len(rows))
	for _, row := range rows {
		if hiddenThisRequest[row.Key] {
			continue
		}
		out = append(out, row)
	}
	return out
}

func registerSettings(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)
	mux.HandleFunc("/settings", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		all, _ := d.Settings.All(r.Context())
		st := d.CurrentState()
		scale := st.UIScale
		if scale <= 0 {
			scale = 1
		}
		payMethods, _ := data.NewPOSRepo(d.Db).ListActivePaymentMethods(r.Context())
		payDefault, _, _ := d.Settings.Get(r.Context(), "payments.default_method")
		type feeRow struct {
			ID, Name   string
			PercentMaj string
			FixedMaj   string
		}
		feeRows := make([]feeRow, 0, len(payMethods))
		for _, m := range payMethods {
			fr := feeRow{ID: m.ID, Name: m.Name}
			if raw, ok, _ := d.Settings.Get(r.Context(), "payments.fee."+m.ID); ok && raw != "" {
				var f struct {
					BP    int64 `json:"bp"`
					Fixed int64 `json:"fixed"`
				}
				if json.Unmarshal([]byte(raw), &f) == nil {
					if f.BP > 0 {
						fr.PercentMaj = fmt.Sprintf("%.2f", float64(f.BP)/100)
					}
					if f.Fixed > 0 {
						// ut-docs#1290: was hardcoded %.2f against /100,
						// silently wrong on a 0-decimal currency (IRR/IRT/
						// IQD/AFN/JPY) -- 500 minor units rendered as
						// "5.00" instead of "500". Same fix as
						// ut-docs#1274's CarryForwardDisplay.
						fr.FixedMaj = httpx.FormatMajorPlain(f.Fixed, httpx.ActiveCurrency().Decimals)
					}
				}
			}
			feeRows = append(feeRows, fr)
		}
		autoUpdateEnabled, _, _ := d.Settings.Get(r.Context(), keyAutoUpdateEnabled)
		autoUpdateTime, _, _ := d.Settings.Get(r.Context(), keyAutoUpdateTime)
		// Sample-data note (ut-docs#539, extended to customers/promos by
		// ut-docs#567): best-effort — a schema-less test DB or a query
		// error just renders the page without the note, same posture as
		// payMethods above. Combined across catalogue items + customers +
		// promo codes, since "Remove sample data" now clears all three
		// together and the note should describe what the button actually
		// does.
		demoSeedRepo := data.NewDemoSeedRepo(d.Db)
		sampleItemCount, _ := demoSeedRepo.SampleItemCount(r.Context())
		sampleCustomerPromoCount, _ := demoSeedRepo.SampleCustomerPromoCount(r.Context())
		sampleCount := sampleItemCount + sampleCustomerPromoCount
		shopType, _, _ := d.Settings.Get(r.Context(), common.KeyShopType)
		// Restore-from-another-POS resume prompt (ut-docs#617): only shown
		// when the wizard's "Later" choice left it deferred; best-effort
		// like shopType above, same posture.
		restorePromptStatus, _, _ := d.Settings.Get(r.Context(), common.KeyRestorePromptStatus)
		// Country base-plugin auto-install (ut-docs#591): whatever the setup
		// wizard's own attempt and the background retry haven't installed
		// yet, so the merchant can see it's happening (or failing) and
		// dismiss it — best-effort like everything else on this page, a
		// read error just renders with nothing pending.
		pendingBasePlugins, _ := loadPendingBasePlugins(r.Context(), d)
		// German TSE provisioning status (ADR-0053, ut-docs#802): whatever
		// the wizard's kickoff / the background retry / the ready-directive
		// handler has left in flight or failed, so the merchant sees exactly
		// where it stands (still pending vs kickoff rejected vs credential
		// fetch failed) and can dismiss it — best-effort like everything
		// else on this page, a read error just renders with nothing pending.
		tseState, _ := loadTSEProvisioningState(r.Context(), d)
		// Missing fiscal signer (DE hard-gate visibility card): a
		// system-of-record DE shop with no active fiscal.sign.ask plugin
		// gets a persistent, non-dismissable banner here --
		// detection-and-visibility only, never touches the gate itself.
		// Best-effort like tseState above: a read error just renders
		// with the banner absent.
		missingSigner, missingSignerErr := missingFiscalSigner(r.Context(), d, d.CurrentState().Country)
		if missingSignerErr != nil {
			logging.L().Errorf("missing-fiscal-signer check: %v", missingSignerErr)
		}
		// Missing tax-rate switcher (ADR-0068): a mandated country (today:
		// DE) with no active, working tax.rate.ask answerer gets a
		// persistent Settings banner — detection-and-visibility only,
		// never touches tax_hook.go's actual rate-switching behaviour.
		// Best-effort like missingSigner above: a read error just renders
		// with the banner absent.
		missingSwitcher, missingSwitcherErr := missingTaxRateSwitcher(r.Context(), d, d.CurrentState().Country)
		if missingSwitcherErr != nil {
			logging.L().Errorf("missing-tax-rate-switcher check: %v", missingSwitcherErr)
		}
		exportEntries, exportEntriesErr := data.NewPluginRepo(d.Db).ListExportEntries(r.Context())
		if exportEntriesErr != nil {
			// Non-fatal: the settings page still renders without the
			// export section, matching a genuinely-empty install — but a
			// real DB error here shouldn't look identical to that in the
			// logs (ut-docs#189 review).
			logging.L().Errorf("list export entries: %v", exportEntriesErr)
		}
		// ADR-0040 (ut-docs#571 card 1): the current retention mode
		// (defaulting to "till", same fallback the prune step itself uses)
		// and how far back the archive goes, for the new Report Retention
		// card. Non-fatal on error, same reasoning as exportEntries above.
		reportRetentionMode, _, _ := d.Settings.Get(r.Context(), common.KeyReportRetentionMode)
		if reportRetentionMode == "" {
			reportRetentionMode = common.ReportRetentionModeTill
		}
		reportArchiveCoverage, coverageErr := data.NewPOSRepo(d.Db).ReportArchiveCoverage(r.Context())
		if coverageErr != nil {
			logging.L().Errorf("report archive coverage: %v", coverageErr)
		}
		// ADR-0042: archived reset batches for the Data card's restore list.
		// Best-effort like the other Data-card queries above — a schema-less
		// test DB just renders the page without the list.
		resetBatchesRaw, resetBatchesErr := data.NewPOSRepo(d.Db).ListResetBatches(r.Context())
		if resetBatchesErr != nil {
			logging.L().Errorf("list reset batches: %v", resetBatchesErr)
		}
		// Locale-aware date format (ut-docs#1130/#1632/#1894). The .backups
		// table below used to be a separate gap (ut-docs#1936 — it was
		// still a hardcoded "2006-01-02 15:04" in listBackupsForUI,
		// backup_api.go) but now goes through the same httpx.FormatDateTime
		// call as everything else on this page.
		// Purgeable/RetainedUntilDisplay (ut-docs#698) let the template show
		// per-row purge eligibility instead of every row offering a
		// Delete-permanently control that a gated batch will just refuse.
		type resetBatchView struct {
			ID                   string
			CreatedAt            string
			SalesCount           int64
			Purgeable            bool
			RetainedUntilDisplay string
		}
		resetBatches := make([]resetBatchView, 0, len(resetBatchesRaw))
		for _, b := range resetBatchesRaw {
			display := b.CreatedAt
			if t, err := time.Parse(time.RFC3339, b.CreatedAt); err == nil {
				display = httpx.FormatDateTime(t.Local(), locale)
			}
			view := resetBatchView{ID: b.ID, CreatedAt: display, SalesCount: b.SalesCount, Purgeable: b.Purgeable}
			if !b.RetainedUntil.IsZero() {
				view.RetainedUntilDisplay = httpx.FormatDate(b.RetainedUntil.Local(), locale)
			}
			resetBatches = append(resetBatches, view)
		}
		// This till's register identity for the Tills card's picker
		// (ut-docs#268). Ambiguous is a normal state here — a
		// multi-register shop where nobody has picked yet renders the
		// dropdown unselected — and any other resolution error is
		// best-effort like the other queries above, never a failed page.
		tillRegisterID := ""
		if resolved, resolveErr := pos.ResolveTillRegisterID(r.Context(), d.Db, d.Settings); resolveErr == nil {
			tillRegisterID = resolved
		} else if !errors.Is(resolveErr, pos.ErrRegisterIdentityAmbiguous) {
			logging.L().Errorf("resolve till register: %v", resolveErr)
		}
		// Listed AFTER resolving, so a register the resolver just
		// self-created on an empty shop shows up as an option too.
		registers, registersErr := data.NewPOSRepo(d.Db).ListRegisters(r.Context())
		if registersErr != nil {
			logging.L().Errorf("list registers: %v", registersErr)
		}
		// Quarantined LAN-sync journal entries (ut-docs#1133, ADR-0065
		// follow-up): a primary-only concept (InsertJournalQuarantine only
		// ever runs on the till applying a replica's pushed batch), so this
		// stays 0 on a replica rather than querying at all — best-effort
		// like the other reads on this page, a count error just hides the
		// card's warning line instead of failing the page.
		isPrimaryTill := d.SyncPrimaryURL(r.Context()) == ""
		var quarantineCount int
		var hasEnrolledTills bool
		if isPrimaryTill {
			if n, qErr := data.NewPOSRepo(d.Db).CountJournalQuarantine(r.Context()); qErr != nil {
				logging.L().Errorf("count sync journal quarantine: %v", qErr)
			} else {
				quarantineCount = n
			}
			// ut-docs#1133 review: a single-till shop that has never
			// enrolled a replica cannot structurally have a quarantine row
			// (InsertJournalQuarantine only ever fires while applying a
			// REPLICA's pushed batch) — showing the quarantine help text
			// and "View quarantined entries" button there is premature
			// noise contradicting the nav chip's own "nothing to sync yet"
			// design. Once a replica has ever joined, keep showing it even
			// after every replica is later revoked: quarantineCount alone
			// (checked in the template) still covers that case.
			if list, tillsErr := data.NewTillsRepo(d.Db).ListTills(r.Context()); tillsErr != nil {
				logging.L().Errorf("list tills: %v", tillsErr)
			} else {
				hasEnrolledTills = len(list) > 0
			}
		}
		// ListTills only returns CURRENTLY enrolled tills, so this must be
		// an OR, not hasEnrolledTills alone: a shop that revoked the one
		// replica that ever produced a quarantine row would otherwise hide
		// the section again right as it matters most (the same "signal
		// vanishes when the misbehaving till is revoked" gap the nav chip
		// fix above closes).
		showQuarantineSection := hasEnrolledTills || quarantineCount > 0
		// Barcode symbology checklist (ADR-0059 Decision §2, ut-docs#935):
		// every internal/barcode registry entry, plus whether this shop
		// currently has it enabled. Best-effort like the other reads on
		// this page — EnabledBarcodeSymbologies already falls back to the
		// compatibility-preserving default set on error, so the checklist
		// still renders (with the pre-ADR-0059 defaults) rather than the
		// whole page failing.
		type symbologyRow struct {
			ID      string
			NameKey string
			Enabled bool
		}
		barcodeReg := barcode.Default()
		enabledSymbologies, enabledErr := data.NewSettingsRepo(d.Db).EnabledBarcodeSymbologies(r.Context())
		if enabledErr != nil {
			logging.L().Errorf("enabled barcode symbologies: %v", enabledErr)
		}
		enabledSymbologySet := make(map[string]bool, len(enabledSymbologies))
		for _, id := range enabledSymbologies {
			enabledSymbologySet[id] = true
		}
		barcodeSymbologies := make([]symbologyRow, 0, len(barcodeReg.IDs()))
		for _, id := range barcodeReg.IDs() {
			sym, _ := barcodeReg.Lookup(id)
			barcodeSymbologies = append(barcodeSymbologies, symbologyRow{ID: sym.ID, NameKey: sym.NameKey, Enabled: enabledSymbologySet[id]})
		}
		// ut-docs#1060: shared with GET /ui/settings/window-mode-status
		// below via windowControlTopology, so both computations agree.
		shellAttached, piKioskAppliance := windowControlTopology(d)
		isManager := canPerform(d, r, "settings")
		pendingBasePluginRows := pendingBasePluginViews(pendingBasePlugins)
		restorePromptDeferred := restorePromptStatus == common.RestorePromptStatusDeferred
		// ut-docs#1913: the same four conditions that gate whether
		// settings-issuereport/settings-menulayout/settings-payments/
		// settings-data/settings-all actually RENDER their `.card` this
		// request (below), restated so settingsNav's filter can drop a
		// gated row from the sidebar index too — otherwise a cashier with
		// nothing pending would see the Data card's title leak into
		// #settings-nav-index even though its own `.card` never renders
		// (TestSettingsPage_DataCardHiddenFromCashierWhenNothingPending's
		// whole point: the heading text must not appear in the body at
		// all, not merely be visually hidden).
		showDataCard := isManager || sampleCount > 0 || len(pendingBasePluginRows) > 0 || restorePromptDeferred
		data := map[string]any{
			"title":       "Settings",
			"theme":       st.Theme,
			"themes":      availableThemes(r.Context(), d),
			"settings":    st,
			"settingsMap": all,
			"menuItems":   d.MenuSnapshot(),
			"uiScale":     strconv.FormatFloat(scale, 'f', -1, 64),
			"isManager":   isManager,
			// ut-docs#1537: will the Android install endpoint accept this
			// caller's session on its own, or is it going to demand a PIN?
			// Rendered up front so a cashier (or anyone on a self-order kiosk)
			// sees the PIN field immediately, instead of submitting once to
			// find out. Mirrors update_api.go's own decision exactly — if the
			// two ever disagree the 403 branch still reveals the field, so a
			// drift here degrades to the old extra round trip, never to a
			// bypass.
			"androidUpdateSessionAuth": androidUpdateSessionAuthorizes(d, r),
			"printer":                  printerConfig(r.Context(), d),
			// ADR-0089 Decision 3: the interim Germany carve-out locks the
			// receipt-policy control to "always" — read from the same
			// settings row the save handler and printerConfig key off, not
			// CurrentState, so a country changed via /api/settings/upsert in
			// this same session renders consistently with what the save
			// handler will actually accept.
			"receiptPolicyLocked": receiptPolicyLockedForCountry(all[common.KeyCountry]),
			"backups":             listBackupsForUI(d, locale),
			// ut-docs#1613: a restore staged in an earlier visit (or before
			// a page reload) must still offer its restart trigger here —
			// otherwise the operator who reloads mid-flow lands back on the
			// exact dead end this card exists to remove, with the restore
			// still silently staged on disk (db.PendingRestore is a
			// persistent, on-disk fact; nothing else re-checks it once the
			// original POST response is gone).
			"restorePending":         backupRestorePending(d.Cfg.DBPath),
			"restartSupported":       backupRestartSupported(),
			"payMethods":             payMethods,
			"payDefault":             payDefault,
			"payFees":                feeRows,
			"exportEntries":          exportEntries,
			"autoUpdateEnabled":      autoUpdateEnabled == "true",
			"autoUpdateTime":         autoUpdateTime,
			"TillName":               tillNameOrDefault(r.Context(), d, locale),
			"TillRegisterID":         tillRegisterID,
			"registers":              registers,
			"IsPrimaryTill":          isPrimaryTill,
			"QuarantineCount":        quarantineCount,
			"ShowQuarantineSection":  showQuarantineSection,
			"reportRetentionMode":    reportRetentionMode,
			"reportArchiveCoverage":  reportArchiveCoverage,
			"shopType":               shopType,
			"shopTypes":              setupShopTypes,
			"restorePromptDeferred":  restorePromptDeferred,
			"pendingBasePlugins":     pendingBasePluginRows,
			"tseProvisioning":        tseProvisioningViewFor(tseState),
			"tseRetryable":           tseProvisioningRetryable(tseState),
			"tseCanDismiss":          !tseProvisioningDismissBlocked(tseState),
			"missingFiscalSigner":    missingSigner,
			"missingTaxRateSwitcher": missingSwitcher,
			"resetBatches":           resetBatches,
			"sampleCount":            sampleCount,
			"windowMode":             st.WindowMode,
			"launchOnStartup":        st.LaunchOnStartup,
			// shellAttached + piKioskAppliance (ADR-0064, ut-docs#1039;
			// finding 8 of its review): which of the three window-control
			// topologies this till is actually in, so the Display card can
			// say the true thing in each — a desktop shell holding a live
			// control poll; a Pi kiosk appliance (kiosk is real and
			// systemd-driven, no desktop app will ever run); or neither,
			// where chrome-hiding modes stay off (the fail-closed
			// downgrade) until the desktop app runs. The appliance case is
			// read off the controller pages.Init actually wired — the one
			// fact that decides where exit-to-os and apply really go.
			// Nil-safe for bare-Deps tests.
			"shellAttached":      shellAttached,
			"piKioskAppliance":   piKioskAppliance,
			"barcodeSymbologies": barcodeSymbologies,
			// ADR-0088, ut-docs#1913: the sidebar's resolved order/label/
			// grouping (core defaults + any active `layout` plugin's
			// Settings-slot amendments) — see settingsnav's own doc comment
			// for why this resolves the SIDEBAR only, not the on-page card
			// content/order.
			"settingsNav": filterSettingsNavForRender(
				settingsnav.Resolve(locale, d.SettingsAmendmentsSnapshot()),
				isManager, len(payMethods) > 0, showDataCard,
			),
		}
		httpx.Render("ui/pages/settings.html", data)(w, r)
	})

	// GET /ui/settings/window-mode-status (ut-docs#1060): the fragment
	// window_mode_status.html's own root now self-polls, so the Display
	// card's status note stays true after a desktop shell attaches or
	// detaches while Settings is already open, instead of only being
	// correct at the moment of page load. Same auth tier as the rest of
	// /settings (normal signed-in session -- this is nested INSIDE an
	// already-authenticated page, unlike GET /api/window-mode, the shell's
	// own pre-login control channel this deliberately does not touch). No
	// PIN/manager gate: this is the exact same read-only topology fact the
	// full page already shows any signed-in user.
	mux.HandleFunc("GET /ui/settings/window-mode-status", func(w http.ResponseWriter, r *http.Request) {
		shellAttached, piKioskAppliance := windowControlTopology(d)
		httpx.RenderPartial("ui/partials/window_mode_status.html", map[string]any{
			"shellAttached":    shellAttached,
			"piKioskAppliance": piKioskAppliance,
		})(w, r)
	})

	// Preferred payment method: leads the tender UI (ADR-0016 manual mode —
	// the shop's cheaper/house provider is the one-tap default).
	mux.HandleFunc("POST /api/settings/payments-default", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// ParseForm moved ahead of the gate solely so override_pin is
		// readable — it validates nothing, so an already-authorized
		// request is checked exactly as before (ut-docs#796).
		_ = r.ParseForm()
		method := strings.TrimSpace(r.Form.Get("method"))
		// Mutating + audit-writing (ut-docs#796, mechanism ut-docs#557): a
		// denied session gets an in-place PIN re-auth instead of the flat
		// forbidden span.
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/payments-default", "#pay-default-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.payments_default"), method),
				[]elevationHiddenField{{Name: "method", Value: method}}, elev)
			return
		}
		if err := d.Settings.Set(r.Context(), "payments.default_method", method); err != nil {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		settingsAudit(r, posRepo, elev, "settings", "payments.default_method", "payments_default_changed",
			map[string]any{"method": method})
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "plugins.settings.saved"))
	})

	// Order-number scheme (ut-docs#1817): how NextDisplayNo counts the
	// short, customer-facing order number -- a SEPARATE setting from
	// receipt_no, which this never touches. Same elevation+audit shape as
	// payments-default just above.
	mux.HandleFunc("POST /api/settings/order-no-scheme", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = r.ParseForm()
		scheme := strings.TrimSpace(r.Form.Get("scheme"))
		if scheme != data.DisplayNoSchemeTradingPeriodReset && scheme != data.DisplayNoSchemeLifetimeNoReset {
			http.Error(w, "scheme must be trading_period_reset or lifetime_no_reset", http.StatusBadRequest)
			return
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/order-no-scheme", "#order-no-scheme-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.order_no_scheme"), httpx.T(locale, "settings.order_no.scheme_"+scheme)),
				[]elevationHiddenField{{Name: "scheme", Value: scheme}}, elev)
			return
		}
		if err := d.Settings.Set(r.Context(), data.SaleDisplayNoSchemeKey, scheme); err != nil {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		settingsAudit(r, posRepo, elev, "settings", data.SaleDisplayNoSchemeKey, "order_no_scheme_changed",
			map[string]any{"scheme": scheme})
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "plugins.settings.saved"))
	})

	// Per-provider fee rules (B4): percent + fixed per transaction, feeding
	// the checkout cost hints. Stored as JSON per method.
	mux.HandleFunc("POST /api/settings/payments-fee", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Validate BEFORE checking elevation (permission_settings_page.go's
		// reviewed convention, ut-docs#557): burning a manager's live PIN
		// entry on a request that was always going to reject is a needless
		// cost.
		_ = r.ParseForm()
		method := strings.TrimSpace(r.Form.Get("method"))
		if method == "" {
			fmt.Fprintf(w, `<span class="error">✗ method</span>`)
			return
		}
		pct, _ := strconv.ParseFloat(strings.TrimSpace(r.Form.Get("percent")), 64)
		fixedMaj, _ := strconv.ParseFloat(strings.TrimSpace(r.Form.Get("fixed")), 64)
		if pct < 0 || fixedMaj < 0 || pct > 100 {
			fmt.Fprintf(w, `<span class="error">✗ range</span>`)
			return
		}
		// Mutating + audit-writing (ut-docs#796): in-place PIN re-auth
		// instead of the flat forbidden span. Numbers pre-formatted in Go
		// so every locale's summary string takes plain %s args.
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/payments-fee", "#fee-msg-"+method,
				fmt.Sprintf(httpx.T(locale, "elevation.summary.payments_fee"), method,
					strconv.FormatFloat(pct, 'f', -1, 64), strconv.FormatFloat(fixedMaj, 'f', -1, 64)),
				[]elevationHiddenField{
					{Name: "method", Value: method},
					{Name: "percent", Value: r.Form.Get("percent")},
					{Name: "fixed", Value: r.Form.Get("fixed")},
				}, elev)
			return
		}
		bp := int64(math.Round(pct * 100)) // basis points, not money -- stays *100 regardless of currency
		// ut-docs#1400: currency.Decimals-aware, not a hardcoded *100 -- a
		// hardcoded conversion stored a 100x-too-large fee on a 0-decimal
		// shop (IRR/IRT/IQD/AFN/JPY).
		fixedMinor := httpx.MinorFromMajor(fixedMaj, httpx.ActiveCurrency().Decimals)
		raw, _ := json.Marshal(map[string]int64{
			"bp":    bp,
			"fixed": fixedMinor,
		})
		if err := d.Settings.Set(r.Context(), "payments.fee."+method, string(raw)); err != nil {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		settingsAudit(r, posRepo, elev, "settings", "payments.fee."+method, "payments_fee_changed",
			map[string]any{"method": method, "percent_bp": bp, "fixed_minor": fixedMinor})
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "plugins.settings.saved"))
	})

	// Immediate marketplace registration attempt (the Settings card's
	// "Register now" button). Registration also runs automatically in the
	// background; this just gives the operator a button and instant feedback.
	// "Claim this store" (ADR-0013 layer 2): mint a short-lived claim code
	// on the marketplace and show it with the redemption link. The owner
	// signs in with their Universal Till ID and enters the code.
	mux.HandleFunc("POST /api/enrol/claim-code", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// ut-docs#865: same checkOrElevate/InsertAuditElevated mechanism as
		// #796 — a denied session gets an in-place PIN re-auth instead of the
		// flat forbidden span. No other form fields, so ParseForm only reads
		// override_pin.
		_ = r.ParseForm()
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/enrol/claim-code", "#claim-code-msg",
				httpx.T(locale, "elevation.summary.enrol_claim_code"), nil, elev)
			return
		}
		info, err := enroll.ClaimCode(r.Context(), d.Cfg)
		if err != nil {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		settingsAudit(r, posRepo, elev, "enrollment", "-", "claim_code_generated", nil)
		// QR of the claim URL: the owner scans it and claims FROM THEIR
		// PHONE — the only escape hatch on shells that can't open an
		// external browser (Pi kiosk, windows/linux webview).
		qrHTML := ""
		if png, err := qrcode.Encode(info.ClaimURL, qrcode.Medium, 180); err == nil {
			qrHTML = fmt.Sprintf(
				`<div class="claim-qr"><img src="data:image/png;base64,%s" alt="" width="180" height="180">`+
					`<div class="muted">%s</div></div>`,
				base64.StdEncoding.EncodeToString(png),
				html.EscapeString(httpx.T(locale, "settings.enrol.claim_scan")))
		}
		fmt.Fprintf(w,
			`<div class="claim-code-box"><div class="claim-code">%s</div>`+
				`<div class="muted">%s</div>`+
				`<a href="%s" target="_blank" rel="noopener">%s</a>%s</div>`,
			html.EscapeString(info.Code),
			html.EscapeString(httpx.T(locale, "settings.enrol.claim_expires")),
			html.EscapeString(info.ClaimURL),
			html.EscapeString(httpx.T(locale, "settings.enrol.claim_open")),
			qrHTML)
	})

	mux.HandleFunc("POST /api/enrol/now", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Always answer 200: this is an hx-swap target, and HTMX silently drops
		// non-2xx responses — a 403/502 here is exactly why the button looked
		// dead. The message carries the outcome (and the reason on failure).
		// ut-docs#865: checkOrElevate/InsertAuditElevated (#557/#796) in
		// place of the flat forbidden span. No other form fields, so
		// ParseForm only reads override_pin.
		_ = r.ParseForm()
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/enrol/now", "#enrol-msg",
				httpx.T(locale, "elevation.summary.enrol_now"), nil, elev)
			return
		}
		status, err := enroll.RegisterNow(r.Context(), d.Cfg, d.Settings)
		if err != nil || !status.Registered {
			// Show the concrete reason (and the endpoint we tried) so the
			// operator can see e.g. an unreachable/misconfigured marketplace.
			reason := httpx.T(locale, "settings.enrol.not_registered")
			if err != nil {
				reason = err.Error()
			}
			endpoint := enroll.Effective(d.Cfg).Marketplace.EndpointURL
			fmt.Fprintf(w, `<span class="error">❌ %s: %s (%s)</span>`,
				httpx.T(locale, "settings.enrol.failed"), reason, endpoint)
			return
		}
		settingsAudit(r, posRepo, elev, "enrollment", status.StoreID, "enrol_now_registered", map[string]any{"store_id": status.StoreID})
		fmt.Fprintf(w, `<span>✅ %s — <code>%s</code></span>`,
			httpx.T(locale, "settings.enrol.registered"), status.StoreID)
	})

	// The store's fleet: every till registered under this store. Lazy-loaded
	// (a marketplace call) so it never blocks the settings page; failure just
	// shows "unavailable" — offline-first.
	mux.HandleFunc("GET /api/enrol/devices", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !canPerform(d, r, "settings") {
			fmt.Fprintf(w, `<span class="muted">%s</span>`, httpx.T(locale, "settings.enrol.forbidden"))
			return
		}
		devices, err := enroll.Fleet(r.Context(), d.Cfg)
		if err != nil {
			fmt.Fprintf(w, `<span class="muted">%s</span>`, httpx.T(locale, "settings.enrol.fleet_unavailable"))
			return
		}
		if len(devices) == 0 {
			fmt.Fprintf(w, `<span class="muted">%s</span>`, httpx.T(locale, "settings.enrol.fleet_empty"))
			return
		}
		var b strings.Builder
		fmt.Fprintf(&b, `<p class="muted">%s (%d)</p><ul style="margin:.2rem 0; padding-inline-start:1.1rem">`,
			httpx.T(locale, "settings.enrol.fleet_title"), len(devices))
		for _, dev := range devices {
			name := dev.Name
			if name == "" {
				name = dev.DeviceID
			}
			fmt.Fprintf(&b, `<li>%s <code class="muted">%s</code></li>`,
				html.EscapeString(name), html.EscapeString(shortDeviceID(dev.DeviceID)))
		}
		b.WriteString(`</ul>`)
		_, _ = w.Write([]byte(b.String()))
	})

	// Eager-registration opt-in toggle (ADR-0071, ut-docs#879): the same
	// choice the setup wizard's last screen asks, changeable afterwards.
	// Elevation-gated like POST /api/enrol/now above; audited like the
	// telemetry toggle. Toggling ON also fires ONE best-effort, time-boxed
	// EnsureRegistered attempt (register now, not just "arm a future
	// trigger"); toggling OFF only stops future eager triggers — it never
	// deregisters an identity already minted (no such flow exists).
	mux.HandleFunc("POST /api/settings/auto-register", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		optIn := "false"
		if r.Form.Get("optIn") == "on" || r.Form.Get("optIn") == "1" {
			optIn = "true"
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			summaryKey := "elevation.summary.auto_register_off"
			if optIn == "true" {
				summaryKey = "elevation.summary.auto_register_on"
			}
			renderElevationPrompt(w, r, "/api/settings/auto-register", "#auto-register-msg",
				httpx.T(locale, summaryKey),
				[]elevationHiddenField{{Name: "optIn", Value: r.Form.Get("optIn")}}, elev)
			return
		}
		if err := d.Settings.Set(r.Context(), common.KeyAutoRegisterOptIn, optIn); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "settings.error.save_failed", "settings_auto_register", err)
			return
		}
		settingsAudit(r, posRepo, elev, "settings", common.KeyAutoRegisterOptIn, "auto_register_opt_in_changed", map[string]any{"opt_in": optIn})
		if optIn == "true" {
			// Best-effort and bounded, exactly like the wizard's own opt-in
			// attempt (autoRegisterForSetup): EnsureRegistered logs and
			// swallows its own failure, and the response below reports the
			// SETTING saved — registration status stays this page's
			// enrolment card's job, refreshed by the success reload.
			attemptCtx, cancel := context.WithTimeout(r.Context(), autoRegisterAttemptTimeout)
			enroll.EnsureRegistered(attemptCtx, d.Cfg, d.Settings)
			cancel()
		}
		settingsRespondSaved(w, r, elev)
	})

	// Idle auto-lock window (docs: pos-auth.md). Manager/admin only — an
	// unattended till's security posture is not a cashier decision.
	// ut-docs#865: checkOrElevate/InsertAuditElevated (#557/#796) — range
	// validation runs BEFORE elevation (established convention: a value that
	// would be rejected anyway must not burn an approver's live PIN entry).
	mux.HandleFunc("POST /api/settings/idle-lock", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		n, err := strconv.Atoi(strings.TrimSpace(r.Form.Get("minutes")))
		if err != nil || n < 0 || n > 480 {
			http.Error(w, "minutes must be between 0 and 480", http.StatusBadRequest)
			return
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			label := httpx.T(locale, "settings.idle_lock.off")
			if n != 0 {
				label = fmt.Sprintf("%d %s", n, httpx.T(locale, "settings.idle_lock.minutes"))
			}
			renderElevationPrompt(w, r, "/api/settings/idle-lock", "#idle-lock-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.idle_lock"), label),
				[]elevationHiddenField{{Name: "minutes", Value: r.Form.Get("minutes")}}, elev)
			return
		}
		st := d.CurrentState()
		st.IdleLockMinutes = n
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		d.SetState(st)
		d.AuthSvc.SetIdleLockMinutes(n)
		if !auth.Disabled(os.Getenv("UT_AUTH")) {
			httpx.InitIdleLock(n)
		}
		settingsAudit(r, posRepo, elev, "settings", common.KeyIdleLock, "idle_lock_changed", map[string]any{"minutes": n})
		settingsRespondSaved(w, r, elev)
	})

	// Self-order kiosk idle-reset window (ADR-0020): distinct from the
	// idle-lock above — the kiosk route is auth-exempt (no session to
	// revoke), so this is purely a client-side "reload to the start
	// screen" timer, read at render time. Manager/admin only.
	// ut-docs#865: checkOrElevate/InsertAuditElevated (#557/#796), same
	// validation-before-elevation ordering as idle-lock above.
	mux.HandleFunc("POST /api/settings/kiosk-idle-reset", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		n, err := strconv.Atoi(strings.TrimSpace(r.Form.Get("seconds")))
		if err != nil || n < 0 || n > 600 {
			http.Error(w, "seconds must be between 0 and 600", http.StatusBadRequest)
			return
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			label := httpx.T(locale, "settings.kiosk_idle_reset.off")
			if n != 0 {
				label = fmt.Sprintf("%d %s", n, httpx.T(locale, "settings.kiosk_idle_reset.seconds"))
			}
			renderElevationPrompt(w, r, "/api/settings/kiosk-idle-reset", "#kiosk-idle-reset-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.kiosk_idle_reset"), label),
				[]elevationHiddenField{{Name: "seconds", Value: r.Form.Get("seconds")}}, elev)
			return
		}
		st := d.CurrentState()
		st.KioskIdleResetSeconds = n
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		d.SetState(st)
		settingsAudit(r, posRepo, elev, "settings", common.KeyKioskIdleReset, "kiosk_idle_reset_changed", map[string]any{"seconds": n})
		settingsRespondSaved(w, r, elev)
	})

	// Self-order kiosk payment mode (ut-docs#582): "kiosk" (default --
	// ADR-0020's card/contactless payment picker) or "counter" ("pay at
	// counter" -- the kiosk takes the order and sends it to the kitchen but
	// never charges anything; a human takes payment at the till
	// afterwards, so counter-mode checkout creates no sale/payment row at
	// all -- see internal/data/kiosk_counter_orders_repo.go). Manager/admin
	// only, same elevation gate as every other kiosk setting on this page.
	// ut-docs#865: checkOrElevate/InsertAuditElevated, same validation-
	// before-elevation ordering as kiosk-idle-reset just above.
	mux.HandleFunc("POST /api/settings/kiosk-payment-mode", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		mode := strings.TrimSpace(r.Form.Get("mode"))
		switch mode {
		case common.KioskPaymentModeKiosk, common.KioskPaymentModeCounter:
		default:
			http.Error(w, "mode must be kiosk or counter", http.StatusBadRequest)
			return
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/kiosk-payment-mode", "#kiosk-payment-mode-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.kiosk_payment_mode"), httpx.T(locale, "settings.kiosk.payment_mode."+mode)),
				[]elevationHiddenField{{Name: "mode", Value: mode}}, elev)
			return
		}
		st := d.CurrentState()
		st.KioskPaymentMode = mode
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		d.SetState(st)
		settingsAudit(r, posRepo, elev, "settings", common.KeyKioskPaymentMode, "kiosk_payment_mode_changed", map[string]any{"mode": mode})
		settingsRespondSaved(w, r, elev)
	})

	// Window mode (ut-docs#608 scaffold, #883 for the Pi kiosk path): stores
	// the till's window/process display mode AND applies it via WindowCtl.
	// Real OS effect today: the Pi headless kiosk (#883, immediately, no
	// restart) and the Linux desktop shell (immediately, in attach AND
	// spawn mode, over the shell's polled control channel — ADR-0064,
	// ut-docs#1039; ut-docs#882's env-handed channel survives as the
	// spawn-mode fallback for a shell too old to poll, and #611's
	// next-launch apply still covers a shell that isn't running at all).
	// Still scaffolding-only on macOS (#609) and Windows (#610): their
	// shells have no real applyWindowMode, so they never claim control=live
	// and GET /api/window-mode serves them "normal" for the chrome-hiding
	// modes (the ADR-0064 fail-closed downgrade) — a toggle there is
	// accepted (204) and persists, but the window deliberately stays
	// normal, never a fullscreen it can't leave.
	// ut-docs#865: checkOrElevate/InsertAuditElevated (#557/#796),
	// validation before elevation as elsewhere in this file.
	mux.HandleFunc("POST /api/settings/window-mode", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		mode := strings.TrimSpace(r.Form.Get("mode"))
		switch mode {
		case "fullscreen", "kiosk", "maximized", "normal":
		default:
			http.Error(w, "mode must be one of fullscreen, kiosk, maximized, normal", http.StatusBadRequest)
			return
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/window-mode", "#window-mode-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.window_mode"), httpx.T(locale, "settings.display.window_mode_"+mode)),
				[]elevationHiddenField{{Name: "mode", Value: mode}}, elev)
			return
		}
		// ut-docs#883: apply BEFORE persisting — on the Pi kiosk path this is
		// what actually flips unitill-kiosk.service; if it fails (e.g. a
		// pre-#883 Pi upgraded without the sudoers grant), the operator sees
		// a clear error and the stored preference never lies about what the
		// OS actually did. That guarantee is real for KioskSystemdWindowController
		// (a synchronous systemctl call that genuinely fails or succeeds) but
		// deliberately not for common.ShellPollWindowController (ADR-0064,
		// the desktop-shell default): its ApplyMode publishes the new live
		// mode and always returns nil — persisting a preference while no
		// shell is attached is legitimate (configure now, launch the shell
		// later), and the window-state endpoint's fail-closed downgrade
		// guarantees a saved-but-unapplied chrome-hiding mode can never
		// become a trap. WindowCtl is set in pages.Init; nil-checked here
		// so bare-Deps tests/helpers that predate ut-docs#608 stay valid,
		// same convention as the exit-to-os handler below.
		wc := d.WindowCtl
		if wc == nil {
			wc = common.NoopWindowController{}
		}
		if err := wc.ApplyMode(mode); err != nil {
			logging.L().Errorf("window mode apply %s: %v", mode, err)
			http.Error(w, "could not apply window mode", http.StatusInternalServerError)
			return
		}
		st := d.CurrentState()
		st.WindowMode = mode
		st.WindowModeChanged = true // ut-docs#1555: this save DOES mean to change it
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		d.SetState(st)
		settingsAudit(r, posRepo, elev, "settings", common.KeyWindowMode, "window_mode_changed", map[string]any{"mode": mode})
		settingsRespondSaved(w, r, elev)
	})

	// Launch-on-startup (ut-docs#608 scaffold): stores/surfaces the till's
	// autostart-on-boot preference. OS-level application (ut-docs#611) is
	// deliberately NOT done here: this handler runs inside unitill-pos, a
	// separate OS process from the desktop shell (unitill-desktop) that
	// owns the actual window/autostart mechanism and — critically — is the
	// process actually running as the interactive desktop user (on a .deb
	// install, unitill-pos runs as the unprivileged system user `pos`,
	// which has no meaningful XDG autostart directory of its own). The
	// shell reads this persisted value itself via GET /api/window-mode and
	// reconciles its own autostart entry at its own next launch — the same
	// next-launch semantics window-mode already uses, and for the same
	// reason (#549 explicitly allows either). See GET /api/window-mode
	// (window_state_api.go) and cmd/unitill-desktop/autostart_linux.go.
	// ut-docs#865: checkOrElevate/InsertAuditElevated (#557/#796). Two
	// separate summary keys (on/off) rather than a %s placeholder — no
	// generic "on"/"off" i18n key exists yet (mirrors eod_settings_enabled/
	// eod_settings_disabled's precedent pair in eod_api.go).
	mux.HandleFunc("POST /api/settings/launch-on-startup", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		b, err := strconv.ParseBool(strings.TrimSpace(r.Form.Get("enabled")))
		if err != nil {
			http.Error(w, "enabled must be a boolean", http.StatusBadRequest)
			return
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			summaryKey := "elevation.summary.launch_on_startup_off"
			if b {
				summaryKey = "elevation.summary.launch_on_startup_on"
			}
			renderElevationPrompt(w, r, "/api/settings/launch-on-startup", "#launch-on-startup-msg",
				httpx.T(locale, summaryKey),
				[]elevationHiddenField{{Name: "enabled", Value: r.Form.Get("enabled")}}, elev)
			return
		}
		st := d.CurrentState()
		st.LaunchOnStartup = b
		st.LaunchOnStartupChanged = true // ut-docs#1555: this save DOES mean to change it
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		d.SetState(st)
		settingsAudit(r, posRepo, elev, "settings", common.KeyLaunchOnStartup, "launch_on_startup_changed", map[string]any{"enabled": b})
		settingsRespondSaved(w, r, elev)
	})

	// ut-docs#1356: whether a fresh catalog-import preview's "derive a
	// barcode from SKU" checkbox (ut-docs#1224, import_page.go) starts
	// pre-ticked for this shop. Same manager-gated, elevation-wired,
	// persist-a-bool shape as launch-on-startup above — this key is read
	// only to choose that checkbox's initial state, never to change what an
	// import/backfill actually does (the operator's own explicit submit
	// always decides that).
	mux.HandleFunc("POST /api/settings/catalog-import-barcode-default", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		b, err := strconv.ParseBool(strings.TrimSpace(r.Form.Get("enabled")))
		if err != nil {
			http.Error(w, "enabled must be a boolean", http.StatusBadRequest)
			return
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			summaryKey := "elevation.summary.catalog_import_barcode_default_off"
			if b {
				summaryKey = "elevation.summary.catalog_import_barcode_default_on"
			}
			renderElevationPrompt(w, r, "/api/settings/catalog-import-barcode-default", "#catalog-import-barcode-default-msg",
				httpx.T(locale, summaryKey),
				[]elevationHiddenField{{Name: "enabled", Value: r.Form.Get("enabled")}}, elev)
			return
		}
		val := "0"
		if b {
			val = "1"
		}
		if err := d.Settings.Set(r.Context(), data.CatalogImportBarcodeFromSKUDefaultKey, val); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		settingsAudit(r, posRepo, elev, "settings", data.CatalogImportBarcodeFromSKUDefaultKey, "catalog_import_barcode_default_changed", map[string]any{"enabled": b})
		settingsRespondSaved(w, r, elev)
	})

	// "Sell items without tracking stock" (ut-docs#1843). Same manager-
	// gated, elevation-wired, persist-a-bool shape as launch-on-startup
	// above, but this one changes what the till DOES, not just what it
	// remembers: CompleteSale's stock guard rejects any line whose item
	// has no inventory row (a missing row reads as quantity 0, so 0-1 < 0),
	// and the German pilot merchant's 116-item SumUp catalogue has no
	// inventory rows at all because his source system says "Track
	// inventory? No" for every one of them. The capability was complete on
	// the server and reachable only by hand-editing the settings table —
	// the same "backend done, no UI" shape as the voucher screens in
	// ut-docs#1832.
	//
	// SaveState-then-SetState (not UpdateState) on purpose, per
	// ut-docs#157: a failed persist must never become the in-memory state
	// and ride along on the next unrelated successful save.
	mux.HandleFunc("POST /api/settings/allow-negative-inventory", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		b, err := strconv.ParseBool(strings.TrimSpace(r.Form.Get("enabled")))
		if err != nil {
			http.Error(w, "enabled must be a boolean", http.StatusBadRequest)
			return
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			summaryKey := "elevation.summary.allow_negative_inventory_off"
			if b {
				summaryKey = "elevation.summary.allow_negative_inventory_on"
			}
			renderElevationPrompt(w, r, "/api/settings/allow-negative-inventory", "#allow-negative-inventory-msg",
				httpx.T(locale, summaryKey),
				[]elevationHiddenField{{Name: "enabled", Value: r.Form.Get("enabled")}}, elev)
			return
		}
		st := d.CurrentState()
		st.AllowNegativeInventory = b
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		d.SetState(st)
		settingsAudit(r, posRepo, elev, "settings", common.KeyAllowNegativeInventory, "allow_negative_inventory_changed", map[string]any{"enabled": b})
		settingsRespondSaved(w, r, elev)
	})

	// Barcode symbology checklist (ADR-0059 Decision §2, ut-docs#935): one
	// checkbox per internal/barcode registry entry, persisted immediately
	// via SettingsRepo.SetEnabledBarcodeSymbologies — same manager-gated,
	// audit-writing, elevation-wired shape as launch-on-startup above.
	mux.HandleFunc("POST /api/settings/barcode-symbology", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		id := strings.TrimSpace(r.Form.Get("id"))
		sym, ok := barcode.Default().Lookup(id)
		if !ok {
			http.Error(w, "unknown barcode symbology", http.StatusBadRequest)
			return
		}
		b, err := strconv.ParseBool(strings.TrimSpace(r.Form.Get("enabled")))
		if err != nil {
			http.Error(w, "enabled must be a boolean", http.StatusBadRequest)
			return
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			// The elevation summary names the symbology by its localized
			// display name (via the registry's own NameKey, ut-docs#935
			// review finding MINOR 5 — not a hand-rebuilt "barcode.symbology."+id
			// string, which would silently diverge if a future entry's
			// NameKey ever doesn't follow that convention) — the raw id
			// (e.g. "EAN13_WEIGHT_PREFIX2X") is meaningless to an approving
			// manager, the display name is exactly what the checklist
			// itself shows them.
			summaryKey := "elevation.summary.barcode_symbology_off"
			if b {
				summaryKey = "elevation.summary.barcode_symbology_on"
			}
			renderElevationPrompt(w, r, "/api/settings/barcode-symbology", "#barcode-symbologies-msg",
				fmt.Sprintf(httpx.T(locale, summaryKey), httpx.T(locale, sym.NameKey)),
				[]elevationHiddenField{
					{Name: "id", Value: id},
					{Name: "enabled", Value: r.Form.Get("enabled")},
				}, elev)
			return
		}
		settingsRepo := data.NewSettingsRepo(d.Db)
		if _, err := settingsRepo.SetBarcodeSymbologyEnabled(r.Context(), id, b); err != nil {
			if errors.Is(err, data.ErrEmptyBarcodeSymbologySet) {
				// ut-docs#935 review finding MAJOR 3: unticking the last
				// enabled symbology would leave every scan and every
				// untyped AddBarcode call matching nothing — refused
				// server-side, the only place that can actually guarantee
				// it (the client can't be the gate: the ten checkboxes are
				// independent, so no client-side "last one" check can see
				// concurrent state from another tab/till reliably).
				http.Error(w, "at least one barcode type must stay enabled", http.StatusBadRequest)
				return
			}
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		settingsAudit(r, posRepo, elev, "settings", data.BarcodeEnabledSymbologiesKey, "barcode_symbology_changed", map[string]any{"id": id, "enabled": b})
		settingsRespondSaved(w, r, elev)
	})

	// Exit to OS window (ut-docs#608 scaffold): a manager-session cookie
	// alone is NOT enough here (per the product owner's #549 comment thread
	// — "need someone with a right role... need pin") — this requires a LIVE
	// PIN, checked the same way as shifts_api.go's cash-adjustment/payout
	// handlers, INCLUDING that its PIN check stays live under UT_AUTH=off —
	// deliberately NOT mirroring those handlers' `auth.Disabled(...)` bypass.
	// The whole point of this endpoint is the live-PIN gate itself (there's
	// no "positive amount, no PIN needed" case here to bypass toward), and
	// the product owner's requirement was for a PIN check that can't be
	// switched off. The hook has real effect on Linux since ut-docs#882 (was
	// a no-op stub before that), but this means the action can't be
	// exercised under this repo's UT_AUTH=off dev/e2e convention until a
	// real manager PIN is seeded — expected, not a bug. A blank manager_pin
	// is rejected BEFORE calling AuthorizeManager,
	// which would otherwise burn a failed-attempt count shared device-wide
	// with keypad login (5 failures = 30s lockout) — the exact blank-PIN
	// lockout burn bug fixed there (see
	// TestExitToOSBlankPINRejectedWithoutBurningLockoutBudget below).
	mux.HandleFunc("POST /api/settings/exit-to-os", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		pin := strings.TrimSpace(r.Form.Get("manager_pin"))
		if pin == "" {
			http.Error(w, "manager PIN required", http.StatusForbidden)
			return
		}
		approver, err := d.AuthSvc.AuthorizeManager(r.Context(), pin)
		if err != nil {
			status := http.StatusForbidden
			if errors.Is(err, auth.ErrLockedOut) {
				status = http.StatusTooManyRequests
			}
			http.Error(w, "manager PIN required", status)
			return
		}
		// WindowCtl is set in pages.Init (common.NoopWindowController until
		// #609/#610/#611 wire a real one); nil-checked here so bare-Deps
		// tests/helpers that predate this field stay valid, same convention
		// as Deps.OrderStatus.
		wc := d.WindowCtl
		if wc == nil {
			wc = common.NoopWindowController{}
		}
		if err := wc.ExitToOS(); err != nil {
			logging.L().Errorf("exit to OS: %v", err)
			// ADR-0064 (ut-docs#1039): nothing can act on the window, or
			// the attached shell never acknowledged applying "normal" —
			// unavailability, not an internal fault: 503, with a marker
			// token in the body the settings page maps to the honest,
			// case-specific operator message (three distinct truths, one
			// status code):
			//
			//   kiosk_appliance — this till is a Pi kiosk appliance; there
			//       is no OS desktop, the window-mode toggle is the way
			//       out. Nothing changed; no audit row.
			//   not_confirmed — a shell was attached and the exit WAS
			//       signalled (the live mode is now "normal", which the
			//       shell will pick up from state on its next poll), but
			//       no applied=normal acknowledgement came back in time.
			//       The lockdown break did happen under a verified manager
			//       PIN, so it IS audited (review of ut-docs#1039,
			//       finding 3 — the old single "nothing changed" message
			//       was false here, and the real exit went unaudited).
			//   no_shell — no desktop shell attached, no fallback channel:
			//       genuinely nothing changed; no audit row (only a real
			//       exit is audited, the ut-docs#616 reasoning below).
			//
			// Order matters only for clarity — the three sentinels are
			// distinct, none wraps another.
			switch {
			case errors.Is(err, common.ErrNoOSDesktop):
				http.Error(w, "window control unavailable: kiosk_appliance", http.StatusServiceUnavailable)
			case errors.Is(err, common.ErrExitNotConfirmed):
				if aerr := posRepo.InsertAudit(r.Context(), nil, approver.ID, "settings", "-", "exit_to_os",
					map[string]any{"confirmed": false}, time.Now().UTC().Format(time.RFC3339), ""); aerr != nil {
					logging.L().Errorf("exit-to-os audit (unconfirmed): %v", aerr)
				}
				http.Error(w, "window control unavailable: not_confirmed", http.StatusServiceUnavailable)
			case errors.Is(err, common.ErrNoWindowControl):
				http.Error(w, "window control unavailable: no_shell", http.StatusServiceUnavailable)
			default:
				http.Error(w, "could not exit to OS", http.StatusInternalServerError)
			}
			return
		}
		// ut-docs#616: record who authorized breaking kiosk lockdown, now that
		// #611/#882 give this a real effect on Linux (harmless no-op prior to
		// that, but no accountability trail once it actually does something).
		// Only on success — a failed/blank/wrong PIN attempt above already
		// returned before reaching here, mirroring how other manager-gated
		// actions in this codebase don't audit-log the failure itself. Best-
		// effort like settingsAudit's own posture: the OS-exit already
		// happened, so a failed audit insert must not fail this response, but
		// it is logged rather than silently swallowed.
		if err := posRepo.InsertAudit(r.Context(), nil, approver.ID, "settings", "-", "exit_to_os", nil, time.Now().UTC().Format(time.RFC3339), ""); err != nil {
			logging.L().Errorf("exit-to-os audit: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Plugin telemetry opt-in (FR-013): off by default, manager-only —
	// gates internal/plugins.TelemetryClient.ReportNow's scheduler tick.
	// ut-docs#865: checkOrElevate/InsertAuditElevated (#557/#796). Two
	// summary keys (on/off), same reasoning as launch-on-startup above.
	mux.HandleFunc("POST /api/settings/telemetry", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		optIn := "false"
		if r.Form.Get("optIn") == "on" || r.Form.Get("optIn") == "1" {
			optIn = "true"
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			summaryKey := "elevation.summary.telemetry_off"
			if optIn == "true" {
				summaryKey = "elevation.summary.telemetry_on"
			}
			renderElevationPrompt(w, r, "/api/settings/telemetry", "#telemetry-msg",
				httpx.T(locale, summaryKey),
				[]elevationHiddenField{{Name: "optIn", Value: r.Form.Get("optIn")}}, elev)
			return
		}
		if err := d.Settings.Set(r.Context(), "marketplace.telemetry_opt_in", optIn); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "settings.error.save_failed", "settings_telemetry", err)
			return
		}
		settingsAudit(r, posRepo, elev, "settings", "marketplace.telemetry_opt_in", "telemetry_opt_in_changed", map[string]any{"opt_in": optIn})
		settingsRespondSaved(w, r, elev)
	})

	// Interface scale for this till's screen; saved and applied immediately.
	mux.HandleFunc("POST /api/settings/ui-scale", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f, err := strconv.ParseFloat(strings.TrimSpace(r.Form.Get("scale")), 64)
		if err != nil || f < 0.5 || f > 2.0 {
			http.Error(w, "scale must be between 0.5 and 2.0", http.StatusBadRequest)
			return
		}
		st := d.CurrentState()
		st.UIScale = f
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		d.SetState(st)
		httpx.InitUIScale(f)
		w.WriteHeader(http.StatusNoContent)
	})

	// On-screen keyboard mode for this till's screen (auto|on|off).
	mux.HandleFunc("POST /api/settings/osk", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mode := strings.TrimSpace(r.Form.Get("mode"))
		if mode != "auto" && mode != "on" && mode != "off" {
			http.Error(w, "mode must be auto, on or off", http.StatusBadRequest)
			return
		}
		st := d.CurrentState()
		st.OSKMode = mode
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		d.SetState(st)
		httpx.InitOSKMode(mode)
		w.WriteHeader(http.StatusNoContent)
	})

	// Device profile (ADR-0018/ADR-0020): register (default), back-office
	// manager station, or self-order kiosk — "/" becomes the reports page
	// (backoffice) or the locked customer-facing self-order flow
	// (self_order). Per-till (display.* never LAN-syncs), so one shop can
	// mix registers, a back-office device, and one or more kiosks.
	// ut-docs#865: checkOrElevate/InsertAuditElevated (#557/#796), validation
	// before elevation as elsewhere in this file. The display label is
	// resolved BEFORE the "register" -> "" collapse below, since "" has no
	// settings.display.mode_* translation of its own — used for the
	// approver-facing summary ONLY. The audit payload records rawMode (the
	// actual persisted value, matching every sibling handler's convention —
	// e.g. window-mode audits {"mode": mode}, not a localized label) —
	// review finding F2: auditing modeLabel made the trail locale-dependent
	// (the same action wrote a different string per operator UI language)
	// and, for "register", recorded a label for a value that isn't what
	// actually gets persisted (mode collapses to "").
	mux.HandleFunc("POST /api/settings/display-mode", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		mode := strings.TrimSpace(r.Form.Get("mode"))
		if mode != "register" && mode != "backoffice" && mode != "self_order" {
			http.Error(w, "mode must be register, backoffice, or self_order", http.StatusBadRequest)
			return
		}
		rawMode := mode
		modeLabel := httpx.T(locale, "settings.display.mode_"+mode)
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/display-mode", "#display-mode-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.display_mode"), modeLabel),
				[]elevationHiddenField{{Name: "mode", Value: mode}}, elev)
			return
		}
		if mode == "register" {
			mode = "" // empty = default register profile
		}
		if err := d.Settings.Set(r.Context(), "display.mode", mode); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		// ut-docs#2099: keep httpx's live "selforder" template flag
		// (record_dialog.html's status/lock/exit-to-OS withholding, §10)
		// in step with the mode that was JUST persisted — without this the
		// flag would only ever reflect the value pages.Init read at boot,
		// stale until the till restarts. Mirrors httpx.InitOSKMode(mode)
		// right above the OSK settings handler in this same file.
		httpx.InitSelfOrderMode(rawMode == "self_order")
		// ut-docs#1259: self_order is customer-facing and auth-exempt
		// (/self-order, /api/self-order/*) — the browser that just made this
		// switch must not keep a live session past it, or anyone with
		// physical/URL access to that same browser (no chrome-less kiosk
		// lockdown, or a desktop till's OS chrome) reaches an authenticated
		// page directly with zero PIN, same door #1253 closed for the
		// tap-through kiosk UI but not for this one. Revoke the acting
		// session server-side and clear the cookie, mirroring
		// POST /api/auth/logout. register/backoffice never revoke — the
		// operator making that switch needs to stay signed in.
		//
		// ut-docs#1301 (review finding NB-2, decided): this revokes ONLY the
		// acting session — deliberately, not an oversight, and kept as (a)
		// against #1301's own grooming recommendation for (b) (revoke every
		// session on the till), per the concrete-technical-reason escape
		// hatch that recommendation itself defined (see the issue comment).
		// Two verifiable reasons this switch creates no new exposure for a
		// session on a DIFFERENT device:
		//   1. auth.Middleware's /self-order, /api/self-order/* exemption is
		//      unconditional, not gated on display.mode — an anonymous LAN
		//      client could always reach that surface, self_order mode or
		//      not. The switch changes what a walk-up customer is routed to
		//      from "/", not what's reachable; it grants no attacker
		//      anything against a session elsewhere that a request to
		//      /self-order didn't already grant.
		//   2. A forgotten session anywhere is already time-bounded by the
		//      idle-lock default (common.DefaultIdleLockMinutes = 10),
		//      enforced server-side on every Resolve — independent of this
		//      till's display mode.
		// The threat #1259 closed is specific to the ACTING screen going
		// customer-facing while still one navigation away from an
		// authenticated /settings; neither of the above changes for a
		// session on a different device before vs. after this switch. See
		// TestSelfOrderMode_DoesNotRevokeOtherSessionsOnTheTill and
		// docs/code-reviews/2026-08-30-self-order-mode-revoke-session.md.
		if rawMode == "self_order" {
			if c, err := r.Cookie(auth.CookieName); err == nil && d.AuthSvc != nil {
				d.AuthSvc.Logout(r.Context(), c.Value)
				// ut-docs#1303 (ut-docs#1259 follow-up): the revoke itself is a
				// distinct auditable event, not just a side effect of
				// display_mode_changed — same reasoning as POST /api/auth/logout's
				// own dedicated "logout" entry. Written only when a session
				// actually existed to revoke (same gate as the Logout call above),
				// attributed to the acting session user via the same elev used
				// below, so dual-attribution (approver vs. blocked actor) matches.
				settingsAudit(r, posRepo, elev, "user", elev.ActorID, "self_order_session_revoked", nil)
			}
			setSessionCookie(w, "", -1)
		}
		settingsAudit(r, posRepo, elev, "settings", "display.mode", "display_mode_changed", map[string]any{"mode": rawMode})
		settingsRespondSaved(w, r, elev)
	})

	// Shop type (ut-docs#539, taxonomy per ADR-0026) — editable after setup.
	// Manager-only, same gate as the other store-level settings.
	mux.HandleFunc("POST /api/settings/shop-type", func(w http.ResponseWriter, r *http.Request) {
		// Validate BEFORE the elevation gate (ut-docs#557 convention) — a
		// bad shop type 400s without burning an approver's live PIN entry.
		_ = r.ParseForm()
		v := strings.TrimSpace(r.Form.Get("shop_type"))
		if v != "" && !isValidShopType(v) {
			http.Error(w, "unknown shop type", http.StatusBadRequest)
			return
		}
		// Mutating + audit-writing (ut-docs#796): in-place PIN re-auth
		// instead of the flat 403.
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			// Same label the picker itself shows; "—" mirrors the
			// template's own empty (clear) option.
			label := "—"
			if v != "" {
				label = httpx.T(locale, "setup.shop_type."+v)
			}
			renderElevationPrompt(w, r, "/api/settings/shop-type", "#shop-type-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.shop_type"), label),
				[]elevationHiddenField{{Name: "shop_type", Value: v}}, elev)
			return
		}
		if err := d.Settings.Set(r.Context(), common.KeyShopType, v); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		// ut-docs#1902: shop_type=service activates the builtin Salon layout
		// (ADR-0088); any other value (including clearing it back to "")
		// deactivates it if it was active. Best-effort — a failure here must
		// never block a shop-type save over a cosmetic menu personalization.
		// ut-docs#2006: reload unless it's a genuine no-op (no error,
		// nothing changed) — an error still reloads, since a failed
		// reinstall can leave the DB changed (removeSalon succeeded) even
		// though Sync itself returned an error.
		changed, syncErr := builtinlayouts.Sync(r.Context(), d.Db, v)
		if syncErr != nil {
			logging.L().Warnf("settings: could not sync builtin layout for shop_type %q: %v", v, syncErr)
		}
		if syncErr != nil || changed {
			if err := d.ReloadPlugins(r.Context()); err != nil {
				logging.L().Warnf("settings: could not reload plugins after shop_type layout sync: %v", err)
			}
		}
		settingsAudit(r, posRepo, elev, "settings", common.KeyShopType, "shop_type_changed",
			map[string]any{"shop_type": v})
		settingsRespondSaved(w, r, elev)
	})

	// Remove all opt-in sample data (ut-docs#539, extended to customers/
	// promo codes by ut-docs#567): the catalogue AND the 3 demo customers
	// AND the 3 demo promo codes together, so the button's copy matches
	// what it actually removes. Only untouched rows go in each category
	// (never sold/stock-adjusted for items; never sold-to or targeted by a
	// promotion for customers; never targeted at a customer for promo
	// codes — see remove_demo_customers_promos.sql's header for why that
	// last rule differs from the other two). The response reports combined
	// removed vs kept. Answers 200 with the outcome in the fragment — it's
	// an hx-swap target, and HTMX drops non-2xx bodies (see the enrol
	// handlers above).
	mux.HandleFunc("POST /api/settings/remove-demo-catalogue", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// ut-docs#633: a demo item/customer LIVE in the current (not yet
		// held) basket has no held_sales row for remove_demo*.sql's own
		// safety check to catch — block removal here instead of letting a
		// later tender FK-fail with no clear recovery. Checks both baskets
		// (ADR-0020 kiosk isolation: read-only here, so no guard conflict).
		// Read-only rejecting VALIDATION, not authorization — checked
		// BEFORE the elevation gate below (ut-docs#796 review finding #3),
		// same reasoning as payments-fee's range check: an approver
		// shouldn't burn a live PIN entry on a request the live basket was
		// always going to refuse.
		//
		// ut-docs#746: this check and the DELETE below aren't atomic — a
		// cashier could add a demo item to either basket in the gap between
		// this read and the removal running. Accepted un-fixed: single-till,
		// offline, low-value target (worst case is the same FK-fail this
		// guard exists to avoid in the first place, not data loss), and the
		// window is one HTTP request wide.
		if match := demoDataInLiveBasket(d.Engine, d.KioskEngine); match != noBasketMatch {
			key := "settings.data.demo_in_basket_cashier"
			if match == kioskBasketMatch {
				key = "settings.data.demo_in_basket_kiosk"
			}
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, httpx.T(locale, key))
			return
		}
		// Mutating, IRREVERSIBLE, and audit-writing (ut-docs#796): a denied
		// session gets an in-place PIN re-auth instead of the flat
		// forbidden span. ParseForm only reads override_pin — this handler
		// takes no other input.
		_ = r.ParseForm()
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/remove-demo-catalogue", "#demo-remove-msg",
				httpx.T(locale, "elevation.summary.remove_demo_data"), nil, elev)
			return
		}
		seedRepo := data.NewDemoSeedRepo(d.Db)
		removedItems, keptItems, err := seedRepo.RemoveDemoCatalogue(r.Context())
		if err != nil {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		removedCustPromo, keptCustomers, keptPromos, err := seedRepo.RemoveDemoCustomersPromos(r.Context())
		if err != nil {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		removed := removedItems + removedCustPromo
		// The single highest-value audit site in ut-docs#796 slice 1 — an
		// irreversible bulk deletion — so the payload records both the
		// per-category and the combined removed/kept counts the response
		// itself reports. ut-docs#1840: kept ITEMS are individually
		// named+reasoned (keptItems). ut-docs#1858: kept CUSTOMERS/PROMOS now
		// are too (keptCustomers/keptPromos), instead of a bare combined
		// count — the same audit-trail improvement, extended to the other
		// two kept lists.
		keptItemPayload := make([]map[string]any, len(keptItems))
		for i, it := range keptItems {
			keptItemPayload[i] = map[string]any{"id": it.ID, "reason": it.Reason}
		}
		keptCustomerPayload := make([]map[string]any, len(keptCustomers))
		for i, c := range keptCustomers {
			keptCustomerPayload[i] = map[string]any{"id": c.ID, "reason": c.Reason}
		}
		keptPromoPayload := make([]map[string]any, len(keptPromos))
		for i, p := range keptPromos {
			keptPromoPayload[i] = map[string]any{"code": p.Code, "reason": p.Reason}
		}
		settingsAudit(r, posRepo, elev, "demo_data", "-", "demo_data_removed", map[string]any{
			"removed":                  removed,
			"kept":                     len(keptItems) + len(keptCustomers) + len(keptPromos),
			"removed_items":            removedItems,
			"kept_items":               keptItemPayload,
			"removed_customers_promos": removedCustPromo,
			"kept_customers":           keptCustomerPayload,
			"kept_promos":              keptPromoPayload,
		})
		var b strings.Builder
		fmt.Fprintf(&b, `<span>✓ %s</span>`, html.EscapeString(fmt.Sprintf(httpx.T(locale, "settings.data.demo_removed"), removed)))
		writeKeptDemoItemsHTML(&b, locale, keptItems)
		writeKeptDemoCustomersHTML(&b, locale, keptCustomers)
		writeKeptDemoPromosHTML(&b, locale, keptPromos)
		w.Write([]byte(b.String()))
	})

	// ut-docs#1840 AC3: per-item resolution for a demo item kept only
	// because it was edited (data.KeptReasonEdited) — "remove anyway". Safe
	// by construction (that reason means every trading-history/held-basket
	// check already passed), but RemoveDemoItem re-checks server-side rather
	// than trusting the client's last-rendered reason, since the item could
	// have been sold or parked in a basket since. Same elevation/audit
	// pattern as remove-demo-catalogue above, scoped to one item — the
	// dedicated #demo-item-msg-<id> span is BOTH the button's own hx-target
	// AND the elevation prompt's hxTarget (unlike dismiss-pending-base-
	// plugin's #chip-row/#chip-row-msg split above), so a first-time
	// elevation hint can never wipe out the row's own retry target the way
	// ut-docs#865 finding F1 hit #restore-resume-block.
	//
	// ut-docs#1840 review findings F2/F3, both fixed here:
	//   - F3: the id is checked against IsSampleItem BEFORE checkOrElevate
	//     (same convention as dismiss-pending-base-plugin's own `matched`
	//     check above) — an unknown/already-gone id never burns an
	//     approver's PIN entry, and the hxTarget uses the attribute-selector
	//     form (demoItemMsgSelector) rather than a bare "#"+id, since id is
	//     never validated against CSS-identifier syntax.
	//   - F2: does NOT set X-UT-Response: ok on the elevated success path.
	//     That header (see dismiss-pending-base-plugin below) tells
	//     elevation_prompt.html's retry form to window.location.reload() —
	//     exactly wrong here, since a reload would wipe out the OTHER kept
	//     rows the merchant hasn't resolved yet. Leaving it unset matches
	//     remove-demo-catalogue's own bulk handler above, which never sets
	//     it either.
	mux.HandleFunc("POST /api/settings/demo-item/{id}/remove", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		id := r.PathValue("id")
		isSample, err := data.NewDemoSeedRepo(d.Db).IsSampleItem(r.Context(), id)
		if err != nil {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		if !isSample {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, httpx.T(locale, "settings.data.demo_item_not_found"))
			return
		}
		_ = r.ParseForm()
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/demo-item/"+id+"/remove",
				demoItemMsgSelector(id),
				fmt.Sprintf(httpx.T(locale, "elevation.summary.remove_demo_item"), id), nil, elev)
			return
		}
		if err := data.NewDemoSeedRepo(d.Db).RemoveDemoItem(r.Context(), id); err != nil {
			switch {
			case errors.Is(err, data.ErrDemoItemNotFound):
				fmt.Fprintf(w, `<span class="error">✗ %s</span>`, httpx.T(locale, "settings.data.demo_item_not_found"))
			case errors.Is(err, data.ErrDemoItemHasHistory):
				fmt.Fprintf(w, `<span class="error">✗ %s</span>`, httpx.T(locale, "settings.data.demo_item_has_history"))
			default:
				fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			}
			return
		}
		settingsAudit(r, posRepo, elev, "demo_data", id, "demo_item_removed", nil)
		fmt.Fprintf(w, `<span>✓ %s</span>%s`, httpx.T(locale, "settings.data.demo_item_removed"), disableDemoRowButtonsScript(".demo-kept-item"))
	})

	// ut-docs#1840 AC3's other resolution: "keep as my own item" — clears
	// is_sample_data so this item becomes a permanent catalog item, never
	// offered for removal again. No trading-history re-check needed (this
	// action doesn't delete anything), but still elevation/audit-gated like
	// every other mutating Settings→Data action on this page. Same F2/F3
	// fixes as the remove endpoint above.
	mux.HandleFunc("POST /api/settings/demo-item/{id}/keep", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		id := r.PathValue("id")
		isSample, err := data.NewDemoSeedRepo(d.Db).IsSampleItem(r.Context(), id)
		if err != nil {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		if !isSample {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, httpx.T(locale, "settings.data.demo_item_not_found"))
			return
		}
		_ = r.ParseForm()
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/demo-item/"+id+"/keep",
				demoItemMsgSelector(id),
				fmt.Sprintf(httpx.T(locale, "elevation.summary.keep_demo_item"), id), nil, elev)
			return
		}
		if err := data.NewDemoSeedRepo(d.Db).KeepDemoItemAsOwn(r.Context(), id); err != nil {
			if errors.Is(err, data.ErrDemoItemNotFound) {
				fmt.Fprintf(w, `<span class="error">✗ %s</span>`, httpx.T(locale, "settings.data.demo_item_not_found"))
				return
			}
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		settingsAudit(r, posRepo, elev, "demo_data", id, "demo_item_kept_as_own", nil)
		fmt.Fprintf(w, `<span>✓ %s</span>%s`, httpx.T(locale, "settings.data.demo_item_kept"), disableDemoRowButtonsScript(".demo-kept-item"))
	})

	// ut-docs#1858: per-promo resolution mirroring the per-item one above
	// exactly — "remove anyway" for a demo promo kept only because it was
	// edited (data.KeptReasonEdited). Safe by construction (that reason
	// means the targeting check already passed), but RemoveDemoPromo
	// re-checks server-side rather than trusting the client's last-rendered
	// reason, since the promo could have been targeted at a customer since
	// the page last rendered. Same elevation/audit pattern, same F2/F3-style
	// fixes (pre-elevation existence check, attribute-selector hxTarget, no
	// X-UT-Response: ok on the elevated path — see the item handler's own
	// comment for why).
	mux.HandleFunc("POST /api/settings/demo-promo/{code}/remove", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		code := r.PathValue("code")
		isSample, err := data.NewDemoSeedRepo(d.Db).IsSamplePromo(r.Context(), code)
		if err != nil {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		if !isSample {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, httpx.T(locale, "settings.data.demo_promo_not_found"))
			return
		}
		_ = r.ParseForm()
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/demo-promo/"+code+"/remove",
				demoPromoMsgSelector(code),
				fmt.Sprintf(httpx.T(locale, "elevation.summary.remove_demo_promo"), code), nil, elev)
			return
		}
		if err := data.NewDemoSeedRepo(d.Db).RemoveDemoPromo(r.Context(), code); err != nil {
			switch {
			case errors.Is(err, data.ErrDemoPromoNotFound):
				fmt.Fprintf(w, `<span class="error">✗ %s</span>`, httpx.T(locale, "settings.data.demo_promo_not_found"))
			case errors.Is(err, data.ErrDemoPromoTargeted):
				fmt.Fprintf(w, `<span class="error">✗ %s</span>`, httpx.T(locale, "settings.data.demo_promo_targeted"))
			default:
				fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			}
			return
		}
		settingsAudit(r, posRepo, elev, "demo_data", code, "demo_promo_removed", nil)
		fmt.Fprintf(w, `<span>✓ %s</span>%s`, httpx.T(locale, "settings.data.demo_promo_removed"), disableDemoRowButtonsScript(".demo-kept-promo"))
	})

	// ut-docs#1858's other resolution: "keep as my own" — clears
	// is_sample_data so this promo becomes a permanent code, never offered
	// for removal again. No targeting re-check needed (this action doesn't
	// delete anything), but still elevation/audit-gated like every other
	// mutating Settings→Data action on this page. Mirrors KeepDemoItemAsOwn's
	// handler exactly.
	mux.HandleFunc("POST /api/settings/demo-promo/{code}/keep", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		code := r.PathValue("code")
		isSample, err := data.NewDemoSeedRepo(d.Db).IsSamplePromo(r.Context(), code)
		if err != nil {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		if !isSample {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, httpx.T(locale, "settings.data.demo_promo_not_found"))
			return
		}
		_ = r.ParseForm()
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/demo-promo/"+code+"/keep",
				demoPromoMsgSelector(code),
				fmt.Sprintf(httpx.T(locale, "elevation.summary.keep_demo_promo"), code), nil, elev)
			return
		}
		if err := data.NewDemoSeedRepo(d.Db).KeepDemoPromoAsOwn(r.Context(), code); err != nil {
			if errors.Is(err, data.ErrDemoPromoNotFound) {
				fmt.Fprintf(w, `<span class="error">✗ %s</span>`, httpx.T(locale, "settings.data.demo_promo_not_found"))
				return
			}
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		settingsAudit(r, posRepo, elev, "demo_data", code, "demo_promo_kept_as_own", nil)
		fmt.Fprintf(w, `<span>✓ %s</span>%s`, httpx.T(locale, "settings.data.demo_promo_kept"), disableDemoRowButtonsScript(".demo-kept-promo"))
	})

	// Dismiss the "restore from another POS?" resume prompt (ut-docs#617)
	// without importing anything — an explicit "no thanks," not just
	// ignoring it forever. hx-swap="outerHTML" on the whole block, so an
	// empty 200 body removes it; 204 wouldn't swap (see remove-demo-
	// catalogue's comment above on why 2xx-with-body is used for hx-swap
	// targets).
	// ut-docs#865: checkOrElevate/InsertAuditElevated (#557/#796). hxTarget
	// is the dedicated #restore-resume-msg span, NOT #restore-resume-block
	// itself — review finding F1: the block is what the button's own
	// hx-swap="outerHTML" removes, and the dialog's retry form always
	// innerHTML-swaps into hxTarget (elevation_prompt.html's fixed
	// hx-swap); pointing it at a node the denial hint had just replaced
	// left the retry's own hx-target resolving to nothing — htmx bails
	// with htmx:targetError instead of ever sending the approver's PIN.
	// X-UT-Response: ok, set inline below (not via settingsRespondSaved,
	// which this handler doesn't call — it keeps the pre-existing bare
	// empty-200-body success shape), makes the dialog's own script reload
	// the page on the elevated path instead of leaving a stale msg span —
	// same fix #796's review made for till-name/till-register/save (a
	// missing X-UT-Response: ok left stale values after approval).
	mux.HandleFunc("POST /api/settings/dismiss-restore-prompt", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/dismiss-restore-prompt", "#restore-resume-msg",
				httpx.T(locale, "elevation.summary.dismiss_restore_prompt"), nil, elev)
			return
		}
		if err := d.Settings.Set(r.Context(), common.KeyRestorePromptStatus, ""); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		settingsAudit(r, posRepo, elev, "settings", common.KeyRestorePromptStatus, "restore_prompt_dismissed", nil)
		if elev.Outcome == elevated {
			w.Header().Set("X-UT-Response", "ok")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	})

	// Dismiss one still-pending country base-plugin auto-install
	// (ut-docs#591) without installing it — "a merchant can decline/remove
	// anything auto-installed" for the not-yet-installed case. hx-swap
	// "outerHTML" on the chip itself, same reasoning as the restore-prompt
	// dismiss above: an empty 200 body removes just that chip.
	// ut-docs#865: checkOrElevate/InsertAuditElevated (#557/#796). hxTarget
	// is this chip's own dedicated #pending-plugin-msg-<canonical_type>
	// span (review finding F1 — same "don't point the retry at a node the
	// button's own outerHTML removal can make vanish" reasoning as
	// dismiss-restore-prompt above), NOT the chip-row itself, via a CSS
	// attribute selector rather than "#id" since canonical_type could
	// contain characters a bare #id selector would need escaping for (the
	// shipped catalogue only ever produces "language" today —
	// setup_base_plugins.go). Same X-UT-Response: ok reasoning as
	// dismiss-restore-prompt above.
	// ut-docs#868: canonical_type/locale are now validated against the
	// currently-pending list BEFORE checkOrElevate (this file's own
	// established convention — till-register/payments-fee): previously
	// neither was checked at all, so a mismatched pair broke the retry's
	// `[id="pending-plugin-msg-%s"]` selector (a value containing `"` or
	// `]`) and polluted the audit trail's entity_id with an arbitrary
	// string. Rejecting here also means a request that was always going
	// to be a no-op doesn't burn an approver's live PIN entry.
	mux.HandleFunc("POST /api/settings/dismiss-pending-base-plugin", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		canonicalType := strings.TrimSpace(r.Form.Get("canonical_type"))
		localeVal := strings.TrimSpace(r.Form.Get("locale"))
		pending, err := loadPendingBasePlugins(r.Context(), d)
		if err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		matched := false
		for _, spec := range pending {
			if spec.CanonicalType == canonicalType && spec.Locale == localeVal {
				matched = true
				break
			}
		}
		if !matched {
			http.Error(w, "unknown pending base plugin", http.StatusBadRequest)
			return
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/dismiss-pending-base-plugin",
				fmt.Sprintf(`[id="pending-plugin-msg-%s"]`, canonicalType),
				fmt.Sprintf(httpx.T(locale, "elevation.summary.dismiss_pending_base_plugin"), canonicalType),
				[]elevationHiddenField{{Name: "canonical_type", Value: canonicalType}, {Name: "locale", Value: localeVal}}, elev)
			return
		}
		if err := dismissPendingBasePlugin(r.Context(), d, canonicalType, localeVal); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		settingsAudit(r, posRepo, elev, "settings", canonicalType, "pending_base_plugin_dismissed", map[string]any{"canonical_type": canonicalType, "locale": localeVal})
		if elev.Outcome == elevated {
			w.Header().Set("X-UT-Response", "ok")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	})

	// Dismiss the TSE provisioning status chip (ADR-0053, ut-docs#802)
	// without provisioning — e.g. a shop that decided against the managed
	// service after a subscription_inactive rejection. Clears the whole
	// lifecycle state (a later re-run of provisioning re-creates it); same
	// hx-swap "outerHTML"/empty-200-body convention as the two dismisses
	// above. Deliberately does NOT touch fiscal.signing_device_configured or any stored
	// credential — this dismisses a status chip, not a configured TSE.
	//
	// ut-docs#1174: no longer unconditional — a hard-gated market's
	// UNRESOLVED failure (tseProvisioningDismissBlocked) answers 409 and
	// keeps the state: with sales hard-blocked until a TSE works, dismissing
	// the only explanation would hide the exact thing the operator must fix.
	// The template already omits the dismiss button in that case; this is
	// the defense-in-depth server half.
	mux.HandleFunc("POST /api/settings/dismiss-tse-provisioning", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "settings") {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		st, err := loadTSEProvisioningState(r.Context(), d)
		if err != nil {
			http.Error(w, "could not load state", http.StatusInternalServerError)
			return
		}
		if tseProvisioningDismissBlocked(st) {
			locale := httpx.ResolveLocale(w, r)
			http.Error(w, httpx.T(locale, "settings.tse.dismiss_blocked"), http.StatusConflict)
			return
		}
		if err := saveTSEProvisioningState(r.Context(), d, nil); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		if st != nil { // a no-op dismiss of nothing isn't a state change
			now := time.Now().UTC().Format(time.RFC3339)
			if err := posRepo.InsertAudit(r.Context(), nil, settingsActorID(r), "fiscal", "tse", "tse_provisioning_dismissed",
				map[string]any{"status": st.Status, "error_code": st.ErrorCode}, now, ""); err != nil {
				logging.L().Errorf("settings: audit TSE provisioning dismiss: %v", err)
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	})

	// Manual TSE provisioning retry (ut-docs#1174 item A): a manager
	// re-attempts a definitively rejected kickoff or a failed credential
	// fetch — e.g. right after activating the subscription the cloud said
	// was missing — and gets an immediate answer: the block re-renders with
	// the post-attempt state (htmx swaps it in place, same outerHTML target
	// as dismiss). Same manager gate as dismiss.
	mux.HandleFunc("POST /api/settings/retry-tse-provisioning", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "settings") {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		st, err := retryTSEProvisioning(r.Context(), d, settingsActorID(r))
		if errors.Is(err, errNoTSERetry) {
			locale := httpx.ResolveLocale(w, r)
			http.Error(w, httpx.T(locale, "settings.tse.nothing_to_retry"), http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		renderTSEProvisioningBlock(w, r, st)
	})

	// This till's own display name (ut-docs#396) — distinct from a replica's
	// own sync.till_name — shown in Settings and on the /tills page.
	mux.HandleFunc("POST /api/settings/till-name", func(w http.ResponseWriter, r *http.Request) {
		// No rejecting validation here (the name is only trimmed/
		// truncated), so the gate stays first, exactly as before —
		// ParseForm only moved up to make override_pin readable
		// (ut-docs#796).
		_ = r.ParseForm()
		name := strings.TrimSpace(r.Form.Get("name"))
		if rs := []rune(name); len(rs) > 60 { // mirrors the field's own maxlength="60" server-side
			name = string(rs[:60])
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			renderElevationPrompt(w, r, "/api/settings/till-name", "#till-name-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.till_name"), name),
				[]elevationHiddenField{{Name: "name", Value: name}}, elev)
			return
		}
		if name != "" {
			if err := d.Settings.Set(r.Context(), "till.name", name); err != nil {
				http.Error(w, "could not save", http.StatusInternalServerError)
				return
			}
			settingsAudit(r, posRepo, elev, "settings", "till.name", "till_name_changed",
				map[string]any{"name": name})
		}
		settingsRespondSaved(w, r, elev)
	})

	// This till's own register identity (ut-docs#268) — the register a
	// shift-scoped WRITE (e.g. a Pfandrückgabe payout) resolves against,
	// persisted under till.register_id via pos.ResolveTillRegisterID's
	// settings key. An id that isn't an active register is rejected rather
	// than persisted: garbage here would silently misroute payouts later.
	mux.HandleFunc("POST /api/settings/till-register", func(w http.ResponseWriter, r *http.Request) {
		// Required-field + real-register validation BEFORE the elevation
		// gate (ut-docs#557 convention): a register id that would be
		// rejected anyway must not burn an approver's live PIN entry.
		_ = r.ParseForm()
		id := strings.TrimSpace(r.Form.Get("register_id"))
		if id == "" {
			http.Error(w, "register_id required", http.StatusBadRequest)
			return
		}
		regs, err := posRepo.ListRegisters(r.Context())
		if err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		regName := ""
		valid := false
		for _, reg := range regs {
			if reg.ID == id {
				valid = true
				regName = reg.Name
				break
			}
		}
		if !valid {
			http.Error(w, "unknown register", http.StatusBadRequest)
			return
		}
		// Mutating + audit-writing (ut-docs#796): a wrong register here
		// silently misroutes payouts later, so the approver sees the
		// register's display name in the summary, not just its id.
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			renderElevationPrompt(w, r, "/api/settings/till-register", "#till-register-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.till_register"), regName),
				[]elevationHiddenField{{Name: "register_id", Value: id}}, elev)
			return
		}
		if err := d.Settings.Set(r.Context(), pos.SettingsKeyTillRegisterID, id); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		settingsAudit(r, posRepo, elev, "settings", pos.SettingsKeyTillRegisterID, "till_register_changed",
			map[string]any{"register_id": id, "register_name": regName})
		settingsRespondSaved(w, r, elev)
	})

	mux.HandleFunc("/api/settings/theme", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if v := strings.TrimSpace(r.Form.Get("theme")); v != "" {
			st := d.CurrentState()
			st.Theme = v
			if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
				http.Error(w, "could not save", http.StatusInternalServerError)
				return
			}
			d.SetState(st)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/settings/save", func(w http.ResponseWriter, r *http.Request) {
		// No rejecting validation on this handler (empty fields are simply
		// skipped, a bad taxRatePct is ignored), so the gate stays first —
		// ParseForm only moved up to make override_pin readable
		// (ut-docs#796).
		_ = r.ParseForm()
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			renderElevationPrompt(w, r, "/api/settings/save", "#settings-save-msg",
				httpx.T(locale, "elevation.summary.store_save"),
				[]elevationHiddenField{
					{Name: "currency", Value: r.Form.Get("currency")},
					{Name: "country", Value: r.Form.Get("country")},
					{Name: "region", Value: r.Form.Get("region")},
					{Name: "taxRatePct", Value: r.Form.Get("taxRatePct")},
					{Name: "locale", Value: r.Form.Get("locale")},
				}, elev)
			return
		}
		auditPayload := map[string]any{}
		st := d.CurrentState()
		if v := strings.TrimSpace(r.Form.Get("currency")); v != "" {
			st.Currency = v
			auditPayload["currency"] = v
			// ut-docs#970 review (F2): this is the handler the shipped
			// Settings currency card actually posts to (settings.html's
			// `hx-post="/api/settings/save"`) — the earlier attempt to mark
			// this in POST /api/settings/upsert's generic key/value switch
			// was the WRONG handler (that one backs the advanced raw
			// key/value table, not the currency picker), so an operator
			// using the shipped UI was still gated on their next import.
			// Left the upsert-handler marking in place too (harmless, and
			// correct for a caller that does use the raw table), but this
			// is the one that actually matters for the real UI.
			if err := d.Settings.Set(r.Context(), common.KeyCurrencyConfirmed, "true"); err != nil {
				logging.L().Errorf("settings: mark currency confirmed: %v", err)
			}
		}
		if v := strings.TrimSpace(r.Form.Get("country")); v != "" {
			// ut-docs#1750: the second writer of store.country. A reviewer
			// reproduced a manager-only bypass through THIS handler after
			// the first fix guarded only /api/settings/upsert.
			if !requireFiscalAuthorityForCountryChange(w, r, d, v) {
				return
			}
			if err := clearFiscalStateForCountryChange(r.Context(), d, actorIDFor(r), v); err != nil {
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "settings.error.save_failed", "settings", err) // page-error:allow /api/ route
				return
			}
			st.Country = v
			auditPayload["country"] = v
			// ut-docs#1027: re-derive locale from the new country when the
			// operator didn't explicitly post one in the same request —
			// "changing country afterwards re-derives the locale... never
			// silently leaves a mismatched pair." See localeSafeToPreset's
			// own doc comment for why this is gated at all (RTL/digit
			// rendering, not just translated text) and why a non-RTL
			// locale doesn't need its pack installed first. If the
			// country's default isn't safe yet, locale is left exactly as
			// it was — a partial, not full, answer to "never mismatched,"
			// but the safe one.
			//
			// ut-docs#1074: also never re-derive once the operator has
			// EXPLICITLY chosen a locale via Settings' Language card
			// (common.KeyLocaleConfirmed) — a country change is not that
			// choice, and silently overriding it here would be the exact
			// clobber this same card exists to prevent, just from a
			// different call site.
			if strings.TrimSpace(r.Form.Get("locale")) == "" {
				localeConfirmed, _, lcErr := d.Settings.Get(r.Context(), common.KeyLocaleConfirmed)
				if lcErr != nil {
					logging.L().Warnf("settings: read %s: %v", common.KeyLocaleConfirmed, lcErr)
				}
				if localeConfirmed != "true" {
					if cs, ok, csErr := data.NewCountrySettingsRepo(d.Db).Get(r.Context(), v); csErr == nil && ok &&
						cs.DefaultLocale != st.Locale && localeSafeToPreset(cs.DefaultLocale) {
						st.Locale = cs.DefaultLocale
						auditPayload["locale"] = cs.DefaultLocale
					}
				}
			}
		}
		if v := strings.TrimSpace(r.Form.Get("region")); v != "" {
			st.Region = v
			auditPayload["region"] = v
		}
		// Whether this request carried an ACCEPTED explicit locale — the
		// Language card's own field. Drives the ut-docs#2135 cookie clear
		// below; a rejected value must not clear anything.
		localeChosen := false
		if v := strings.TrimSpace(r.Form.Get("locale")); v != "" {
			// Reject silently rather than 400 (ut-docs#861) — matches this
			// handler's existing lenient contract (see the comment at its
			// top: "empty fields are simply skipped, a bad taxRatePct is
			// ignored"). An unrecognized locale isn't just cosmetically
			// wrong the way an unknown currency code is (that falls back to
			// a plain "CODE 1.23" format): it would make T() fall back to
			// raw keys sitewide for anything with no request to resolve a
			// per-browser preference from — notification email in
			// particular, the exact case this card exists to fix.
			if slices.Contains(httpx.AvailableLocales(), v) {
				st.Locale = v
				auditPayload["locale"] = v
				localeChosen = true
				// ut-docs#1074: this form field is the one genuine
				// operator-explicit locale choice (Settings' Language
				// card) — mark it confirmed so no later derivation
				// (ut-docs#1027's country-change re-derive, or this
				// card's own base-plugin-install catch-up) ever
				// silently overrides it.
				if err := d.Settings.Set(r.Context(), common.KeyLocaleConfirmed, "true"); err != nil {
					logging.L().Errorf("settings: mark locale confirmed: %v", err)
				}
			}
		}
		// TaxInclusive/AllowNegativeInventory are deliberately NOT set here:
		// the only caller (the currency card) never posts them, and an
		// unconditional write silently zeroed both on every currency change
		// (ut-docs#178). They're settable via /api/settings/upsert instead
		// (store.tax_inclusive / pos.allow_negative_inventory).
		// taxRatePct keeps its guard below though no shipped UI posts it
		// here either — not dead code, exercised by TestDisplayAndStoreSettings.
		if v := r.Form.Get("taxRatePct"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				st.TaxRatePct = n
				auditPayload["tax_rate_pct"] = n
			}
		}
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		d.SetState(st)
		httpx.InitCurrency(st.Currency)
		// Live-apply, no restart (ut-docs#861) — st.Locale is always the
		// current-or-just-changed value (CurrentState() seeds it when the
		// "locale" field wasn't posted this call), same unconditional-call
		// shape as InitCurrency above. SetDefaultLocale is itself a no-op on
		// an empty value, so a till whose Locale has genuinely never been
		// set (fresh boot, before LoadState's cfg.Locales.Locale fallback
		// even applies) can't accidentally blank the wired translator.
		httpx.SetDefaultLocale(st.Locale)
		if localeChosen {
			// ut-docs#2135: setting the shop default is not enough — a
			// ut_lang cookie from an earlier ?lang= link (clicking through
			// /setup in English is the common way to acquire one) overrides
			// it on every page, for a year. Retire every such override
			// shop-wide. This is what makes re-applying the language the
			// shop is ALREADY set to do something, which is precisely what
			// an operator does when the screen shows the wrong language and
			// Settings already says the right one — and what lets that fix
			// reach a till the manager is not standing at.
			retireLocaleOverrides(r.Context(), d.Settings)
		}
		// In place: replacing the engine would empty a basket in progress.
		// Both engines: the kiosk's separate instance (ut-docs#449) must see
		// the same tax config or it would silently charge stale rates.
		newCfg := pos.Config{
			TaxInclusive:                 st.TaxInclusive,
			TaxRateBasisPoints:           st.TaxRatePct * 100,
			ServiceChargeRateBasisPoints: common.EffectiveServiceChargeRateBP(st),
		}
		d.Engine.SetConfig(newCfg)
		if d.KioskEngine != nil {
			d.KioskEngine.SetConfig(newCfg)
		}
		settingsAudit(r, posRepo, elev, "settings", "-", "store_settings_saved", auditPayload)
		settingsRespondSaved(w, r, elev)
	})

	// generic key/value upsert
	mux.HandleFunc("POST /api/settings/upsert", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		key := strings.TrimSpace(r.Form.Get("key"))
		value := strings.TrimSpace(r.Form.Get("value"))
		// Key-independent validation BEFORE the elevation gate (ut-docs#557
		// convention — a request that was always going to 400 must not burn
		// an approver's live PIN entry). The fiscal.* key gates below are
		// deliberately NOT moved: they are ADR-0048's own separate
		// authorization layer, out of scope for ut-docs#796.
		if key == "" {
			http.Error(w, "key required", http.StatusBadRequest)
			return
		}
		// ut-docs#244: validate before persisting, not just before reflecting
		// into RuntimeState — the old code let an unparsable value through to
		// d.Settings.Set unchanged (silently no-op'ing only the in-memory
		// reflection below), so the DB and the live state disagreed with no
		// operator feedback at all.
		if key == common.KeyServiceChargeRate {
			bp, ok := common.ParseServiceChargeRateBasisPoints(value)
			if !ok {
				// Localized since ut-docs#962: this body is now actually
				// RENDERED to the operator (settings.html's All-settings
				// card surfaces a refused upsert), so an English-only
				// literal would show through in every locale.
				http.Error(w, httpx.T(httpx.ResolveLocale(w, r), "settings.service_charge.invalid_rate"), http.StatusBadRequest)
				return
			}
			// ut-docs#962: a service-charge/cover line on a bill has been
			// illegal in Turkey since the 2026-01-30 Fiyat Etiketi
			// Yönetmeliği amendment (₺3,973 per receipt under Law 6502
			// art. 77) — the setting is not the merchant's to enable, so
			// this is refused here rather than silently accepted and
			// zeroed out later at the tender path (common.
			// EffectiveServiceChargeRateBP is the fail-closed backstop for
			// whatever reaches there regardless, e.g. a rate saved before
			// the shop's country was set to TR).
			if bp > 0 && common.ServiceChargeForbidden(d.CurrentState().Country) {
				http.Error(w, httpx.T(httpx.ResolveLocale(w, r), "settings.service_charge.tr_forbidden"), http.StatusBadRequest)
				return
			}
		}
		// ADR-0083 (ut-docs#1767): the two signing-device posture keys are
		// stored per country, but the form field / cloud directive still
		// sends their flat logical names. Resolve once, up front: every
		// fiscal gate below switches on logicalKey, and every read/write
		// goes to storageKey. For any other key both are simply key.
		logicalKey, storageKey := resolveFiscalPostureKey(d, key)
		// ADR-0048 rejecting VALIDATION (not authorization) — applies
		// regardless of who's asking, so hoisted above the elevation gate
		// below (ut-docs#796 review finding #2): without this, a manager
		// posting one of these two always-400 cases got walked through a
		// live PIN entry for a request that was always going to fail,
		// exactly the cost the key/service-charge checks above already
		// avoid. The actual AUTHORIZATION checks (canPerform on
		// "fiscal_tse_override") stay below, after the elevation gate,
		// unchanged — they depend on the SESSION user, not on whether
		// "settings" got elevated (see the comment there).
		switch logicalKey {
		case fiscal.KeyOverrideUntil, fiscal.KeyOverrideReason, fiscal.KeyOverrideActor:
			// Fabricating a non-empty override here is refused for
			// everyone — real validation, not a role check. Clearing
			// (empty value) is allowed past this point; its actual
			// authorization (owner-only) is the canPerform check below.
			if value != "" {
				http.Error(w, "fiscal override state is managed via POST /api/fiscal/signing-override", http.StatusBadRequest)
				return
			}
		case wireKeySigningDeviceFailingSince:
			// ADR-0048 Decision 1: "Not operator-settable in this card" —
			// no UI control ships for this key at all, set or clear, by
			// design (a fake "mark as failing"/"mark as fixed" toggle would
			// let an operator manufacture or erase the very state the
			// override exists to gate). Written only by tests directly, or
			// by a future real fiscal.sign.ask failure callback (#675) —
			// never through this generic editor, for anyone. Always 400,
			// for anyone — no role can ever make this key settable here.
			http.Error(w, "fiscal.signing_device_failing_since is not settable via this endpoint", http.StatusBadRequest)
			return
		}
		// Mutating + audit-writing (ut-docs#796): this replaces ONLY the
		// old flat `if !canPerform(d, r, "settings") { 403 }` outer gate —
		// the interior fiscal_tse_override gates below still check the
		// SESSION user, exactly as before (ADR-0048); an elevated
		// "settings" approval never grants "fiscal_tse_override".
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			renderElevationPrompt(w, r, "/api/settings/upsert", "#settings-upsert-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.setting_upsert"), key, value),
				[]elevationHiddenField{
					{Name: "key", Value: key},
					{Name: "value", Value: value},
				}, elev)
			return
		}
		// ADR-0048: this generic editor must not be a side door around the
		// fiscal gate. The override window/metadata is written only by
		// POST /api/fiscal/signing-override (typed acknowledgement, duration
		// cap, audit) — fabricating one here is refused for everyone
		// (validated above); clearing (empty value = revoking an override
		// early) stays possible for an owner, checked here. The fiscal.*
		// flags themselves are owner(admin)-only, not manager-level like
		// the rest of this page — this authorization check reads the
		// SESSION user (canPerform, not the elevation's approver), so an
		// elevated "settings" approval alone can never satisfy it.
		switch logicalKey {
		case fiscal.KeyOverrideUntil, fiscal.KeyOverrideReason, fiscal.KeyOverrideActor:
			if !canPerform(d, r, "fiscal_tse_override") {
				httpx.RenderError(w, r, http.StatusForbidden, "fiscaldevice.error.owner_required", nil)
				return
			}
		case fiscal.KeySystemOfRecord, wireKeySigningDeviceConfigured:
			if !canPerform(d, r, "fiscal_tse_override") {
				httpx.RenderError(w, r, http.StatusForbidden, "fiscaldevice.error.owner_required", nil)
				return
			}
		}
		// ut-docs#1750: a country edit goes through the shared invariant
		// every writer of store.country uses (fiscal_country_change.go) —
		// before the value is persisted, so a failure can never leave the
		// country moved with the old country's posture still set. Since
		// ADR-0083 the posture rows are per country, so this is
		// shop-configuration hygiene plus an owner check on changing a
		// live shop's tax jurisdiction, no longer a shared-key guard.
		if key == common.KeyCountry {
			if !requireFiscalAuthorityForCountryChange(w, r, d, value) {
				return
			}
			if err := clearFiscalStateForCountryChange(r.Context(), d, actorIDFor(r), value); err != nil {
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "settings.error.save_failed", "settings", err) // page-error:allow /api/ route
				return
			}
		}
		// Read the prior value first so the fiscal-toggle audit below can
		// record the actual transition, not just the new value.
		fiscalToggleAction := ""
		switch logicalKey {
		case fiscal.KeySystemOfRecord:
			fiscalToggleAction = "system_of_record_changed"
		case wireKeySigningDeviceConfigured:
			fiscalToggleAction = "tse_configured_changed"
		}
		prevFiscalValue := ""
		if fiscalToggleAction != "" {
			prevFiscalValue, _, _ = d.Settings.Get(r.Context(), storageKey)
		}
		if err := d.Settings.Set(r.Context(), storageKey, value); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "settings.error.save_failed", "settings_fiscal", err)
			return
		}
		// Every write to the fiscal posture toggles is itself audit-logged
		// (ADR-0048 Decision 1): the shop's own trail honestly records when
		// it declared itself in- or out-of-scope. The setting write itself
		// already succeeded by this point, so a failed audit insert doesn't
		// fail the request (the toggle did take effect) — but it must not
		// be silently swallowed either, since a lost audit entry here is
		// exactly the gap ADR-0048 added this logging to close.
		if fiscalToggleAction != "" && prevFiscalValue != value {
			// Recorded against the row actually written (the per-country
			// storage key, ADR-0083), so the trail names the market whose
			// posture changed rather than the flat logical name.
			if auditErr := data.NewPOSRepo(d.Db).InsertAudit(r.Context(), nil, getSessionUserID(r),
				"fiscal_settings", storageKey, fiscalToggleAction,
				map[string]any{"actor": getSessionUserID(r), "from": prevFiscalValue, "to": value},
				time.Now().UTC().Format(time.RFC3339), ""); auditErr != nil {
				logging.L().Errorf("fiscal settings: audit log for %s (%s -> %s) failed: %v", storageKey, prevFiscalValue, value, auditErr)
			}
		}
		// General upsert audit (ut-docs#796), mutually exclusive BY KEY with
		// the ADR-0048 fiscal-toggle audit above: for the two fiscal posture
		// toggles that block is the authoritative record (it captures the
		// actual from→to transition, and skips no-op re-saves on purpose) —
		// double-logging the same single write as a generic
		// "setting_upserted" row too would make the trail ambiguous (two
		// rows, one write). Every other key gets the generic entry here,
		// including an early override-clear (fiscal.override_* set to ""),
		// which previously left no trace at all.
		if fiscalToggleAction == "" {
			settingsAudit(r, posRepo, elev, "settings", key, "setting_upserted",
				map[string]any{"key": key, "value": value})
		}
		// reflect into state for known keys
		truthy := func(v string) bool { return strings.ToLower(v) == "true" || v == "1" || v == "on" }
		// ut-docs#1027: this raw key/value table is, today, the ONLY shipped
		// UI path that lets an operator change store.country after setup
		// (neither Settings form posts "country" — see the currency-card
		// handler above and the Language card below) — so it's where the
		// card's "changing country afterwards re-derives the locale"
		// acceptance criterion actually has to live. Looked up before
		// UpdateState's closure, not inside it, so the DB read doesn't run
		// under the state write lock.
		var derivedLocale string
		if key == common.KeyCountry {
			// ut-docs#1074: never re-derive once the operator has explicitly
			// chosen a locale via Settings' Language card — same guard as
			// the /api/settings/save country handler just above.
			localeConfirmed, _, lcErr := d.Settings.Get(r.Context(), common.KeyLocaleConfirmed)
			if lcErr != nil {
				logging.L().Warnf("settings: read %s: %v", common.KeyLocaleConfirmed, lcErr)
			}
			if localeConfirmed != "true" {
				if cs, ok, csErr := data.NewCountrySettingsRepo(d.Db).Get(r.Context(), value); csErr == nil && ok && localeSafeToPreset(cs.DefaultLocale) {
					derivedLocale = cs.DefaultLocale
				}
			}
		}
		st := d.UpdateState(func(s *common.RuntimeState) {
			switch key {
			case common.KeyTheme:
				s.Theme = value
			case common.KeyCurrency:
				s.Currency = value
			case common.KeyCountry:
				s.Country = value
				if derivedLocale != "" && derivedLocale != s.Locale {
					s.Locale = derivedLocale
				}
			case common.KeyRegion:
				s.Region = value
			case common.KeyLocale:
				// ut-docs#861 review finding F2: without this case,
				// SaveState's now-unconditional KeyLocale write (added by
				// this same card) meant an operator editing store.locale
				// via this raw table saw it silently reverted on the very
				// next /api/settings/save from any OTHER card (that
				// handler always writes back CurrentState().Locale, which
				// this switch never updated) — the exact ut-docs#178 class
				// of bug its own comment two cases up warns about. Same
				// validation as the shipped Language card, not the bare
				// unvalidated pass-through Currency/Country get above: an
				// invalid locale here breaks T() rendering sitewide
				// immediately (worse blast radius than an unknown currency
				// code, which just falls back to a plain "CODE 1.23"
				// format), so this is closer in kind to the
				// KeyServiceChargeRate validation below than to Currency.
				if slices.Contains(httpx.AvailableLocales(), value) {
					s.Locale = value
				}
			case common.KeyTaxInclusive:
				s.TaxInclusive = truthy(value)
			case common.KeyTaxRate:
				if n, err := strconv.Atoi(value); err == nil {
					s.TaxRatePct = n
				}
			case common.KeyServiceChargeRate:
				// Already validated above; guard kept for defensive safety.
				if bp, ok := common.ParseServiceChargeRateBasisPoints(value); ok {
					s.ServiceChargeRateBasisPoints = bp
				}
			case common.KeyAllowNegativeInventory:
				s.AllowNegativeInventory = truthy(value)
			}
		})
		switch key {
		case common.KeyCurrency:
			httpx.InitCurrency(st.Currency)
			// An explicit Settings write is exactly the "operator chose
			// this" signal ut-docs#970's import gate needs — mark it
			// confirmed so a catalogue import never re-asks after this.
			if err := d.Settings.Set(r.Context(), common.KeyCurrencyConfirmed, "true"); err != nil {
				logging.L().Errorf("settings: mark currency confirmed: %v", err)
			}
		case common.KeyLocale:
			// Live-apply here too (ut-docs#861 review F2) — same
			// unconditional-on-current-value shape as InitCurrency above;
			// SetDefaultLocale's own empty-guard makes this safe even when
			// the switch above left s.Locale untouched (invalid value).
			httpx.SetDefaultLocale(st.Locale)
			// Same reasoning as the Language card (ut-docs#2135): editing
			// store.locale by hand here is just as explicit a shop choice,
			// so it must not be silently outvoted by a stale per-browser
			// cookie either. Guarded on the value actually having been
			// ACCEPTED above — a rejected locale changes nothing, so it
			// must not throw away anyone's override.
			if slices.Contains(httpx.AvailableLocales(), value) {
				retireLocaleOverrides(r.Context(), d.Settings)
			}
		case common.KeyTaxInclusive, common.KeyServiceChargeRate, common.KeyCountry:
			// In place: replacing the engine would empty a basket in progress.
			// Both engines — see the currency-card handler above (ut-docs#449).
			//
			// KeyCountry is in this list because the effective service-charge
			// rate is country-derived (ut-docs#962): a shop that switches to
			// TR with a rate still configured would otherwise keep quoting an
			// illegal service-charge line on the basket and the customer
			// display until the process restarted — while the tender path
			// already recomputes it as 0, so the screen and the recorded sale
			// would disagree as well. Leaving TR restores the still-stored
			// rate by the same path (the suppression never erases it).
			// ut-docs#1027: live-apply a country-derived locale change too
			// (harmless no-op via SetDefaultLocale's own empty-guard when
			// derivedLocale above didn't fire) — same reasoning as the
			// KeyLocale case just above.
			httpx.SetDefaultLocale(st.Locale)
			newCfg := pos.Config{
				TaxInclusive:                 st.TaxInclusive,
				TaxRateBasisPoints:           st.TaxRatePct * 100,
				ServiceChargeRateBasisPoints: common.EffectiveServiceChargeRateBP(st),
			}
			d.Engine.SetConfig(newCfg)
			if d.KioskEngine != nil {
				d.KioskEngine.SetConfig(newCfg)
			}
		}
		settingsRespondSaved(w, r, elev)
	})
}

// Wire-level names of the two per-country signing-device posture keys
// (ADR-0083, ut-docs#1767). These are the LOGICAL identifiers the settings
// form and the cloud set_setting directive still send — deliberately
// unchanged, so no template, i18n string or directive contract moved with
// the split — and NOT storage keys: the row actually read and written is
// fiscal.SigningDeviceConfiguredKey(country) /
// fiscal.SigningDeviceFailingSinceKey(country) for the shop's current
// country, resolved by resolveFiscalPostureKey. Nothing outside the upsert
// handler should name these; every other reader/writer goes through the
// fiscal package's key functions directly.
const (
	wireKeySigningDeviceConfigured   = "fiscal.signing_device_configured"
	wireKeySigningDeviceFailingSince = "fiscal.signing_device_failing_since"
)

// resolveFiscalPostureKey maps an incoming /api/settings/upsert key onto
// (logical, storage): logical is the wire-level name every fiscal-posture
// gate in the upsert handler switches on, storage is the row that is
// actually read and written. Three shapes reach here:
//
//   - the flat logical name ("fiscal.signing_device_configured"): resolved
//     to the CURRENT country's row, so the UI/directive contract is
//     unchanged by the split (ADR-0083 point 4);
//   - an explicit per-country row ("fiscal.signing_device_configured.de"),
//     which the All-settings card's free-text key input can post: stored
//     under the normalised per-country name, but classified as the SAME
//     logical key so it gets the same owner-only permission check, the same
//     fiscal-toggle audit and — for failing_since — the same unconditional
//     400. Without this the split would have opened a manager-level side
//     door onto the very rows the flat name is guarded for;
//   - anything else: passed through untouched, both values equal to key.
func resolveFiscalPostureKey(d *common.Deps, key string) (logical, storage string) {
	for _, w := range []struct {
		wire    string
		resolve func(string) string
	}{
		{wireKeySigningDeviceConfigured, fiscal.SigningDeviceConfiguredKey},
		{wireKeySigningDeviceFailingSince, fiscal.SigningDeviceFailingSinceKey},
	} {
		if key == w.wire {
			return w.wire, w.resolve(d.CurrentState().Country)
		}
		if strings.HasPrefix(key, w.wire+".") {
			return w.wire, w.resolve(strings.TrimPrefix(key, w.wire+"."))
		}
	}
	return key, key
}
