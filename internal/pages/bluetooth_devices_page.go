package pages

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/bluetooth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Bluetooth devices panel (universaltill/ut-docs#76): a manager pairs,
// trusts and connects a Bluetooth HID device (keyboard-emulating barcode
// scanner, scale) from inside the kiosk UI — no OS settings, no SSH.
// Modelled on kitchen_stations_page.go's discover-printers slice: bounded,
// per-click discovery that only OFFERS candidates, the operator confirms
// each action, manager-gated throughout, raw driver errors logged server-
// side and never shown. BlueZ is the source of truth for what is paired, so
// there is no table behind this page (Architect, ut-docs#76).

// discoverBluetoothTimeout bounds the inquiry the "Scan for devices" button
// runs per click — never ambient/background. Longer than the LAN scans'
// 4s: a Bluetooth inquiry realistically needs ~10s to surface a nearby
// device that is advertising at a slow interval.
const discoverBluetoothTimeout = 10 * time.Second

// pairBluetoothTimeout bounds one Pair+Trust+Connect. BlueZ's own pairing
// timeout is in the same range; past this the device is not in pairing
// mode and the manager needs to retry, not wait.
const pairBluetoothTimeout = 45 * time.Second

// forgetBluetoothTimeout bounds one RemoveDevice (a local operation that
// only needs to disconnect first).
const forgetBluetoothTimeout = 15 * time.Second

// listBluetoothTimeout bounds a ListDevices call (a single ObjectManager
// read, no discovery involved) — same reasoning as pair/forget's own
// bounds: an unbounded r.Context() lets a wedged bluetoothd park the
// handler goroutine indefinitely (ut-docs#1582 independent-review finding).
const listBluetoothTimeout = 5 * time.Second

// scanCallTimeout bounds the ctx handed to Scan itself, which is separate
// from discoverBluetoothTimeout (the deliberate discovery WAIT duration
// Scan already receives as its timeout argument). Scan's own D-Bus calls
// around that wait — the initial ObjectManager read, SetProperty/
// StartDiscovery, and the final re-read — run on this ctx too and are
// exactly what ut-docs#1582 found unbounded. This must stay comfortably
// longer than discoverBluetoothTimeout: a ctx deadline equal to (or
// shorter than) it would make Scan's own internal wait-then-stop sequence
// race the ctx and return ctx.Err() instead of the candidates it just
// found, on every ordinary successful scan.
const scanCallTimeout = discoverBluetoothTimeout + 15*time.Second

// newBluetoothClient is a package var over bluetooth.NewDBusClient, same
// seam-for-testability pattern as discoveryBrowsePrinters — handler tests
// substitute a fake; there is no Bluetooth adapter (or system bus) in CI.
var newBluetoothClient = bluetooth.NewDBusClient

// registerBluetoothDevices wires the page and its four JSON endpoints.
func registerBluetoothDevices(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)

	// Same "settings" action and canPerform shape as kitchen_stations_page.go
	// (ut-docs#901/#902): UT_AUTH=off bypasses it for dev/CI, a real till
	// requires a manager session. The API routes are under /api/, never
	// under /self-order — an anonymous kiosk client must not be able to
	// pair hardware to the till.
	requireManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "settings") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}
	// requirePageManager is the same gate answered as a full RenderError
	// page for the /bluetooth-devices page route itself (ut-docs#1455 —
	// a bare 403 body on a pinned kiosk has no rail and no way back), same
	// split tables_page.go carries. requireManager stays for the JSON
	// routes, whose fetch() callers expect a short body.
	requirePageManager := func(w http.ResponseWriter, r *http.Request) bool {
		if !canPerform(d, r, "settings") {
			httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
			return false
		}
		return true
	}

	audit := func(r *http.Request, actorID, address, action string) {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "bluetooth_device", address, action, nil, now, "")
	}

	// apiError writes the error envelope with a machine-readable code the
	// page's own script maps to a translated string — never the raw D-Bus
	// error text (ut-docs#303/#538's standing rule), which goes to the
	// server log only via logBluetoothError. No separate "message" field:
	// the JS only ever reads "code" (messageFor(j.error.code, ...)), so a
	// duplicate "message": code was dead weight on every response
	// (ut-docs#1582 independent-review finding).
	apiError := func(w http.ResponseWriter, status int, code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  nil,
			"error": map[string]string{"code": code},
		})
	}
	apiOK := func(w http.ResponseWriter, data any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "error": nil})
	}
	// apiFail classifies a bluetooth error onto a status + code.
	apiFail := func(w http.ResponseWriter, what string, err error) {
		log.Printf("[bluetooth] %s failed: %v", what, err)
		switch {
		case errors.Is(err, bluetooth.ErrUnavailable):
			apiError(w, http.StatusServiceUnavailable, "bluetooth_unavailable")
		case errors.Is(err, bluetooth.ErrAccessDenied):
			apiError(w, http.StatusServiceUnavailable, "bluetooth_access_denied")
		case errors.Is(err, bluetooth.ErrNotFound):
			apiError(w, http.StatusNotFound, "bluetooth_not_found")
		case errors.Is(err, bluetooth.ErrPairingFailed):
			apiError(w, http.StatusConflict, "bluetooth_pairing_failed")
		default:
			apiError(w, http.StatusInternalServerError, "bluetooth_error")
		}
	}

	// openClient connects for the duration of one request. One connection
	// per request, closed on the way out: BlueZ ties discovery sessions
	// and agent registrations to the connection, so nothing can be left
	// running after a request ends (see bluetooth.NewDBusClient).
	openClient := func(w http.ResponseWriter, what string) (bluetooth.Client, bool) {
		c, err := newBluetoothClient()
		if err != nil {
			apiFail(w, what, err)
			return nil, false
		}
		return c, true
	}

	// readAddress accepts {"address": "..."} (JSON) — the only shape the
	// page's own fetch() ever sends — and returns the canonical form, the
	// only shape ever handed to a D-Bus call. There is deliberately no
	// form-encoded fallback: it was unreachable from the product and only
	// widened what these mutating endpoints accept (a form POST is a
	// CORS-"simple" request, unlike the JSON one this page actually sends —
	// ut-docs#1582 independent-review finding).
	readAddress := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		var body struct {
			Address string `json:"address"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			return "", false
		}
		return bluetooth.NormalizeAddress(body.Address)
	}

	mux.HandleFunc("GET /bluetooth-devices", func(w http.ResponseWriter, r *http.Request) {
		if !requirePageManager(w, r) {
			return
		}
		var (
			devices      []bluetooth.Device
			unavailable  bool
			accessDenied bool
			errKey       string
		)
		c, err := newBluetoothClient()
		if err == nil {
			defer c.Close()
			ctx, cancel := context.WithTimeout(r.Context(), listBluetoothTimeout)
			defer cancel()
			devices, err = c.ListDevices(ctx)
		}
		switch {
		case err == nil:
		case errors.Is(err, bluetooth.ErrUnavailable):
			// No Bluetooth on this box (most non-Pi targets): a status
			// notice, never an error page — ADR-0078 consequences.
			unavailable = true
		case errors.Is(err, bluetooth.ErrAccessDenied):
			log.Printf("[bluetooth] list failed: %v", err)
			accessDenied = true
		default:
			log.Printf("[bluetooth] list failed: %v", err)
			errKey = "bluetoothdevices.list_error"
		}
		if devices == nil {
			devices = []bluetooth.Device{}
		}
		httpx.Render("ui/pages/bluetooth_devices.html", map[string]any{
			"title":        "Bluetooth devices",
			"theme":        d.CurrentState().Theme,
			"menuItems":    d.MenuSnapshot(),
			"devices":      devices,
			"unavailable":  unavailable,
			"accessDenied": accessDenied,
			"errKey":       errKey,
		})(w, r)
	})

	mux.HandleFunc("GET /api/bluetooth-devices", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireManager(w, r); !ok {
			return
		}
		c, ok := openClient(w, "list")
		if !ok {
			return
		}
		defer c.Close()
		ctx, cancel := context.WithTimeout(r.Context(), listBluetoothTimeout)
		defer cancel()
		devices, err := c.ListDevices(ctx)
		if err != nil {
			apiFail(w, "list", err)
			return
		}
		if devices == nil {
			devices = []bluetooth.Device{}
		}
		apiOK(w, map[string]any{"devices": devices})
	})

	// Scan (bounded, per click): candidates are OFFERED only — nothing is
	// paired until the manager clicks Pair on one (security-first, same
	// rule as discover-printers). POST, not GET: unlike list, a scan can
	// power on the adapter (Adapter1.Powered=true) and always starts a
	// discovery session — real adapter-state mutation, reachable by
	// prefetch/link on a GET since the session cookie is SameSite=Lax
	// (ut-docs#1582 independent-review finding; mirrors the identical shape
	// already fixed in kitchen_stations_page.go's discover-printers route).
	mux.HandleFunc("POST /api/bluetooth-devices/scan", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireManager(w, r); !ok {
			return
		}
		c, ok := openClient(w, "scan")
		if !ok {
			return
		}
		defer c.Close()
		ctx, cancel := context.WithTimeout(r.Context(), scanCallTimeout)
		defer cancel()
		candidates, err := c.Scan(ctx, discoverBluetoothTimeout)
		if err != nil {
			apiFail(w, "scan", err)
			return
		}
		if candidates == nil {
			candidates = []bluetooth.Device{}
		}
		apiOK(w, map[string]any{"devices": candidates})
	})

	mux.HandleFunc("POST /api/bluetooth-devices/pair", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		address, ok := readAddress(w, r)
		if !ok {
			apiError(w, http.StatusBadRequest, "bluetooth_bad_address")
			return
		}
		c, ok := openClient(w, "pair")
		if !ok {
			return
		}
		defer c.Close()
		ctx, cancel := context.WithTimeout(r.Context(), pairBluetoothTimeout)
		defer cancel()
		if err := c.Pair(ctx, address); err != nil {
			apiFail(w, "pair "+address, err)
			return
		}
		audit(r, actor.ID, address, "bluetooth_device_pair")
		apiOK(w, map[string]any{"address": address})
	})

	mux.HandleFunc("POST /api/bluetooth-devices/forget", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		address, ok := readAddress(w, r)
		if !ok {
			apiError(w, http.StatusBadRequest, "bluetooth_bad_address")
			return
		}
		c, ok := openClient(w, "forget")
		if !ok {
			return
		}
		defer c.Close()
		ctx, cancel := context.WithTimeout(r.Context(), forgetBluetoothTimeout)
		defer cancel()
		if err := c.Forget(ctx, address); err != nil {
			apiFail(w, "forget "+address, err)
			return
		}
		audit(r, actor.ID, address, "bluetooth_device_forget")
		apiOK(w, map[string]any{"address": address})
	})
}
