package pages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerPendingPairingsUI wires GET /ui/tills/pending-pairings — the
// manager-facing card list backing the approve/deny UI (ADR-0033 part 3/3,
// ut-docs#185) — and GET /ui/pairing-notice, the nav-level dismissible
// notice for the same queue (ut-docs#1551). Same direct-repo-access style
// as GET /ui/sync-chip (sync_admin.go): computed straight from
// PairingRepo + discovery.TillID, not by calling this package's own JSON
// API internally. The Tills page polls the first via
// hx-trigger="every 30s" (ADR-0033 §4's cadence); base.html polls the
// second the same way on every page.
func registerPendingPairingsUI(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /api/pairings/stream", func(w http.ResponseWriter, r *http.Request) {
		streamPairings(w, r, d)
	})
	mux.HandleFunc("GET /ui/tills/pending-pairings", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		repo := data.NewPairingRepo(d.Db)
		list, err := repo.ListPending(r.Context())
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "pairings", err)
			return
		}
		primaryTillID, err := discovery.TillID(r.Context(), data.NewSettingsRepo(d.Db))
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "pairings", err)
			return
		}
		type row struct {
			ID               string
			DeviceName       string
			VerificationCode string
			RequestedRole    string // ut-docs#2781: preselects the card's role select
		}
		rows := make([]row, 0, len(list))
		for _, p := range list {
			rows = append(rows, row{
				ID:               p.ID,
				DeviceName:       p.DeviceName,
				VerificationCode: derivedVerificationCode(p.Commitment, primaryTillID, p.ServedPin),
				RequestedRole:    p.RequestedRole,
			})
		}
		httpx.RenderPartial("ui/partials/pending_pairings.html", map[string]any{"Pending": rows})(w, r)
	})

	// GET /ui/pairing-notice (ut-docs#1551): a manager anywhere in the app
	// — not just on /tills — sees a pending pairing request within one
	// polling interval, mirroring the sync/bugreport/session/fiscal chips'
	// own "nav.html has no per-request data, load a fragment after render"
	// convention (base.html mounts the placeholder). Pairing approval only
	// ever happens on the primary (a replica has no PairingRepo rows of its
	// own to approve), so this skips the same way GET /ui/sync-chip's
	// replica branch does (sync_admin.go) rather than relying on
	// canPerform alone — a cashier or manager working on a REPLICA till
	// must never see a notice about a request only the primary can act on.
	mux.HandleFunc("GET /ui/pairing-notice", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") || d.SyncPrimaryURL(r.Context()) != "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		// Read-only variant (no opportunistic expired-row DELETE): this
		// fragment is polled every 30s from every open page by every
		// manager, unlike the approve/deny queue this repo method was
		// originally written for -- see ListPendingReadOnly's own comment
		// (independent review, ut-docs#1551).
		repo := data.NewPairingRepo(d.Db)
		list, err := repo.ListPendingReadOnly(r.Context())
		if err != nil {
			// Same "silent empty on error" convention as the sibling nav
			// fragments (GET /ui/sync-chip, GET /ui/bugreport-chip) rather
			// than a 500: htmx doesn't swap a non-2xx response anyway (the
			// placeholder just keeps its last good content), so the only
			// visible effect of a 500 here is logging an error every 30s
			// per open page for as long as the underlying read stays
			// broken (independent review).
			logging.L().Errorf("list pending pairings (read-only): %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		// ut-docs#2743: an authorised primary caller always gets the partial
		// (also with 0 pending) so it can emit the stream marker that tells
		// pairing-stream.js to open the EventSource; the notice itself is
		// rendered only when Count > 0.
		// Fingerprint is the sorted-by-request-time-then-id (stable across
		// polls even for two requests landing in the same second --
		// independent review) set of pending IDs -- the client-side
		// dismiss script (pairing_notice.html) compares this against what
		// was last dismissed, so a resolved request (approved/denied/
		// expired, which ListPendingReadOnly already excludes) or a
		// genuinely NEW one changes the fingerprint and the notice
		// reappears, satisfying "must clear itself... never a stale
		// notice" with no extra state kept here.
		ids := make([]string, 0, len(list))
		for _, p := range list {
			ids = append(ids, p.ID)
		}
		httpx.RenderPartial("ui/partials/pairing_notice.html", map[string]any{
			"Count":       len(list),
			"Fingerprint": strings.Join(ids, ","),
		})(w, r)
	})
}

const (
	// pairingStreamCap bounds concurrent pairing streams (ut-docs#2743 AC8);
	// beyond it the route answers 503 and the page stays on its 30s polls.
	pairingStreamCap = 16
)

// pairingStreamHeartbeat is a var so tests can shorten it.
var pairingStreamHeartbeat = 20 * time.Second

// streamPairings serves GET /api/pairings/stream (ut-docs#2743, ADR-0114 §2
// -> ADR-0079 §1): one payload-free `event: pairing` per pending-pairing
// change, plus a `: ping` heartbeat. Session-authed by the middleware and
// gated by the same sync_management permission as the fragments it nudges;
// a replica gets 204 (EventSource stops), over the cap 503. No body, so no
// user-facing text. Every refusal happens before any subscription exists.
func streamPairings(w http.ResponseWriter, r *http.Request, d *common.Deps) {
	if !canPerform(d, r, "sync_management") {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if d.SyncPrimaryURL(r.Context()) != "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "pairings",
			errors.New("pairing stream: response writer cannot flush"))
		return
	}
	if d.PairingPush == nil {
		common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "pairings",
			errors.New("pairing stream: no broadcaster wired"))
		return
	}
	// Subscribe BEFORE the headers go out: a client that has seen 200 must
	// not miss a change published in the gap.
	ch, cancel, ok := d.PairingPush.Subscribe()
	if !ok {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer cancel()
	sessionLive := pairingStreamSessionCheck(d, r)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	heartbeat := time.NewTicker(pairingStreamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case _, ok := <-ch:
			if !ok {
				return // broadcaster closed -- process shutting down
			}
			if _, err := fmt.Fprint(w, "event: pairing\ndata: {}\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			// The middleware checked the session only at connect: end the
			// stream once it is idle-locked, signed out or revoked, so the
			// browser's reconnect meets the middleware's refusal (AC7).
			if !sessionLive(r.Context()) {
				return
			}
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// pairingStreamSessionCheck reports whether the session that opened the
// stream is still live and still the same operator (resolved without a
// touch, like the background polls — ut-docs#2743 review). No session
// cookie (UT_AUTH=off) means nothing to re-check.
func pairingStreamSessionCheck(d *common.Deps, r *http.Request) func(context.Context) bool {
	c, err := r.Cookie(auth.CookieName)
	if d.AuthSvc == nil || err != nil || c.Value == "" {
		return func(context.Context) bool { return true }
	}
	opened, _ := auth.FromContext(r.Context())
	check := d.AuthSvc.SessionCheck(c.Value)
	return func(ctx context.Context) bool {
		u, ok := check(ctx)
		return ok && u.ID == opened.ID
	}
}

// schedulePendingPairingExpiryNudges re-arms an expiry nudge for every
// request already pending at boot (the per-request timers die with the
// process). Primary only; log-and-continue, never fails boot.
func schedulePendingPairingExpiryNudges(ctx context.Context, d *common.Deps) {
	if d.PairingPush == nil || d.SyncPrimaryURL(ctx) != "" {
		return
	}
	list, err := data.NewPairingRepo(d.Db).ListPendingReadOnly(ctx)
	if err != nil {
		logging.L().Errorf("pairing expiry nudges: %v", err)
		return
	}
	for _, p := range list {
		exp, err := time.Parse(time.RFC3339, p.ExpiresAt)
		if err != nil {
			continue
		}
		schedulePairingExpiryNudge(d, time.Until(exp))
	}
}
