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

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/procrestart"
)

// Seams over internal/procrestart so the handler/template tests can observe
// "a restart was scheduled" and render both platform branches without a
// real syscall.Exec replacing the test binary — same hermetic convention as
// update_api.go's autoUpdateApply/autoUpdateSupported over selfupdate.
var (
	pairingRestartFn        = procrestart.Restart
	pairingRestartSupported = procrestart.Supported
	// pairingRestorePending is a seam over db.PendingRestore so a test can
	// force either branch without staging (or not staging) a real
	// restore-pending file on disk.
	pairingRestorePending = db.PendingRestore
)

// pairingRestartURLFor maps a pair-status route to its sibling restart
// route, keeping the manager-vs-first-boot split (ut-docs#289) intact:
// /api/sync/pair-status → /api/sync/pairing-restart (manager-gated) and
// /api/setup/pair-status → /api/setup/pairing-restart (middleware-exempt).
// Derived from statusURL rather than threaded as one more parameter through
// every pairWaitView call site, since the two are only ever meaningful as a
// pair — a first-boot wizard rendered with the manager-gated restart route
// would 401 on the very click this card exists to fix.
func pairingRestartURLFor(statusURL string) string {
	return strings.TrimSuffix(statusURL, "pair-status") + "pairing-restart"
}

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
	pairWaitViewPolling(w, r, statusURL, status, code, shopName, errMsg, status == "waiting")
}

// pairWaitViewPolling is pairWaitView with the self-poll decided by the
// CALLER rather than inferred from status (ut-docs#1540 review).
//
// Whether an "error" render may keep polling depends entirely on whether
// there is an active attempt behind it, which only the caller knows:
//
//   - pairStartHandler's error branches return BEFORE rp.set(), so there is
//     no active attempt. A poll from there renders the "idle" view — it
//     would silently replace the just-rendered, field-specific message
//     ("This till's name is required…", "cannot reach that primary: …")
//     with "No pairing attempt in progress." 15 seconds later, or, if an
//     EARLIER attempt is still in flight, resurrect that older attempt's
//     "waiting" screen and its stale verification code. Those renders stay
//     terminal, and the retry re-renders this same div anyway: the
//     "Request to pair" button posts with hx-target on the status host and
//     hx-swap="innerHTML".
//   - pairStatusHandler's error branches DO have active state, so the poll
//     re-renders the same stored errMsg (nothing is lost) and can pick up a
//     new "waiting"/"joined" state started from another tab or device
//     against this till's single replicaPairing.
func pairWaitViewPolling(w http.ResponseWriter, r *http.Request, statusURL, status, code, shopName, errMsg string, polling bool) {
	httpx.RenderPartial("ui/partials/pairing_wait.html", map[string]any{
		"statusURL": statusURL,
		"status":    status,
		"code":      code,
		"shopName":  shopName,
		"errMsg":    errMsg,
		"polling":   polling,
		// ut-docs#1550: the "joined" branch either restarts the till itself
		// (restartURL, where an in-place re-exec is possible) or tells the
		// operator to close and reopen the app (Windows — ut-docs#1614).
		"restartSupported": pairingRestartSupported(),
		"restartURL":       pairingRestartURLFor(statusURL),
		// autoRestart (review finding, ut-docs#1550): only the first-boot
		// wizard fires the restart automatically on render — a Pi kiosk has
		// no shell to press a button from. The manager-driven /tills flow
		// restarts an already-configured, possibly-in-use till, so it stays
		// an explicit click; statusURL is the one thing that already tells
		// the two flavours apart (see pairingRestartURLFor).
		"autoRestart": statusURL == "/api/setup/pair-status",
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

	mux.HandleFunc("POST /api/sync/pair-start", pairStartHandler(d, rp, client, managerGate(d), "/api/sync/pair-status"))
	mux.HandleFunc("GET /api/sync/pair-status", pairStatusHandler(d, rp, client, managerGate(d), "/api/sync/pair-status"))
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
	// ut-docs#1550: the "joined" screen's restart trigger, in the same two
	// flavours. The setup flavour MUST be listed in internal/auth/
	// middleware.go's exempt paths (next to /api/setup/pair-status) or the
	// wizard's auto-restart only ever collects 401s — exactly the failure
	// mode the pair-status comment above warns about. UNLIKE pair-status,
	// this one DOES need the same rate limit as pair-start above (review
	// finding, ut-docs#1550): pairingRestartHandler itself also refuses
	// unless a restore is actually staged, but that alone doesn't stop an
	// anonymous LAN caller from holding a first-boot till in a restart loop
	// once a real join IS staged and in progress — the limiter bounds that
	// window the same way it already bounds pair-start's SSRF-oracle risk.
	setupPairingRestartLimiter := newPairRateLimiter(time.Minute, 5)
	mux.HandleFunc("POST /api/sync/pairing-restart", pairingRestartHandler(d, managerGate(d)))
	mux.HandleFunc("POST /api/setup/pairing-restart", pairingRestartHandler(d, rateLimited(setupPairingRestartLimiter, firstBootGate(d))))
}

// pairingRestartHandler schedules an in-place restart of this till so a
// join staged by completeJoin (a restore-pending.db that only
// db.ApplyPendingRestore, run once before db.Open at startup, can apply)
// actually takes effect — the previous "restart this till to finish" text
// with no button was a real dead end on a kiosk with no shell
// (ut-docs#1550). procrestart.Restart only schedules a goroutine (the
// re-exec fires ~1.5s later), so this response flushes long before the
// process image is replaced; the page then polls /healthz until the new
// image is up. Answers the standard { "data": …, "error": null } envelope.
//
// Refuses with 409 unless a restore is actually staged (review finding:
// the first-boot route is otherwise an unauthenticated, unconditional
// process-kill any anonymous device on the shop LAN could fire in a loop,
// with nothing to gain — completeJoin stages the restore, via
// db.StageRestoreFromReader, strictly BEFORE the state flips to "joined",
// so this is true on every legitimate call and false on every abusive
// one). Deliberately NOT gated on pairingRestartSupported() beyond that:
// the template never renders the trigger where it's false, and on such a
// platform Restart itself is a logged no-op, never a crash.
func pairingRestartHandler(d *common.Deps, gate apiGate) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if !pairingRestorePending(d.Cfg.DBPath) {
			locale := httpx.ResolveLocale(w, r)
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": httpx.T(locale, "tills.pairing.nothing_to_restart")})
			return
		}
		pairingRestartFn()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]bool{"restarting": true}, "error": nil})
	}
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
		// ut-docs#1544: resolved once and reused by every error branch below
		// (was previously re-resolved only inside the missing-name branch) —
		// every one of these branches renders a translated message now, not
		// just that one.
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		baseURL := strings.TrimSuffix(strings.TrimSpace(r.Form.Get("base_url")), "/")
		primaryTillID := strings.TrimSpace(r.Form.Get("till_id"))
		name := strings.TrimSpace(r.Form.Get("name"))
		if baseURL == "" || primaryTillID == "" || name == "" {
			// ut-docs#1540: name the field the operator actually sees on
			// screen ("This till's name"), never the raw JSON keys this
			// handler reads its form values into — base_url/till_id are
			// supplied by this page's own JS (from the discovery result),
			// never typed by a person, so a real gap here is always the
			// till-name box (now client-validated above this handler too,
			// so this branch is defense-in-depth, not the normal path).
			msg := httpx.T(locale, "tills.pairing.error.missing_request")
			if name == "" {
				msg = httpx.T(locale, "tills.pairing.error.name_required")
			}
			pairWaitView(w, r, statusURL, "error", "", "", msg)
			return
		}
		if !validPrimaryBaseURL(baseURL) {
			pairWaitView(w, r, statusURL, "error", "", "", httpx.T(locale, "tills.pairing.error.invalid_address"))
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
			// ut-docs#1611: same defect shape ut-docs#1544 fixed for this
			// handler's other branches — a raw Go error string reaching the
			// operator, invisible to guard-i18n.sh's template-only checks.
			// Realistically very hard to reach: baseURL is already validated
			// scheme+host by validPrimaryBaseURL above (empirically, every
			// input that makes url.Parse fail on the concatenated request
			// URL below already fails validPrimaryBaseURL's own url.Parse
			// first), and method/reader are compile-time-safe — this is
			// defense-in-depth, not a reachable path today, same as the
			// missing-name branch above.
			pairWaitView(w, r, statusURL, "error", "", "", httpx.T(locale, "tills.pairing.error.request_build_failed"))
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			pairWaitView(w, r, statusURL, "error", "", "", fmt.Sprintf(httpx.T(locale, "tills.pairing.error.unreachable"), err.Error()))
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			pairWaitView(w, r, statusURL, "error", "", "", httpx.T(locale, "tills.pairing.error.refused"))
			return
		}
		var out struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if json.NewDecoder(resp.Body).Decode(&out) != nil || out.Data.ID == "" {
			pairWaitView(w, r, statusURL, "error", "", "", httpx.T(locale, "tills.pairing.error.unexpected_response"))
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
		locale := httpx.ResolveLocale(w, r)
		rp.mu.Lock()
		defer rp.mu.Unlock()

		state := rp.active
		if state == nil {
			pairWaitView(w, r, statusURL, "idle", "", "", "")
			return
		}
		if state.status != "waiting" {
			// Terminal already — re-render idempotently (a stray extra poll
			// racing the swap that stopped it, or a page reload). An "error"
			// here keeps polling (ut-docs#1540): the stored errMsg is
			// re-rendered every tick, so nothing is lost, and this is the one
			// path by which a fresh attempt started from another tab/device
			// against this till's single replicaPairing reaches a screen still
			// sitting on the old failure. "joined"/"expired" stay terminal.
			pairWaitViewPolling(w, r, statusURL, state.status, "", state.shopName, state.errMsg, state.status == "error")
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
			next.status, next.errMsg = "error", fmt.Sprintf(httpx.T(locale, "tills.pairing.error.unexpected_response_status"), resp.Status)
			rp.active = &next
			pairWaitViewPolling(w, r, statusURL, "error", "", "", next.errMsg, true)
			return
		}
		var out struct {
			Data struct {
				Token string `json:"token"`
			} `json:"data"`
		}
		if json.NewDecoder(resp.Body).Decode(&out) != nil || out.Data.Token == "" {
			next := *state
			next.status, next.errMsg = "error", httpx.T(locale, "tills.pairing.error.unexpected_response")
			rp.active = &next
			pairWaitViewPolling(w, r, statusURL, "error", "", "", next.errMsg, true)
			return
		}

		shopName, err := completeJoin(r, d, state.primaryURL, out.Data.Token, state.deviceName)
		if err != nil {
			next := *state
			// friendlyJoinError, not err.Error(): completeJoin's failures are
			// now a *joinError (ut-docs#36) whose raw Error() is a locale
			// key, not English prose — this must go through httpx.T.
			next.status, next.errMsg = "error", friendlyJoinError(locale, err)
			rp.active = &next
			pairWaitViewPolling(w, r, statusURL, "error", "", "", next.errMsg, true)
			return
		}
		next := *state
		next.status, next.shopName = "joined", shopName
		rp.active = &next
		pairWaitView(w, r, statusURL, "joined", "", shopName, "")
	}
}
