package pages

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Approve-to-pair, replica side (ADR-0033 part 3/3, ut-docs#185): once a
// primary is found via discovery.Browse (#183) and selected on the Tills
// page, this drives the whole outbound flow — generate a request_secret
// LOCALLY (never transmitted in the clear, only its sha256 commitment),
// send the pair-request, show the manager the SAME verification code the
// primary's approval card shows (independently derived here via
// derivedVerificationCode, never echoed by the primary), poll for a token,
// then complete enrolment via the existing snapshot/stage machinery
// (completeJoin, sync_api.go).
//
// A till only ever has one outbound pairing attempt in flight at a time —
// this is a page a manager is actively watching, not a background/ambient
// process — so a single mutex-guarded struct is enough, no map keyed by
// request id. Same ephemeral, lost-on-restart tradeoff as enrolTokens.

// pairJoinHTTPTimeout bounds both outbound calls this flow makes (the
// initial pair-request and each pair-status poll) — short, since these are
// interactive, user-is-watching calls, not the 60s snapshot download.
const pairJoinHTTPTimeout = 10 * time.Second

// pairingJoinNow is a seam over time.Now so tests can simulate the 10-minute
// TTL elapsing without sleeping (same style as discoveryBrowse/mdnsQuery
// elsewhere in this codebase).
var pairingJoinNow = time.Now

type pendingJoinState struct {
	id            string
	primaryURL    string
	primaryTillID string
	deviceName    string
	requestSecret string
	commitment    string
	requestedAt   time.Time
	status        string // "waiting" | "joined" | "expired" | "error"
	shopName      string
	errMsg        string
}

// replicaPairing holds the single active outbound pairing attempt, if any.
// GET /api/sync/pair-status holds mu for its ENTIRE handler body, including
// the outbound call to the primary and the eventual completeJoin — a
// deliberate choice, not an oversight: two browser tabs on the same Tills
// page polling concurrently must not both observe "waiting", both retrieve
// the (idempotently-readable, non-one-time) token, and both race
// completeJoin against the primary's one-time enrolment token, where one
// tab would land in a confusing "error: primary refused the enrolment"
// state even though the other tab's join actually succeeded. Since there
// is only ever one outbound attempt per till, serializing the rare
// network round-trip behind this lock costs nothing in practice.
type replicaPairing struct {
	mu     sync.Mutex
	active *pendingJoinState
}

func (p *replicaPairing) set(s *pendingJoinState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active = s
}

// pairWaitView renders the waiting/terminal partial for a given state. Only
// the "waiting" status carries the self-polling htmx attributes (see
// pairing_wait.html's "polling" flag) — every other status is a terminal
// render with no further hx-trigger, so the browser naturally stops
// polling once one of them is swapped in. statusURL is the pair-status
// route the waiting fragment polls: the manager flavour must poll the
// manager-gated route and the first-boot flavour the middleware-exempt
// setup route — a first-boot till polling /api/sync/pair-status would only
// ever collect 401s and hang on "waiting" forever.
func pairWaitView(w http.ResponseWriter, r *http.Request, statusURL, status, code, shopName, errMsg string) {
	httpx.RenderPartial("ui/partials/pairing_wait.html", map[string]any{
		"statusURL": statusURL,
		"status":    status,
		"code":      code,
		"shopName":  shopName,
		"errMsg":    errMsg,
		"polling":   status == "waiting",
	})(w, r)
}

// validPrimaryBaseURL rejects anything that isn't a plain http(s) URL with a
// host — base_url originates from an untrusted LAN mDNS responder (any host
// can advertise), so it's external input per CLAUDE.md's "validate all
// external input" rule, not just a value this codebase produced itself.
func validPrimaryBaseURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// registerPairingJoinAPI wires the outbound pairing flow in its two
// authorization flavours (ut-docs#289): manager-gated on the Tills page
// (/api/sync/pair-start|pair-status) and first-boot-gated on the setup
// wizard (/api/setup/pair-start|pair-status, middleware-exempt). All four
// routes share ONE replicaPairing: a till only ever has one outbound
// attempt in flight (see replicaPairing's doc comment), and first boot is
// monotonic, so in practice only one route group is reachable at a time
// anyway — two disconnected states would be the bug, not the fix.
func registerPairingJoinAPI(mux *http.ServeMux, d *common.Deps) {
	rp := &replicaPairing{}
	client := &http.Client{Timeout: pairJoinHTTPTimeout}

	mux.HandleFunc("POST /api/sync/pair-start", pairStartHandler(d, rp, client, managerGate, "/api/sync/pair-status"))
	mux.HandleFunc("GET /api/sync/pair-status", pairStatusHandler(d, rp, client, managerGate, "/api/sync/pair-status"))
	// Rate-limited (ut-docs#289 review): unlike its manager-gated sibling
	// above, this flavour needs no session at all — pair-start is an
	// unauthenticated outbound-HTTP primitive to any attacker-chosen host
	// (validPrimaryBaseURL only checks scheme+host), so an unlimited caller
	// during the first-boot window gets a free blind-SSRF port-scan oracle.
	// pair-status stays unlimited, same as its manager-gated sibling: it's
	// the legitimate 15s self-poll, not an attacker-chosen outbound call.
	setupPairStartLimiter := newPairRateLimiter(time.Minute, 5)
	mux.HandleFunc("POST /api/setup/pair-start", pairStartHandler(d, rp, client, rateLimited(setupPairStartLimiter, firstBootGate(d)), "/api/setup/pair-status"))
	mux.HandleFunc("GET /api/setup/pair-status", pairStatusHandler(d, rp, client, firstBootGate(d), "/api/setup/pair-status"))
}

// pairStartHandler sends the pair request to the chosen primary and renders
// the first "waiting" fragment. Gate rationale for the manager flavour:
// sending a pair request is already the LAN-open, unauthenticated-by-design
// action on the PRIMARY's side (ADR-0033 §8) — being on the manager-gated
// page (or, for the wizard, in the first-boot window) is enough, no extra
// manager-PIN prompt for this side.
//
// Every branch below renders a 200 with the outcome encoded in the
// body (waiting/error), never a 4xx/5xx status: this response is always
// swapped in by htmx, and htmx (the vendored 1.9.12 here) does not swap
// non-2xx responses by default — a non-200 render is invisible to the
// operator, not just cosmetically wrong.
func pairStartHandler(d *common.Deps, rp *replicaPairing, client *http.Client, gate apiGate, statusURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r) {
			return
		}
		_ = r.ParseForm()
		baseURL := strings.TrimSuffix(strings.TrimSpace(r.Form.Get("base_url")), "/")
		primaryTillID := strings.TrimSpace(r.Form.Get("till_id"))
		name := strings.TrimSpace(r.Form.Get("name"))
		if baseURL == "" || primaryTillID == "" || name == "" {
			pairWaitView(w, r, statusURL, "error", "", "", "base_url, till_id and name are all required")
			return
		}
		if !validPrimaryBaseURL(baseURL) {
			pairWaitView(w, r, statusURL, "error", "", "", "not a valid primary address")
			return
		}

		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		secret := hex.EncodeToString(raw)
		sum := sha256.Sum256([]byte(secret))
		commitment := hex.EncodeToString(sum[:])

		body, _ := json.Marshal(map[string]string{"device_name": name, "commitment": commitment})
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
			baseURL+"/api/sync/pair-request", strings.NewReader(string(body)))
		if err != nil {
			pairWaitView(w, r, statusURL, "error", "", "", err.Error())
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			pairWaitView(w, r, statusURL, "error", "", "", "cannot reach that primary: "+err.Error())
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			pairWaitView(w, r, statusURL, "error", "", "", "the primary refused the pair request")
			return
		}
		var out struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if json.NewDecoder(resp.Body).Decode(&out) != nil || out.Data.ID == "" {
			pairWaitView(w, r, statusURL, "error", "", "", "unexpected response from the primary")
			return
		}

		rp.set(&pendingJoinState{
			id:            out.Data.ID,
			primaryURL:    baseURL,
			primaryTillID: primaryTillID,
			deviceName:    name,
			requestSecret: secret,
			commitment:    commitment,
			requestedAt:   pairingJoinNow(),
			status:        "waiting",
		})
		pairWaitView(w, r, statusURL, "waiting", derivedVerificationCode(commitment, primaryTillID), "", "")
	}
}

// pairStatusHandler is polled by the waiting fragment itself
// (hx-trigger="every 15s", pointing back at this handler's own statusURL)
// until it renders a terminal state. No request id/params — reads whatever
// the single active attempt is. Holds rp.mu for its whole body — see
// replicaPairing's own doc comment for why.
func pairStatusHandler(d *common.Deps, rp *replicaPairing, client *http.Client, gate apiGate, statusURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r) {
			return
		}
		rp.mu.Lock()
		defer rp.mu.Unlock()

		state := rp.active
		if state == nil {
			pairWaitView(w, r, statusURL, "idle", "", "", "")
			return
		}
		if state.status != "waiting" {
			// Terminal already — re-render idempotently (a stray extra poll
			// racing the swap that stopped it, or a page reload).
			pairWaitView(w, r, statusURL, state.status, "", state.shopName, state.errMsg)
			return
		}
		if pairingJoinNow().Sub(state.requestedAt) > pairingRequestTTL {
			next := *state
			next.status = "expired"
			rp.active = &next
			pairWaitView(w, r, statusURL, "expired", "", "", "")
			return
		}

		url := state.primaryURL + "/api/sync/pair-requests/" + state.id + "?request_secret=" + state.requestSecret
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
		if err != nil {
			pairWaitView(w, r, statusURL, "waiting", derivedVerificationCode(state.commitment, state.primaryTillID), "", "")
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			// Transient network hiccup — stay waiting, the next tick retries.
			pairWaitView(w, r, statusURL, "waiting", derivedVerificationCode(state.commitment, state.primaryTillID), "", "")
			return
		}
		defer resp.Body.Close()

		// 404: not yet approved — or denied/expired, indistinguishable from
		// here (ADR-0033 §4's own accepted limitation: "Deny just removes
		// the row; the replica's poll then returns 'not approved'"). 429:
		// the shared pair-request rate limiter (registerPairingAPI) — an
		// expected steady-state outcome at this poll cadence, not fatal.
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusTooManyRequests {
			pairWaitView(w, r, statusURL, "waiting", derivedVerificationCode(state.commitment, state.primaryTillID), "", "")
			return
		}
		if resp.StatusCode != http.StatusOK {
			next := *state
			next.status, next.errMsg = "error", fmt.Sprintf("unexpected response from the primary (%s)", resp.Status)
			rp.active = &next
			pairWaitView(w, r, statusURL, "error", "", "", next.errMsg)
			return
		}
		var out struct {
			Data struct {
				Token string `json:"token"`
			} `json:"data"`
		}
		if json.NewDecoder(resp.Body).Decode(&out) != nil || out.Data.Token == "" {
			next := *state
			next.status, next.errMsg = "error", "unexpected response from the primary"
			rp.active = &next
			pairWaitView(w, r, statusURL, "error", "", "", next.errMsg)
			return
		}

		shopName, err := completeJoin(r, d, state.primaryURL, out.Data.Token, state.deviceName)
		if err != nil {
			next := *state
			// friendlyJoinError, not err.Error(): completeJoin's failures are
			// now a *joinError (ut-docs#36) whose raw Error() is a locale
			// key, not English prose — this must go through httpx.T.
			next.status, next.errMsg = "error", friendlyJoinError(httpx.ResolveLocale(w, r), err)
			rp.active = &next
			pairWaitView(w, r, statusURL, "error", "", "", next.errMsg)
			return
		}
		next := *state
		next.status, next.shopName = "joined", shopName
		rp.active = &next
		pairWaitView(w, r, statusURL, "joined", "", shopName, "")
	}
}
