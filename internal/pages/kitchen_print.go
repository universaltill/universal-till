package pages

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

// kitchenStation is the header printed at the top of every kitchen ticket. It
// is printed on thermal paper (latin, like the receipt's "Receipt"/"TOTAL"
// labels), not a UI string, so it stays a constant rather than an i18n key.
const kitchenStation = "KITCHEN"

// buildKitchenTicket assembles the kitchen ticket for a completed sale: item
// names + quantities, order number, order type and timestamp, no prices.
// Table is included when the sale model carries one (not yet — optional
// field, no source for it today).
func buildKitchenTicket(ctx context.Context, d *common.Deps, receiptNo string) (print.KitchenTicket, error) {
	detail, ok, err := data.NewPOSRepo(d.Db).GetSaleDetail(ctx, receiptNo)
	if err != nil {
		return print.KitchenTicket{}, err
	}
	if !ok {
		return print.KitchenTicket{}, fmt.Errorf("receipt %s not found", receiptNo)
	}
	cfg := printerConfig(ctx, d)
	t := print.KitchenTicket{
		Station:   kitchenStation,
		OrderNo:   detail.ReceiptNo,
		OrderType: detail.OrderType,
		Timestamp: detail.CreatedAt,
		Charset:   cfg.Charset,
	}
	for _, l := range detail.Lines {
		t.Items = append(t.Items, print.KitchenItem{
			Qty:       strconv.FormatFloat(l.Qty, 'f', -1, 64),
			Name:      l.Name,
			Modifiers: l.Modifiers,
		})
	}
	return t, nil
}

// printKitchen renders a kitchen ticket for a sale and sends it to the kitchen
// printer (a SEPARATE target from the receipt printer). Returns an error when
// no kitchen printer is configured or the send fails.
func printKitchen(ctx context.Context, d *common.Deps, receiptNo string) error {
	cfg := printerConfig(ctx, d)
	tr, err := print.TransportForAddress(cfg.KitchenAddress)
	if err != nil {
		return err
	}
	if tr == nil {
		return fmt.Errorf("no kitchen printer configured")
	}
	ticket, err := buildKitchenTicket(ctx, d, receiptNo)
	if err != nil {
		return err
	}
	return tr.Print(ctx, print.RenderKitchenTicket(ticket))
}

// printKitchenAsync sends a kitchen ticket without ever blocking the caller:
// fired as a goroutine, a missing printer is a no-op and failures are audited.
func printKitchenAsync(d *common.Deps, receiptNo string, actorID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if !printerConfig(ctx, d).KitchenEnabled() {
			return
		}
		if err := printKitchen(ctx, d, receiptNo); err != nil {
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
		if !printerConfig(r.Context(), d).KitchenEnabled() {
			fail(http.StatusBadRequest, "kitchen.print.off")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		err := printKitchen(ctx, d, receiptNo)
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "sale", receiptNo, "kitchen_printed",
			map[string]any{"ok": err == nil}, time.Now().UTC().Format(time.RFC3339), "")
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "kitchen.print.failed"))
			return
		}
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "kitchen.print.done"))
	})
}
