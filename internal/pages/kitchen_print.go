package pages

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/print"
)

// kitchenStation is the internal identifier and audit/failure-label for the
// DEFAULT kitchen ticket (unrouted lines, or every line when no kitchen
// stations are configured) — the "station" field written to InsertAudit
// calls and kitchenSendFailure.Station. It stays a FIXED, un-translated
// string on purpose: audits/logs need to stay grep-friendly and
// locale-independent regardless of the shop's configured language. The
// PRINTED ticket header for the default bucket is translated separately in
// kitchenTicketFor via the kitchen.ticket.station_default i18n key
// (ut-docs#261) so kitchen staff read it in their own language. Station-
// routed tickets print+audit the station's own shop-entered name instead
// (data.KitchenStation.Name, ut-docs#516) — that's the shop's own text,
// never translated, untouched by this card.
const kitchenStation = "KITCHEN"

// kitchenOrderTypeLabel translates a persisted sale order_type for display
// on a kitchen ticket. "" (dine-in) stays "" — printing nothing is the
// product decision (ut-docs#261), not a missing translation. Only
// pos.OrderTypeTakeaway ("takeaway") exists in the domain today; an
// unrecognized future value falls back to the raw string rather than
// erroring, so a new order type added later degrades to un-translated
// English instead of breaking kitchen printing. charset selects the
// ASCII-safe fallback below.
func kitchenOrderTypeLabel(locale, charset, orderType string) string {
	switch orderType {
	case "":
		return ""
	case pos.OrderTypeTakeaway:
		return kitchenTicketText(locale, charset, "basket.order_type.takeaway")
	case pos.OrderTypeMixed:
		// ADR-0073: a mixed sale prints a translated header and each
		// line carries its own marker (kitchenItemsFor).
		return kitchenTicketText(locale, charset, "basket.order_type.mixed")
	default:
		return orderType
	}
}

// kitchenLineModeLabel is the per-line marker for a MIXED sale's items
// (ut-docs#1181, ADR-0073 Decision 7). Uniform sales get "" for every line
// so their ticket bytes are unchanged.
func kitchenLineModeLabel(locale, charset, saleOrderType, lineOrderType string) string {
	if saleOrderType != pos.OrderTypeMixed {
		return ""
	}
	if lineOrderType == pos.OrderTypeTakeaway {
		return kitchenTicketText(locale, charset, "basket.order_type.takeaway")
	}
	return kitchenTicketText(locale, charset, "basket.order_type.dine_in")
}

// kitchenTicketText translates key for locale, with a Latin-safe fallback
// (review finding, ut-docs#261; extended to cp858 in ut-docs#1243): both
// printer.charset=="ascii" and =="cp858" are single-byte code pages that
// can't render non-Latin scripts — encodeText (internal/print) maps every
// unmappable rune to "?" under either, so an ar/fa translation would print
// as a run of question marks. Before ut-docs#261, kitchen tickets were
// hardcoded English and never hit this path at all; now that they carry
// real translated text, degrade to the English string (always ASCII, so
// safe under both restricted charsets) rather than garbage. utf8-charset
// setups (the default) are unaffected.
func kitchenTicketText(locale, charset, key string) string {
	v := httpx.T(locale, key)
	restricted := charset == "ascii" || charset == "cp858"
	if !restricted || isASCII(v) {
		return v
	}
	return httpx.T("en", key)
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII {
			return false
		}
	}
	return true
}

// buildKitchenTicket assembles the legacy single kitchen ticket for a
// completed sale: item names + quantities, order number, order type and
// timestamp, no prices. Kept as the canonical zero-stations ticket — with no
// kitchen stations configured, printKitchen sends exactly these bytes
// (pinned by TestPrintKitchen_ZeroStations_ByteIdenticalLegacyTicket).
func buildKitchenTicket(ctx context.Context, d *common.Deps, receiptNo string) (print.KitchenTicket, error) {
	detail, ok, err := data.NewPOSRepo(d.Db).GetSaleDetail(ctx, receiptNo)
	if err != nil {
		return print.KitchenTicket{}, err
	}
	if !ok {
		return print.KitchenTicket{}, fmt.Errorf("receipt %s not found", receiptNo)
	}
	cfg := printerConfig(ctx, d)
	locale := httpx.DefaultLocale()
	return kitchenTicketFor(detail, cfg, locale, kitchenStation, true, kitchenItemsFor(detail, cfg, locale, detail.Lines)), nil
}

// kitchenTicketFor assembles one ticket from a sale's header fields, a
// station header and a subset of the sale's lines. Shared by the legacy
// single-ticket path and the per-station tickets so their byte layout can
// never drift apart. locale resolves the ticket's translated text
// (ut-docs#261) — callers pass httpx.DefaultLocale() (the shop's
// configured locale; there's no per-cashier request to resolve from here,
// these tickets are built server-side). isDefault is an explicit flag, NOT
// a `station == kitchenStation` string comparison (review finding,
// ut-docs#261): a shop can name a real station "KITCHEN" (no name
// validation in kitchen_stations_repo.go), and comparing by value would
// wrongly translate that shop's own text — exactly the case this ticket's
// own comment says must never happen.
func kitchenTicketFor(detail data.SaleDetail, cfg print.Config, locale, station string, isDefault bool, items []print.KitchenItem) print.KitchenTicket {
	header := station
	if isDefault {
		header = kitchenTicketText(locale, cfg.Charset, "kitchen.ticket.station_default")
	}
	return print.KitchenTicket{
		Station:    header,
		OrderNo:    detail.ReceiptNo,
		OrderLabel: kitchenTicketText(locale, cfg.Charset, "kitchen.ticket.order_label"),
		OrderType:  kitchenOrderTypeLabel(locale, cfg.Charset, detail.OrderType),
		// Table (ut-docs#820, ADR-0054): detail.TableLabel is already
		// resolved by GetSaleDetail's join against `tables` -- "" when no
		// table was assigned, which print.KitchenTicket already treats as
		// "print nothing" (see its own doc comment).
		Table:     detail.TableLabel,
		Timestamp: detail.CreatedAt,
		Charset:   cfg.Charset,
		Items:     items,
	}
}

func kitchenItemsFor(detail data.SaleDetail, cfg print.Config, locale string, lines []data.SaleDetailLine) []print.KitchenItem {
	var items []print.KitchenItem
	for _, l := range lines {
		items = append(items, print.KitchenItem{
			Qty:       strconv.FormatFloat(l.Qty, 'f', -1, 64),
			Name:      l.Name,
			Modifiers: l.Modifiers,
			Mode:      kitchenLineModeLabel(locale, cfg.Charset, detail.OrderType, l.OrderType),
		})
	}
	return items
}

// kitchenTarget is one destination ticket: a station header, the printer
// address to send to, and the sale lines routed there.
type kitchenTarget struct {
	station   string // ticket header + audit label
	isDefault bool   // the unrouted/legacy bucket, not a real shop station (ut-docs#261)
	address   string
	items     []print.KitchenItem
	rendered  []byte // ESC/POS bytes, rendered at build time
}

// kitchenSendFailure reports one target that could not be printed; Station
// is the station name (or the default KITCHEN constant for the fallback
// bucket) for audits and operator feedback.
type kitchenSendFailure struct {
	Station string
	Err     error
}

// buildKitchenTargets resolves station routing for a sale (ut-docs#516) and
// groups its lines into one ticket per destination:
//
//   - a line whose item resolves to stations (item routes override category
//     routes — data.POSRepo.ResolveKitchenStations owns that precedence)
//     appears on each of those stations' tickets — a line routed to two
//     stations is deliberately duplicated on both;
//   - a line that resolves to no station falls into ONE shared default
//     bucket: Station "KITCHEN", sent to the legacy printer.kitchen_addr —
//     with zero stations configured this is the entire sale, byte-identical
//     to the pre-#516 single ticket.
//
// Station tickets come first (sorted by name, deterministic), the default
// bucket last. Only 'printer' stations receive tickets this slice
// ('display' is ut-docs#544); a line whose every resolved station is
// non-printer joins the default bucket rather than silently vanishing.
func buildKitchenTargets(ctx context.Context, d *common.Deps, receiptNo string) ([]kitchenTarget, error) {
	repo := data.NewPOSRepo(d.Db)
	detail, ok, err := repo.GetSaleDetail(ctx, receiptNo)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("receipt %s not found", receiptNo)
	}
	cfg := printerConfig(ctx, d)
	locale := httpx.DefaultLocale()

	itemIDs := make([]string, 0, len(detail.Lines))
	for _, l := range detail.Lines {
		if l.ItemID != "" {
			itemIDs = append(itemIDs, l.ItemID)
		}
	}
	routes, err := repo.ResolveKitchenStations(ctx, itemIDs)
	if err != nil {
		return nil, err
	}

	type group struct {
		station data.KitchenStation
		lines   []data.SaleDetailLine
	}
	groups := map[string]*group{}
	var defaultLines []data.SaleDetailLine
	for _, l := range detail.Lines {
		routed := false
		for _, s := range routes[l.ItemID] {
			if s.DestinationType != "printer" {
				continue // display stations are ut-docs#544
			}
			// A station with no configured address can't be sent to — fall
			// back to the default bucket instead of silently dropping the
			// line (code review, ut-docs#516: creation/edit now requires an
			// address, but this stays defense-in-depth against pre-existing
			// or otherwise-blank data).
			if strings.TrimSpace(s.PrinterAddress) == "" {
				continue
			}
			g, ok := groups[s.ID]
			if !ok {
				g = &group{station: s}
				groups[s.ID] = g
			}
			g.lines = append(g.lines, l)
			routed = true
		}
		if !routed {
			defaultLines = append(defaultLines, l)
		}
	}

	ordered := make([]*group, 0, len(groups))
	for _, g := range groups {
		ordered = append(ordered, g)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].station.Name != ordered[j].station.Name {
			return ordered[i].station.Name < ordered[j].station.Name
		}
		return ordered[i].station.ID < ordered[j].station.ID
	})

	var targets []kitchenTarget
	for _, g := range ordered {
		targets = append(targets, kitchenTarget{
			station: g.station.Name,
			address: g.station.PrinterAddress,
			items:   kitchenItemsFor(detail, cfg, locale, g.lines),
		})
	}
	if len(defaultLines) > 0 {
		targets = append(targets, kitchenTarget{
			station:   kitchenStation,
			isDefault: true,
			address:   cfg.KitchenAddress,
			items:     kitchenItemsFor(detail, cfg, locale, defaultLines),
		})
	}
	// Render each ticket now so a per-target send failure later can't be a
	// build failure in disguise.
	for i := range targets {
		t := kitchenTicketFor(detail, cfg, locale, targets[i].station, targets[i].isDefault, targets[i].items)
		targets[i].rendered = print.RenderKitchenTicket(t)
	}
	return targets, nil
}

// printKitchen renders one kitchen ticket per routed destination and sends
// each independently, CONCURRENTLY, each on its own fresh timeout — one
// dead printer must never stop, slow down, or steal timeout budget from the
// others (offline-first; code review, ut-docs#516: a shared serial deadline
// meant 3+ unreachable network stations could starve a healthy one late in
// the list, and its audit write inherited the same expired context). Every
// failed target is audited separately (kitchen_print_failed, with the
// station label), on its own fresh background context so a caller-timeout
// can never suppress the audit row for the failure that caused it. The
// returned error is non-nil only when the tickets could not be built at all
// (sale missing, DB error); per-target send failures come back in failures
// so the manual print API can report partial success while
// printKitchenAsync ignores them.
func printKitchen(ctx context.Context, d *common.Deps, receiptNo, actorID string) (total int, failures []kitchenSendFailure, err error) {
	targets, err := buildKitchenTargets(ctx, d, receiptNo)
	if err != nil {
		return 0, nil, err
	}
	posRepo := data.NewPOSRepo(d.Db)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, target := range targets {
		wg.Add(1)
		go func(target kitchenTarget) {
			defer wg.Done()
			sendCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if sendErr := sendKitchenTicket(sendCtx, target); sendErr != nil {
				mu.Lock()
				failures = append(failures, kitchenSendFailure{Station: target.station, Err: sendErr})
				mu.Unlock()
				auditCtx, auditCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer auditCancel()
				_ = posRepo.InsertAudit(auditCtx, nil, actorID, "sale", receiptNo, "kitchen_print_failed",
					map[string]any{"error": sendErr.Error(), "station": target.station},
					time.Now().UTC().Format(time.RFC3339), "")
			}
		}(target)
	}
	wg.Wait()
	return len(targets), failures, nil
}

func sendKitchenTicket(ctx context.Context, target kitchenTarget) error {
	tr, err := print.TransportForAddress(target.address)
	if err != nil {
		return err
	}
	if tr == nil {
		return fmt.Errorf("no kitchen printer configured")
	}
	return tr.Print(ctx, target.rendered)
}

// kitchenPrintingEnabled reports whether ANY kitchen destination exists: the
// legacy default printer (printer.kitchen_addr) or at least one enabled
// station with a printer address. A shop that only configures stations —
// and never fills the legacy setting — must still print (ut-docs#516).
func kitchenPrintingEnabled(ctx context.Context, d *common.Deps) bool {
	enabled, _ := kitchenPrintingEnabledChecked(ctx, d)
	return enabled
}

// kitchenPrintingEnabledChecked is kitchenPrintingEnabled plus the first
// genuine read error hit (settings, or ListKitchenStations), if any — so a
// caller that must not confuse "couldn't tell" with "off" (the async print
// path, ut-docs#1153) can react to it instead of silently no-op'ing.
func kitchenPrintingEnabledChecked(ctx context.Context, d *common.Deps) (bool, error) {
	cfg, cfgErr := printerConfigChecked(ctx, d)
	if cfgErr != nil {
		return false, cfgErr
	}
	if cfg.KitchenEnabled() {
		return true, nil
	}
	stations, err := data.NewPOSRepo(d.Db).ListKitchenStations(ctx)
	if err != nil {
		return false, err
	}
	for _, s := range stations {
		if s.Enabled && s.DestinationType == "printer" && strings.TrimSpace(s.PrinterAddress) != "" {
			return true, nil
		}
	}
	return false, nil
}

// printKitchenAsync sends kitchen tickets without ever blocking the caller:
// fired as a goroutine, a missing printer is a no-op and per-target failures
// are audited (inside printKitchen, ut-docs#516, each on its own fresh
// context) — the sale NEVER fails or waits no matter how many targets are
// down. The /orders warning (ut-docs#517a) is set when the ticket build
// fails OR any target fails, cleared once every resolved target succeeds;
// "no attempt" (kitchen printing off everywhere, or nothing resolved to
// send) must neither overwrite a real prior failure nor falsely clear one.
// Tracked on d.AsyncWork (ut-docs#425), same reasoning as printReceiptAsync.
func printKitchenAsync(d *common.Deps, receiptNo string, actorID string) {
	d.AsyncWork.Add(1)
	go func() {
		defer d.AsyncWork.Done()
		ctx, cancel := context.WithTimeout(context.Background(), printAsyncTimeout)
		defer cancel()
		posRepo := data.NewPOSRepo(d.Db)
		enabled, enabledErr := kitchenPrintingEnabledChecked(ctx, d)
		if enabledErr != nil {
			// A genuine read failure (settings or station list), not
			// "kitchen printing off everywhere" (ut-docs#1153) — must not
			// take the silent no-op path below.
			wctx, wcancel := recordPrintFailureCtx()
			defer wcancel()
			_ = posRepo.InsertAudit(wctx, nil, actorID, "sale", receiptNo, "kitchen_print_failed",
				map[string]any{"error": "kitchen settings read failed: " + enabledErr.Error()}, time.Now().UTC().Format(time.RFC3339), "")
			_ = posRepo.SetKitchenPrintFailed(wctx, receiptNo, time.Now().UTC().Format(time.RFC3339))
			return
		}
		if !enabled {
			// No attempt (kitchen printing off everywhere) — must neither
			// overwrite a real prior failure nor falsely clear one.
			return
		}
		total, failures, err := printKitchenFn(ctx, d, receiptNo, actorID)
		if err != nil {
			// Fresh context: a hung/out-of-paper printer burns the whole
			// print budget before failing, so ctx is already expired here —
			// see recordPrintFailureCtx.
			wctx, wcancel := recordPrintFailureCtx()
			defer wcancel()
			_ = posRepo.InsertAudit(wctx, nil, actorID, "sale", receiptNo, "kitchen_print_failed",
				map[string]any{"error": err.Error()}, time.Now().UTC().Format(time.RFC3339), "")
			_ = posRepo.SetKitchenPrintFailed(wctx, receiptNo, time.Now().UTC().Format(time.RFC3339))
			return
		}
		if total == 0 {
			// Nothing resolved to send — not a failure, nothing was attempted.
			return
		}
		if len(failures) > 0 {
			// Per-target failures are already audited inside printKitchen on
			// their own fresh per-target context; this just makes the
			// CURRENT state visible on /orders, also on a fresh context —
			// the wg.Wait() inside printKitchen can itself run long enough
			// to expire the outer ctx above (same reasoning as the err!=nil
			// branch: this loop's whole point is a printer that didn't fail
			// fast).
			wctx, wcancel := recordPrintFailureCtx()
			defer wcancel()
			_ = posRepo.SetKitchenPrintFailed(wctx, receiptNo, time.Now().UTC().Format(time.RFC3339))
			return
		}
		// Every target succeeded — clear any stale flag from an earlier attempt.
		_ = posRepo.SetKitchenPrintFailed(ctx, receiptNo, "")
	}()
}

// registerKitchenPrintAPI mounts the manual kitchen-ticket print endpoint. Like
// labels, any operator may fire it — sending an order to the kitchen is normal
// floor work, not a manager action.
func registerKitchenPrintAPI(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)

	mux.HandleFunc("POST /api/print/kitchen", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		receiptNo := strings.TrimSpace(r.Form.Get("receipt_no"))
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fail := func(status int, key string) {
			w.WriteHeader(status)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, key))
		}
		if receiptNo == "" {
			fail(http.StatusBadRequest, "kitchen.print.no_receipt")
			return
		}
		if !kitchenPrintingEnabled(r.Context(), d) {
			fail(http.StatusBadRequest, "kitchen.print.off")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		total, failures, err := printKitchen(ctx, d, receiptNo, getSessionUserID(r))
		ok := err == nil && len(failures) == 0
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "sale", receiptNo, "kitchen_printed",
			map[string]any{"ok": ok}, time.Now().UTC().Format(time.RFC3339), "")
		// Keep the /orders warning honest (ut-docs#517a): any failure (total
		// failure to build tickets, or a partial failure with some stations
		// down) flags the sale; a fully successful manual print clears a
		// stale flag, so a manually-fixed printer un-sticks the warning.
		if ok {
			_ = posRepo.SetKitchenPrintFailed(r.Context(), receiptNo, "")
		} else {
			_ = posRepo.SetKitchenPrintFailed(r.Context(), receiptNo, time.Now().UTC().Format(time.RFC3339))
		}
		switch {
		case err != nil:
			fail(http.StatusBadGateway, "kitchen.print.failed")
		case len(failures) == 0:
			fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "kitchen.print.done"))
		case len(failures) < total:
			// Partial: some stations printed, some did not.
			fail(http.StatusBadGateway, "kitchen.print.partial")
		default:
			fail(http.StatusBadGateway, "kitchen.print.failed")
		}
	})
}
