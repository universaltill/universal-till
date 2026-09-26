package pages

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/httpx"
)

// Public "try the till" demo mode (ADR-0113 §1.3–§1.5, §1.8; ut-docs#2687).
//
// newDemoMiddleware is installed by Init ONLY when cfg.Demo is on, as the
// outermost handler inside recoverMiddleware. Every request, in order:
//
//  1. must carry X-UT-Demo-Token equal to cfg.DemoToken (constant-time;
//     an empty configured token never matches) — only the demo broker,
//     which adds the header and strips any a visitor sends, can talk to a
//     demo till, so a till tricked into dialling another till's loopback
//     port gets 403;
//  2. must resolve (mux.Handler) to a pattern on demoAllowedRoutes and not
//     on demoDeniedRoutes — all methods, GET included; an unmatched path
//     (pattern "") or a new, unclassified route is refused (fail closed),
//     and the catch-all "/" only serves the root path (demoRouteAllowed);
//  3. has its body read through http.MaxBytesReader, capped at 64 KiB, and
//     buffered (then handed to the handler unchanged);
//  4. if multipart, must carry no part with a non-empty filename — uploads
//     cannot be told apart by route (category images ride on the ordinary
//     category form), so this is the upload block.
//
// A refusal answers 403 (413 for an oversized body) with the localised
// "demo.not_available" message: a JSON error envelope for /api/, a
// text/html fragment for htmx requests (app.js force-swaps a non-empty
// text/html error fragment into the request's target, the way admin
// handlers show a specific failure), plain text otherwise.

// demoTokenHeader carries the per-till secret the broker adds (ADR-0113 §1.3).
const demoTokenHeader = "X-UT-Demo-Token"

// demoMaxBodyBytes is the per-request body cap (ADR-0113 §1.8).
const demoMaxBodyBytes = 64 << 10

func newDemoMiddleware(next http.Handler, mux *http.ServeMux, token string) http.Handler {
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ConstantTimeCompare answers 0 for unequal lengths; the empty-want
		// check keeps an unset token from matching an empty header.
		if len(want) == 0 || subtle.ConstantTimeCompare([]byte(r.Header.Get(demoTokenHeader)), want) != 1 {
			writeDemoRefusal(w, r, http.StatusForbidden)
			return
		}

		if !demoRouteAllowed(mux, r) {
			writeDemoRefusal(w, r, http.StatusForbidden)
			return
		}

		if r.Body != nil && r.Body != http.NoBody {
			buf, err := io.ReadAll(http.MaxBytesReader(w, r.Body, demoMaxBodyBytes))
			_ = r.Body.Close()
			if err != nil {
				var tooBig *http.MaxBytesError
				if errors.As(err, &tooBig) {
					writeDemoRefusal(w, r, http.StatusRequestEntityTooLarge)
				} else {
					writeDemoRefusal(w, r, http.StatusBadRequest)
				}
				return
			}
			if hasMultipartFile(r.Header.Get("Content-Type"), buf) {
				writeDemoRefusal(w, r, http.StatusForbidden)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(buf))
			r.ContentLength = int64(len(buf))
		}

		next.ServeHTTP(w, r)
	})
}

// demoRouteAllowed reports whether r resolves to an allowed, not-denied
// pattern. The catch-all index pattern "/" is special: ServeMux resolves
// every otherwise-unmatched request to it — unknown paths, a wrong-method
// hit on a denied pattern, and plugin-owned pages the index handler
// dispatches via findPageEntry (plugin_page.go) — so "/" is allowed only
// for the root path itself (the sale screen). /help has its own pattern,
// so the index handler's /help branch is never needed here.
func demoRouteAllowed(mux *http.ServeMux, r *http.Request) bool {
	_, pattern := mux.Handler(r)
	if pattern == "" || !demoAllowedRoutes[pattern] || demoDeniedRoutes[pattern] {
		return false
	}
	if pattern == "/" && r.URL.Path != "/" {
		return false
	}
	return true
}

// hasMultipartFile reports whether body, sent with Content-Type ct, is a
// multipart message with any part carrying a non-empty filename — the same
// test mime/multipart's ReadForm uses to decide "file, not value". A
// multipart Content-Type that doesn't parse, or a body that doesn't, counts
// as a file (fail closed); a non-multipart body never does.
func hasMultipartFile(ct string, body []byte) bool {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "multipart/") {
		return false
	}
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") || params["boundary"] == "" {
		return true
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return false
		}
		if err != nil {
			return true
		}
		if part.FileName() != "" {
			return true
		}
	}
}

// writeDemoRefusal answers a request the demo does not serve, localised.
func writeDemoRefusal(w http.ResponseWriter, r *http.Request, status int) {
	msg := httpx.T(httpx.RequestLocale(r), "demo.not_available")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch {
	case r.Header.Get("HX-Request") != "":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `<p class="muted" role="alert" data-demo-refusal>`+html.EscapeString(msg)+`</p>`)
	case strings.HasPrefix(r.URL.Path, "/api/"):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  nil,
			"error": map[string]string{"code": "demo_not_available", "message": msg},
		})
	default:
		http.Error(w, msg, status)
	}
}

// demoDeniedRoutes are refused in the demo whatever the allow-list says
// (ADR-0113 §1.5). Exact mux patterns, grouped by why.
var demoDeniedRoutes = map[string]bool{
	// Plugins: install, update, side-load, rollback, enable/disable, trust and
	// permission grants, the marketplace, the plugin store, plugin-owned
	// pages/settings/actions (plugin settings can take endpoints) and the
	// marketplace v1 protocol stub (ADR-0113 §1.5).
	"/api/plugins/marketplace":                        true,
	"/api/plugins/permissions/grant":                  true,
	"/api/plugins/permissions/revoke":                 true,
	"/api/plugins/trust":                              true,
	"/plugin/":                                        true,
	"/plugins":                                        true,
	"/plugins/store":                                  true,
	"/v1/install/bundles/export":                      true,
	"/v1/install/bundles/import":                      true,
	"/v1/install/intents":                             true,
	"/v1/install/status":                              true,
	"/v1/telemetry/plugins":                           true,
	"GET /api/plugins/check-updates":                  true,
	"GET /api/plugins/{id}/export":                    true,
	"GET /api/plugins/{id}/versions":                  true,
	"GET /plugins/{id}/settings":                      true,
	"POST /api/plugins/entries/{plugin}/{key}/action": true,
	"POST /api/plugins/import-from-file":              true,
	"POST /api/plugins/install-from-marketplace":      true,
	"POST /api/plugins/store/delete-download":         true,
	"POST /api/plugins/store/download":                true,
	"POST /api/plugins/store/install":                 true,
	"POST /api/plugins/{id}/disable":                  true,
	"POST /api/plugins/{id}/enable":                   true,
	"POST /api/plugins/{id}/rollback":                 true,
	"POST /api/plugins/{id}/settings":                 true,
	"POST /api/plugins/{id}/uninstall":                true,
	"POST /api/plugins/{id}/update":                   true,
	// Shop type: saving it runs builtinlayouts.Sync, which installs/activates
	// the built-in layout plugins via plugins.PersistManifest — a plugin
	// install, so always denied (ADR-0113 §1.5).
	"POST /api/settings/shop-type": true,
	// Catalogue/data/voucher import and saving exports to the filesystem
	// (ADR-0113 §1.5: file ingest).
	"GET /import":                   true,
	"GET /settings/vouchers/import": true,
	"POST /api/catalog/export-save": true,
	"POST /api/data/export":         true,
	"POST /api/data/import":         true,
	"POST /api/import":              true,
	"POST /api/vouchers/import":     true,
	// Backups: create, save-copy, download (a flagged demo database never
	// leaves the demo), restore, and restart.
	"GET /api/backup/download/{name}":   true,
	"POST /api/backup/now":              true,
	"POST /api/backup/restart-now":      true,
	"POST /api/backup/restore":          true,
	"POST /api/backup/save-copy/{name}": true,
	// Uploads with their own endpoint (item/variant photos, receipt logo) —
	// the multipart file filter also covers them; denied outright here.
	"POST /api/catalog/item/image":    true,
	"POST /api/catalog/variant/image": true,
	"POST /api/receipt-designer/logo": true,
	// Outbound network at request time: online barcode lookup, AI camera
	// identify and the report assistant.
	"GET /api/catalog/lookup":        true,
	"POST /api/pos/identify":         true,
	"POST /api/pos/identify/confirm": true,
	"POST /api/reports/ask":          true,
	// Cloud enrolment, registration, telemetry and LAN
	// sync/pairing/discovery/link (all /api/sync/*, /api/setup/*, the Tills
	// pages).
	"GET /api/enrol/devices":                    true,
	"GET /api/setup/discover-primaries":         true,
	"GET /api/setup/pair-status":                true,
	"GET /api/sync/admin":                       true,
	"GET /api/sync/assets":                      true,
	"GET /api/sync/assets/categories":           true,
	"GET /api/sync/assets/categories/file":      true,
	"GET /api/sync/assets/file":                 true,
	"GET /api/sync/discover-primaries":          true,
	"GET /api/sync/held-sales":                  true,
	"GET /api/sync/link":                        true,
	"GET /api/sync/orders":                      true,
	"GET /api/sync/orders/stream":               true,
	"GET /api/sync/pair-requests":               true,
	"GET /api/sync/pair-requests/{id}":          true,
	"GET /api/sync/pair-status":                 true,
	"GET /api/sync/ping":                        true,
	"GET /api/sync/plugins":                     true,
	"GET /api/sync/secrets-key":                 true,
	"GET /api/sync/snapshot":                    true,
	"GET /api/sync/stock":                       true,
	"GET /api/sync/tables":                      true,
	"GET /api/sync/vouchers/{id}":               true,
	"GET /sync-quarantine":                      true,
	"GET /tills":                                true,
	"GET /ui/tills/pending-pairings":            true,
	"GET /ui/tills/roster":                      true, // the denied /tills page's polled roster (ADR-0114 §10)
	"POST /api/enrol/claim-code":                true,
	"POST /api/enrol/now":                       true,
	"POST /api/settings/auto-register":          true,
	"POST /api/settings/telemetry":              true,
	"POST /api/setup/join":                      true,
	"POST /api/setup/language":                  true,
	"POST /api/setup/pair-start":                true,
	"POST /api/setup/pairing-restart":           true,
	"POST /api/setup/tax-plugin":                true,
	"POST /api/setup/tax-plugin-skip":           true,
	"POST /api/setup/update-apply":              true,
	"POST /api/setup/update-check":              true,
	"POST /api/sync/cloud-device":               true,
	"POST /api/sync/enroll":                     true,
	"POST /api/sync/enroll-token":               true,
	"POST /api/sync/held-sales/claim":           true,
	"POST /api/sync/held-sales/delete":          true,
	"POST /api/sync/held-sales/upsert":          true,
	"POST /api/sync/join":                       true,
	"POST /api/sync/orders/{receipt_no}/status": true,
	"POST /api/sync/pair-request":               true,
	"POST /api/sync/pair-requests/{id}/approve": true,
	"POST /api/sync/pair-requests/{id}/deny":    true,
	"POST /api/sync/pair-start":                 true,
	"POST /api/sync/pairing-restart":            true,
	"POST /api/sync/primary-proof":              true,
	"POST /api/sync/promote":                    true,
	"POST /api/sync/sales":                      true,
	"POST /api/sync/tables/claim":               true,
	"POST /api/sync/tables/release":             true,
	"POST /api/sync/tables/release-all":         true,
	"POST /api/sync/tills/{id}/revoke":          true,
	"POST /api/sync/users/apply":                true,
	"POST /api/sync/vouchers/{id}/redeem":       true,
	"POST /api/sync/vouchers/{id}/release":      true,
	// First-run setup wizard and first-owner creation (the demo template is
	// already set up).
	"GET /setup":           true,
	"POST /api/auth/setup": true,
	"POST /api/setup":      true,
	// Self-order kiosk (auth-exempt), kiosk controls and display-mode switch,
	// and anonymous order tracking /o/.
	"GET /api/self-order/cart":                 true,
	"GET /api/self-order/checkout":             true,
	"GET /api/self-order/grid":                 true,
	"GET /api/self-order/modifiers":            true,
	"GET /api/tables/{id}/qr":                  true,
	"GET /o/{token}":                           true,
	"GET /o/{token}/status":                    true,
	"GET /self-order":                          true,
	"GET /self-order/shop":                     true,
	"POST /api/self-order/checkout":            true,
	"POST /api/self-order/line":                true,
	"POST /api/self-order/order-type":          true,
	"POST /api/self-order/remove":              true,
	"POST /api/self-order/scan":                true,
	"POST /api/self-order/scan-with-modifiers": true,
	"POST /api/settings/display-mode":          true,
	"POST /api/settings/kiosk-idle-reset":      true,
	"POST /api/settings/kiosk-payment-mode":    true,
	// Updates: check, apply, Android install, schedule.
	"POST /api/settings/update-schedule": true,
	"POST /api/update/android-install":   true,
	"POST /api/update/apply":             true,
	"POST /api/update/check":             true,
	// Printers (set-up and every print), kitchen-station create/edit (they
	// carry a printer host) and printer discovery.
	"POST /api/kitchen-stations":                   true,
	"POST /api/kitchen-stations/discover-printers": true,
	"POST /api/kitchen-stations/{id}":              true,
	"POST /api/print/kitchen":                      true,
	"POST /api/print/labels":                       true,
	"POST /api/print/receipt/{receiptNo}":          true,
	"POST /api/print/test":                         true,
	"POST /api/receipt-designer/test":              true,
	"POST /api/reports/eod/print/{period}":         true,
	"POST /api/settings/printer":                   true,
	// Fiscal devices and registers: TSE override/provisioning, Türkiye ÖKC
	// device, §146a register.
	"GET /fiscal-device":                               true,
	"GET /fiscal-register":                             true,
	"POST /api/fiscal-device/confirm":                  true,
	"POST /api/fiscal-device/unpair":                   true,
	"POST /api/fiscal-register":                        true,
	"POST /api/fiscal-register/locations/{id}/address": true,
	"POST /api/fiscal-register/{id}/decommission":      true,
	"POST /api/fiscal/signing-override":                true,
	"POST /api/settings/retry-tse-provisioning":        true,
	// Bluetooth device pairing.
	"GET /api/bluetooth-devices":         true,
	"GET /bluetooth-devices":             true,
	"POST /api/bluetooth-devices/forget": true,
	"POST /api/bluetooth-devices/pair":   true,
	"POST /api/bluetooth-devices/scan":   true,
	// External plugin/device proxy.
	"/ext/": true,
	// Issue reports (upload) and diagnostic mode.
	"/report-issue":                              true,
	"GET /my-reports":                            true,
	"POST /api/issue-reports":                    true,
	"POST /api/settings/diagnostics/activate":    true,
	"POST /api/settings/diagnostics/cancel-stop": true,
	"POST /api/settings/diagnostics/stop":        true,
	// Desktop shell / OS: window mode and state, input heartbeat, launch on
	// startup, exit to OS (stays visible in the UI; the POST answers the demo
	// message).
	"GET /api/window-mode":                 true,
	"GET /ui/settings/window-mode-status":  true,
	"POST /api/settings/exit-to-os":        true,
	"POST /api/settings/launch-on-startup": true,
	"POST /api/settings/window-mode":       true,
	"POST /api/window/input-heartbeat":     true,
	// Raw settings key/value editor: takes any key, URL/host settings included.
	"POST /api/settings/upsert": true,
}

// demoAllowedRoutes are the only routes a demo till serves (ADR-0113 §1.4).
// Exact mux patterns, as mux.Handler reports them; grouped by area.
var demoAllowedRoutes = map[string]bool{
	// Static assets, health, theme and icons.
	"/healthz": true,
	"/public/": true,
	"GET /plugin-icons/{plugin}/{version}/{file...}": true,
	"GET /themes/{file}":                             true,
	"GET /ui/theme-sync":                             true,
	// Sign-in, lock and session.
	"GET /login":            true,
	"GET /pin":              true,
	"GET /ui/session-chip":  true,
	"POST /api/auth/login":  true,
	"POST /api/auth/logout": true,
	"POST /api/pin/change":  true,
	// Selling: sale screen ("/" — the root path only; see
	// demoRouteAllowed), basket, scan, lines, discounts, tenders (the
	// tender route is shared; card-terminal tenders are a later card),
	// holds/open orders, tables on the basket, suggestions, modifiers,
	// vouchers.
	"/":                                 true,
	"/api/pos/discount":                 true,
	"/api/pos/line":                     true,
	"/api/pos/order-type":               true,
	"/api/pos/remove":                   true,
	"/api/pos/reset":                    true,
	"/api/pos/sale/status":              true,
	"/api/pos/scan":                     true,
	"/api/pos/table":                    true,
	"/api/pos/tender":                   true,
	"/ui/basket":                        true,
	"/ui/buttons":                       true,
	"/ui/buttons/all/more":              true,
	"/ui/buttons/category":              true,
	"/ui/buttons/search":                true,
	"GET /ui/buttons/version":           true,
	"GET /api/vouchers/{id}":            true,
	"GET /open-orders":                  true,
	"GET /ui/held":                      true,
	"GET /ui/open-orders-badge":         true,
	"GET /ui/parked-orders":             true,
	"GET /ui/pos/modifiers":             true,
	"GET /ui/pos/table-picker":          true,
	"GET /ui/suggestions":               true,
	"POST /api/pos/held/table":          true,
	"POST /api/pos/hold":                true,
	"POST /api/pos/resume":              true,
	"POST /api/pos/scan-with-modifiers": true,
	"POST /api/vouchers/{id}/redeem":    true,
	"POST /open-orders/resume":          true,
	// Status chips every page loads (local reads only; denying them would
	// stamp the demo message into the nav).
	"GET /ui/bugreport-chip":   true,
	"GET /ui/diagnostics-chip": true,
	"GET /ui/fiscal-chip":      true,
	"GET /ui/main-till-status": true,
	"GET /ui/pairing-notice":   true,
	"GET /ui/plugin-buttons":   true,
	"GET /ui/sync-chip":        true,
	// Catalogue browse and edit, Designer, buttons, categories, modifiers,
	// option sets, inventory, tax codes (form edits; uploads are blocked by
	// the multipart file filter).
	"/api/buttons/add":                              true,
	"/api/buttons/delete-item":                      true,
	"/api/buttons/hide":                             true,
	"/api/buttons/remove":                           true,
	"/api/buttons/remove-from-grid":                 true,
	"/api/buttons/search":                           true,
	"/api/buttons/unhide":                           true,
	"/api/buttons/unhide-all":                       true,
	"/api/catalog/barcode":                          true,
	"/api/catalog/item":                             true,
	"/api/catalog/item-station-routes":              true,
	"/api/catalog/item/deactivate":                  true,
	"/api/catalog/item/update":                      true,
	"/api/catalog/modifier-group":                   true,
	"/api/catalog/modifier-group/attach":            true,
	"/api/catalog/modifier-group/attach-category":   true,
	"/api/catalog/modifier-group/delete":            true,
	"/api/catalog/modifier-group/delete-unassigned": true,
	"/api/catalog/modifier-group/detach":            true,
	"/api/catalog/modifier-group/detach-category":   true,
	"/api/catalog/modifier-group/opt-in":            true,
	"/api/catalog/modifier-group/opt-out":           true,
	"/api/catalog/modifier-option":                  true,
	"/api/catalog/variant":                          true,
	"/api/catalog/variant/deactivate":               true,
	"/catalog":                                      true,
	"/designer":                                     true,
	"/inventory":                                    true,
	"/items":                                        true,
	"/modifiers":                                    true,
	"/ui/inventory/stock-table":                     true,
	"GET /api/catalog/barcode-backfill":             true,
	"GET /api/catalog/export":                       true,
	"GET /api/catalog/item-variants":                true,
	"GET /api/catalog/item/icon-state":              true,
	"GET /api/catalog/modifier-groups-panel":        true,
	"GET /api/catalog/variant-options":              true,
	"GET /api/inventory/low-stock":                  true,
	"GET /api/inventory/return/lines":               true,
	"GET /catalog/option-sets":                      true,
	"GET /catalog/tax-codes":                        true,
	"GET /categories":                               true,
	"POST /api/buttons/reorder":                     true,
	"POST /api/catalog/barcode-backfill":            true,
	"POST /api/catalog/barcode/delete":              true,
	"POST /api/catalog/item-cost":                   true,
	"POST /api/catalog/item-lead-time":              true,
	"POST /api/catalog/item-reorder-level":          true,
	"POST /api/catalog/item/generate-variants":      true,
	"POST /api/catalog/item/icon":                   true,
	"POST /api/catalog/item/option-sets":            true,
	"POST /api/catalog/option-set":                  true,
	"POST /api/catalog/option-set-value":            true,
	"POST /api/catalog/tax-codes":                   true,
	"POST /api/catalog/tax-codes/update":            true,
	"POST /api/categories":                          true,
	"POST /api/categories/reorder":                  true,
	"POST /api/categories/{id}":                     true,
	"POST /api/categories/{id}/active":              true,
	"POST /api/designer/categories":                 true,
	"POST /api/designer/categories/reorder":         true,
	"POST /api/designer/categories/{id}":            true,
	"POST /api/designer/categories/{id}/active":     true,
	"POST /api/inventory/override":                  true,
	"POST /api/inventory/receipt":                   true,
	"POST /api/inventory/return":                    true,
	// Orders, kitchen display and routing, kiosk counter-order board (staff
	// side, local).
	"GET /api/orders/stream":                                    true,
	"GET /kiosk-counter-orders":                                 true,
	"GET /kitchen-display/{station_id}":                         true,
	"GET /kitchen-stations":                                     true,
	"GET /orders":                                               true,
	"GET /orders/{receipt}":                                     true,
	"GET /ui/kiosk-counter-orders":                              true,
	"GET /ui/kitchen-display/{station_id}":                      true,
	"GET /ui/orders":                                            true,
	"POST /api/kiosk-counter-orders/{id}/collect":               true,
	"POST /api/kitchen-stations/routes/categories/{categoryID}": true,
	"POST /api/kitchen-stations/routes/items/{itemID}":          true,
	"POST /api/kitchen-stations/{id}/active":                    true,
	"POST /api/orders/{receipt_no}/status":                      true,
	// Journal, refunds, invoices, reports, EOD, shifts, audit (CSV exports are
	// downloads of the visitor's own sample data).
	"/audit":                   true,
	"/backoffice":              true,
	"/journal":                 true,
	"/journal/{receipt}":       true,
	"/reports":                 true,
	"/shifts":                  true,
	"/ui/journal":              true,
	"/ui/reports/tab/{name}":   true,
	"GET /api/audit/export":    true,
	"GET /api/invoices/export": true,
	"GET /api/reports/worker-allocations/export":            true,
	"GET /invoice/{display_no}":                             true,
	"GET /invoices":                                         true,
	"GET /refund/{receipt}":                                 true,
	"POST /api/invoices/issue":                              true,
	"POST /api/refund":                                      true,
	"POST /api/refund/preview":                              true,
	"POST /api/reports/archive/export":                      true,
	"POST /api/reports/eod/range":                           true,
	"POST /api/reports/eod/run":                             true,
	"POST /api/reports/worker-allocations":                  true,
	"POST /api/reports/worker-allocations/pool-collections": true,
	"POST /api/settings/eod":                                true,
	"POST /api/settings/invoice":                            true,
	"POST /api/settings/report-retention":                   true,
	"POST /api/shifts/adjustment":                           true,
	"POST /api/shifts/close":                                true,
	"POST /api/shifts/open":                                 true,
	"POST /api/shifts/pfandrueckgabe":                       true,
	// Admin: users and permissions, locations, registers, tables, promotions,
	// country settings, translations, receipt designer (preview/save), menu
	// layout, data clean-up on the visitor's own instance.
	"/menu":                                      true,
	"GET /admin":                                 true,
	"GET /api/data/customers":                    true,
	"GET /api/data/obsolete-items":               true,
	"GET /api/data/reset-archives":               true,
	"GET /api/entitlement":                       true,
	"GET /country-settings":                      true,
	"GET /locations":                             true,
	"GET /promotions":                            true,
	"GET /receipt-designer":                      true,
	"GET /registers":                             true,
	"GET /settings/menu":                         true,
	"GET /tables":                                true,
	"GET /translations":                          true,
	"GET /ui/tables/state":                       true,
	"GET /ui/translations-table":                 true,
	"GET /users":                                 true,
	"GET /users/permissions":                     true,
	"POST /api/country-settings":                 true,
	"POST /api/country-settings/{code}/delete":   true,
	"POST /api/data/cleanup-catalog":             true,
	"POST /api/data/customers/erase":             true,
	"POST /api/data/reset-archives/{id}/purge":   true,
	"POST /api/data/reset-archives/{id}/restore": true,
	"POST /api/data/reset-transactions":          true,
	"POST /api/locations":                        true,
	"POST /api/locations/{id}":                   true,
	"POST /api/locations/{id}/active":            true,
	"POST /api/promotions":                       true,
	"POST /api/promotions/{code}/active":         true,
	"POST /api/promotions/{code}/edit":           true,
	"POST /api/receipt-designer/preview":         true,
	"POST /api/receipt-designer/save":            true,
	"POST /api/registers":                        true,
	"POST /api/registers/{id}":                   true,
	"POST /api/registers/{id}/active":            true,
	"POST /api/settings/menu/rehide":             true,
	"POST /api/settings/menu/restore":            true,
	"POST /api/tables":                           true,
	"POST /api/tables/{id}":                      true,
	"POST /api/tables/{id}/active":               true,
	"POST /api/tables/{id}/position":             true,
	"POST /api/tables/{id}/release":              true,
	"POST /api/translations/clear":               true,
	"POST /api/translations/set":                 true,
	"POST /api/users":                            true,
	"POST /api/users/permissions":                true,
	"POST /api/users/{id}/active":                true,
	"POST /api/users/{id}/pin":                   true,
	"POST /api/users/{id}/promote-super-admin":   true,
	"POST /api/users/{id}/role":                  true,
	// Settings that take no URL or host.
	"/api/settings/theme": true,
	"/settings":           true,
	"POST /api/settings/allow-negative-inventory":       true,
	"POST /api/settings/barcode-symbology":              true,
	"POST /api/settings/basket-panel-width":             true,
	"POST /api/settings/browsing-mode":                  true,
	"POST /api/settings/catalog-import-barcode-default": true,
	"POST /api/settings/demo-item/{id}/keep":            true,
	"POST /api/settings/demo-item/{id}/remove":          true,
	"POST /api/settings/demo-promo/{code}/keep":         true,
	"POST /api/settings/demo-promo/{code}/remove":       true,
	"POST /api/settings/dismiss-pending-base-plugin":    true,
	"POST /api/settings/dismiss-restore-prompt":         true,
	"POST /api/settings/dismiss-tse-provisioning":       true,
	"POST /api/settings/idle-lock":                      true,
	"POST /api/settings/order-no-scheme":                true,
	"POST /api/settings/order-type-prompt":              true,
	"POST /api/settings/osk":                            true,
	"POST /api/settings/payments-default":               true,
	"POST /api/settings/payments-fee":                   true,
	"POST /api/settings/remove-demo-catalogue":          true,
	"POST /api/settings/save":                           true,
	"POST /api/settings/till-name":                      true,
	"POST /api/settings/till-register":                  true,
	"POST /api/settings/ui-scale":                       true,
	// Help.
	"/help":                         true,
	"GET /help/img/{locale}/{file}": true,
	"GET /help/search":              true,
	"GET /help/{topic}":             true,
}
