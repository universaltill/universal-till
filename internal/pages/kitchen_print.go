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

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

// kitchenStation is the header printed at the top of the DEFAULT kitchen
// ticket (unrouted lines, or every line when no kitchen stations are
// configured). It is printed on thermal paper (latin, like the receipt's
// "Receipt"/"TOTAL" labels), not a UI string, so it stays a constant rather
// than an i18n key. Station-routed tickets print the station's own name
// instead (ut-docs#516).
const kitchenStation = "KITCHEN"

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
	return kitchenTicketFor(detail, cfg, kitchenStation, kitchenItemsFor(detail.Lines)), nil
}

// kitchenTicketFor assembles one ticket from a sale's header fields, a
// station header and a subset of the sale's lines. Shared by the legacy
// single-ticket path and the per-station tickets so their byte layout can
// never drift apart.
func kitchenTicketFor(detail data.SaleDetail, cfg print.Config, station string, items []print.KitchenItem) print.KitchenTicket {
	return print.KitchenTicket{
		Station:   station,
		OrderNo:   detail.ReceiptNo,
		OrderType: detail.OrderType,
		Timestamp: detail.CreatedAt,
		Charset:   cfg.Charset,
		Items:     items,
	}
}

func kitchenItemsFor(lines []data.SaleDetailLine) []print.KitchenItem {
	var items []print.KitchenItem
	for _, l := range lines {
		items = append(items, print.KitchenItem{
			Qty:       strconv.FormatFloat(l.Qty, 'f', -1, 64),
			Name:      l.Name,
			Modifiers: l.Modifiers,
		})
	}
	return items
}

// kitchenTarget is one destination ticket: a station header, the printer
// address to send to, and the sale lines routed there.
type kitchenTarget struct {
	station  string // ticket header + audit label
	address  string
	items    []print.KitchenItem
	rendered []byte // ESC/POS bytes, rendered at build time
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
			items:   kitchenItemsFor(g.lines),
		})
	}
	if len(defaultLines) > 0 {
		targets = append(targets, kitchenTarget{
			station: kitchenStation,
			address: cfg.KitchenAddress,
			items:   kitchenItemsFor(defaultLines),
		})
	}
	// Render each ticket now so a per-target send failure later can't be a
	// build failure in disguise.
	for i := range targets {
		t := kitchenTicketFor(detail, cfg, targets[i].station, targets[i].items)
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
	if printerConfig(ctx, d).KitchenEnabled() {
		return true
	}
	stations, err := data.NewPOSRepo(d.Db).ListKitchenStations(ctx)
	if err != nil {
		return false
	}
	for _, s := range stations {
		if s.Enabled && s.DestinationType == "printer" && strings.TrimSpace(s.PrinterAddress) != "" {
			return true
		}
	}
	return false
}

// printKitchenAsync sends kitchen tickets without ever blocking the caller:
// fired as a goroutine, a missing printer is a no-op and failures are
// audited (per target, inside printKitchen) — the sale NEVER fails or waits
// no matter how many targets are down. Tracked on d.AsyncWork (ut-docs#425),
// same reasoning as printReceiptAsync.
func printKitchenAsync(d *common.Deps, receiptNo string, actorID string) {
	d.AsyncWork.Add(1)
	go func() {
		defer d.AsyncWork.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if !kitchenPrintingEnabled(ctx, d) {
			return
		}
		if _, _, err := printKitchen(ctx, d, receiptNo, actorID); err != nil {
			_ = data.NewPOSRepo(d.Db).InsertAudit(ctx, nil, actorID, "sale", receiptNo, "kitchen_print_failed",
				map[string]any{"error": err.Error()}, time.Now().UTC().Format(time.RFC3339), "")
		}
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
