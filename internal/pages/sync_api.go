package pages

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/lanip"
	"github.com/universaltill/universal-till/internal/lantls"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/netaccess"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// Multi-till sync, increment D1: QR enrolment (docs: adr/0011 +
// architecture/lan-sync.md). This till acts as the PRIMARY: it issues
// one-time enrolment tokens (QR on the Tills page) and hands enrolled
// replicas a per-till bearer for the /api/sync/* surface.

// enrolTokens is the in-memory one-time token store. Losing them on
// restart just means re-showing the QR.
//
// ut-docs#3219: a token minted for the Tills page's "Show pairing code" also
// gets a short twin — six characters a person can type (shortCodeAlphabet,
// shown as XXX-XXX) — so manual pairing no longer means copying the whole
// ~90-character encodeEnrollCode string by hand. The two are one credential:
// same 10-minute expiry, and using either burns both. Six symbols of 5 bits
// is only 30 bits, so short codes get two extra defences the 128-bit long
// token never needed: a per-source rate limit in the enrol handler, and the
// shop-wide failure budget kept here (shortCodeFailureBudget).
type enrolTokens struct {
	mu     sync.Mutex
	tokens map[string]time.Time // token → expiry
	// short maps a live short code (normalised, no dash) to its long twin.
	// Lazily created: callers and tests build the struct with tokens only.
	short map[string]string
	// shortFailures counts failed short-code attempts since the last code
	// was issued or the last budget purge.
	shortFailures int
}

const (
	// shortCodeAlphabet: digits and capitals minus the look-alikes 0/O and
	// 1/I. Exactly 32 symbols, so one random byte & 31 picks one unbiased.
	shortCodeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	shortCodeLen      = 6
	// shortCodeFailureBudget: after this many failed short-code attempts
	// (from any source) the live short code is invalidated, so even a
	// distributed guesser gets at most this many tries per displayed code
	// before the manager has to show a new one. Only the guessable short
	// half dies: the 128-bit long twin (QR, full code) stays usable, so a
	// stranger flooding wrong codes cannot also kill QR pairing (review
	// finding, ut-docs#3219).
	shortCodeFailureBudget = 10
	enrolTokenTTL          = 10 * time.Minute
)

// issue mints a long one-time token only. Used by the approve-to-pair flow
// (pairing_api.go), which hands the token to the replica directly and never
// displays a code, so it gets no short twin and does not reset the
// short-code failure budget.
func (e *enrolTokens) issue() string {
	tok := newLongEnrolToken()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tokens[tok] = time.Now().Add(enrolTokenTTL)
	return tok
}

// issueWithShort mints a long token plus its short twin (normalised, no
// dash — formatShortCode adds it for display). Only ONE short code is live
// per shop: issuing a new one retires earlier short codes (their long twins
// stay live until used or expired), so resetting the failure budget here
// never hands a guesser fresh tries against older codes too.
func (e *enrolTokens) issueWithShort() (long, short string) {
	long = newLongEnrolToken()
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	e.pruneLocked(now)
	e.short = map[string]string{}
	for {
		short = newShortCode()
		if _, taken := e.short[short]; !taken {
			break
		}
	}
	e.tokens[long] = now.Add(enrolTokenTTL)
	e.short[short] = long
	e.shortFailures = 0
	return long, short
}

// consume validates and burns a long token (one-time), and its short twin.
func (e *enrolTokens) consume(tok string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	exp, ok := e.tokens[tok]
	delete(e.tokens, tok)
	for s, l := range e.short {
		if l == tok {
			delete(e.short, s)
		}
	}
	return ok && time.Now().Before(exp)
}

// consumeShort validates and burns a short code (already normalised by
// normaliseShortCode) together with its long twin. A miss counts against the
// shop-wide failure budget; reaching it invalidates the live short code
// (never its long twin), then starts the count again.
func (e *enrolTokens) consumeShort(code string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if long, ok := e.short[code]; ok {
		delete(e.short, code)
		exp, live := e.tokens[long]
		delete(e.tokens, long)
		// A real code that merely expired is a person re-typing a stale
		// code, not a guess: refuse it without spending the budget.
		return live && time.Now().Before(exp)
	}
	e.shortFailures++
	if e.shortFailures >= shortCodeFailureBudget {
		e.short = map[string]string{}
		e.shortFailures = 0
	}
	return false
}

// pruneLocked drops expired tokens (and their short twins) so the maps stay
// small and "unique among live codes" is checked against live codes only.
func (e *enrolTokens) pruneLocked(now time.Time) {
	for tok, exp := range e.tokens {
		if !now.Before(exp) {
			delete(e.tokens, tok)
		}
	}
	for s, l := range e.short {
		if _, ok := e.tokens[l]; !ok {
			delete(e.short, s)
		}
	}
}

func newLongEnrolToken() string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

// newShortCode draws shortCodeLen symbols from crypto/rand. 256 is a
// multiple of len(shortCodeAlphabet) (32), so b&31 is unbiased.
func newShortCode() string {
	raw := make([]byte, shortCodeLen)
	_, _ = rand.Read(raw)
	out := make([]byte, shortCodeLen)
	for i, b := range raw {
		out[i] = shortCodeAlphabet[b&31]
	}
	return string(out)
}

// formatShortCode renders a normalised short code for display: XXX-XXX.
func formatShortCode(code string) string {
	if len(code) != shortCodeLen {
		return code
	}
	return code[:3] + "-" + code[3:]
}

// normaliseShortCode turns what a person typed ("k7p 4xq", "K7P-4XQ") into
// the stored form: uppercase, everything but ASCII letters and digits
// dropped. ok is false unless the result is exactly shortCodeLen symbols
// from shortCodeAlphabet — a long token or an encodeEnrollCode string never
// is, which is how the enrol handler tells the two apart.
func normaliseShortCode(typed string) (code string, ok bool) {
	var b strings.Builder
	for _, c := range strings.ToUpper(typed) {
		if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		}
	}
	code = b.String()
	if len(code) != shortCodeLen {
		return code, false
	}
	for _, c := range code {
		if !strings.ContainsRune(shortCodeAlphabet, c) {
			return code, false
		}
	}
	return code, true
}

func hashBearer(b string) string {
	sum := sha256.Sum256([]byte(b))
	return hex.EncodeToString(sum[:])
}

// advertisableHost turns the Host a browser happened to use into one ANOTHER
// device can dial. A till's kiosk browser is launched against
// http://127.0.0.1:8080, so a pairing code minted from the main till's own
// touchscreen — the obvious way to do it, and the way the manual describes —
// used to embed 127.0.0.1. The joining till then dialled itself, failed in
// about a millisecond, and reported "primary refused the enrolment (code used
// or expired?)", which sent the shop owner into an endless
// regenerate-and-retry loop against an address that could never work
// (ut-docs#362, found on real hardware).
//
// Returns an error rather than a loopback address: minting a code that cannot
// possibly work is worse than refusing to mint one, because the failure
// surfaces on the OTHER device, minutes later, blaming the code.
func advertisableHost(host string) (string, error) {
	hostOnly, port, err := net.SplitHostPort(host)
	if err != nil {
		hostOnly, port = host, ""
	}
	if ip := net.ParseIP(hostOnly); ip == nil || !ip.IsLoopback() {
		if !strings.EqualFold(hostOnly, "localhost") {
			return host, nil // already something a peer can dial
		}
	}
	lan, err := lanIPv4()
	if err != nil {
		return "", err
	}
	if port == "" {
		return lan, nil
	}
	return net.JoinHostPort(lan, port), nil
}

// lanIPv4 picks an address another till on the shop LAN can dial. The
// enumeration this used to do by hand now lives in internal/lanip, which adds
// the route-probe fallback Android needs — enumeration is denied there, so
// this returned an error on every Android till and "show pairing code"
// rendered nothing at all (ut-docs#1499/#1501). Kept as a thin wrapper: it is
// the name the pairing code path and its tests already speak.
//
// Still enumeration-first, never a dial-out requirement: the till is
// offline-first (ADR-0003) and must pair two boxes on an isolated shop LAN
// with no gateway, no DNS and no internet at all.
func lanIPv4() (string, error) {
	return lanipIPv4()
}

// lanipIPv4 is a seam over lanip.IPv4 so a test can drive the
// no-LAN-address path — the exact path sync_api_test.go used to document as
// untestable "without mocking net.Interfaces()".
var lanipIPv4 = lanip.IPv4

// encodeEnrollCode packs the primary's URL + one-time token into ONE opaque,
// copy-pasteable code (base64url of the JSON), so the manual pairing "code"
// never shows raw {"url":…,"token":…} to whoever reads it off the screen.
// The QR carries the same string. (Issue #7 — until LAN auto-discovery
// replaces manual pairing entirely.)
//
// Since ut-docs#3219 this long code is no longer what a person is asked to
// type: the main till shows a short twin (XXX-XXX, see enrolTokens) plus its
// own address, and joinPrimary accepts either. The long code is kept for the
// QR and behind a "full code for copy & paste" disclosure.
func encodeEnrollCode(url, token string) string {
	b, _ := json.Marshal(map[string]string{"url": url, "token": token})
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeEnrollCode reverses encodeEnrollCode. It also accepts a raw-JSON
// payload (a QR/paste from a not-yet-upgraded primary) — base64url can't
// contain '{' or '"', so the two forms never collide. A short code
// ("K7P-4XQ") never decodes here (it is not base64url of a JSON object), so
// joinPrimary tries this first and falls back to normaliseShortCode.
func decodeEnrollCode(code string) (url, token string, err error) {
	code = strings.TrimSpace(code)
	var p struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if raw, e := base64.RawURLEncoding.DecodeString(code); e == nil {
		if json.Unmarshal(raw, &p) == nil && p.URL != "" && p.Token != "" {
			return p.URL, p.Token, nil
		}
	}
	if json.Unmarshal([]byte(code), &p) == nil && p.URL != "" && p.Token != "" {
		return p.URL, p.Token, nil
	}
	return "", "", fmt.Errorf("not a valid enrolment code")
}

// displayPrimaryAddress is what the main till shows as "the address to type"
// next to a short pairing code (ut-docs#3219): host:port for plain http —
// joinPrimary puts the http:// back — but the full URL for https, so the
// joining till doesn't silently dial the wrong scheme.
func displayPrimaryAddress(primaryURL string) string {
	a := strings.TrimSuffix(strings.TrimSpace(primaryURL), "/")
	if rest, ok := strings.CutPrefix(a, "http://"); ok {
		return rest
	}
	return a
}

// syncTill authenticates a replica's sync call by its bearer (only the
// SHA-256 is stored; the hash lookup makes timing attacks moot).
//
// It also enforces pinned LAN TLS per till (ADR-0114 §7, ut-docs#2737): the
// first bearer request a till makes over TLS records it as pinned, and from
// then on its bearer is refused on plain HTTP. A till sends TLS only once it
// holds this main till's pin (lantls.NewPeerClient) and then never falls
// back, so a plain request with a pinned till's bearer is a stolen bearer or
// a downgrade. A till that has never used TLS keeps plain HTTP (§11).
func syncTill(r *http.Request, repo *data.TillsRepo) (data.TillRow, bool) {
	h := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if h == "" {
		return data.TillRow{}, false
	}
	t, ok, err := repo.TillByBearerHash(r.Context(), hashBearer(h))
	if err != nil || !ok {
		return data.TillRow{}, false
	}
	switch {
	case r.TLS == nil && t.TLSPinned:
		warnPlainHTTPRefused(t.ID)
		return data.TillRow{}, false
	case r.TLS != nil && !t.TLSPinned:
		// Best effort: a failed write only means the next TLS request
		// records it; the request itself is authenticated either way.
		if err := repo.MarkTLSPinned(r.Context(), t.ID); err != nil {
			if _, seen := tlsPinRecordFailedWarned.LoadOrStore(t.ID, struct{}{}); !seen {
				logging.L().Warnf("sync: record till %s as TLS-pinned: %v", t.ID, err)
			}
		} else {
			tlsPinRecordFailedWarned.Delete(t.ID)
			logging.L().Infof("sync: till %s now uses pinned TLS; plain-HTTP sync requests from it are refused", t.ID)
		}
		t.TLSPinned = true
	}
	return t, true
}

// joinWithdrawWindow bounds how long after enrolment a till may withdraw
// its own enrolment (ut-docs#4566). A join enrols, then downloads the
// snapshot within a minute or two; the window is generous for a slow LAN.
const joinWithdrawWindow = 15 * time.Minute

// withinJoinWithdrawWindow reports whether a till enrolled at enrolledAt
// (tills.enrolled_at: SQLite datetime('now') in UTC, or RFC 3339) may still
// withdraw at now. An unparseable value fails closed.
func withinJoinWithdrawWindow(enrolledAt string, now time.Time) bool {
	for _, layout := range []string{time.DateTime, time.RFC3339} {
		if t, err := time.ParseInLocation(layout, enrolledAt, time.UTC); err == nil {
			age := now.Sub(t)
			return age >= -time.Minute && age <= joinWithdrawWindow
		}
	}
	return false
}

// plainHTTPRefusedWarned holds the till ids whose plain-HTTP refusal has
// been logged, so a client retrying every few seconds logs once per till
// per process, not once per request. Bounded by the enrolled tills.
var plainHTTPRefusedWarned sync.Map

// tlsPinRecordFailedWarned does the same for a failing MarkTLSPinned (a
// read-only DB would otherwise warn on every TLS request).
var tlsPinRecordFailedWarned sync.Map

func warnPlainHTTPRefused(tillID string) {
	if _, seen := plainHTTPRefusedWarned.LoadOrStore(tillID, struct{}{}); !seen {
		logging.L().Warnf("sync: refused a plain-HTTP request carrying the bearer of till %s, which uses pinned TLS (ADR-0114 §7)", tillID)
	}
}

// refusePlainSyncWhenClosed closes plain-HTTP peer sync entirely once the
// whole fleet has moved to pinned TLS (ADR-0114 §7, ut-docs#4411): this till
// serves TLS, it has at least one enrolled till, and every enrolled till has
// authenticated over TLS (TillsRepo.AllTillsTLSPinned). It is called at the
// top of the unauthenticated peer handshake routes — enroll, pair-request,
// the pair-request poll and primary-proof — before any rate limiter or
// one-time token, so a refused plain request spends neither. Bearer routes
// need no call: every till is pinned then, so syncTill already refuses its
// bearer on plain HTTP. Ping and the local browser's session routes never
// call it, so the till's own UI keeps working over plain HTTP.
//
// It writes the 403 and returns true when the request is refused. A DB error
// leaves the route open (logged once per process) rather than failing it:
// TLS must never be the cause of a failed request (ADR-0114 §7), and the
// per-till check in syncTill still protects every pinned till's bearer.
func refusePlainSyncWhenClosed(w http.ResponseWriter, r *http.Request, repo *data.TillsRepo) bool {
	if r.TLS != nil || lantls.Serving() == nil {
		return false
	}
	closed, err := repo.AllTillsTLSPinned(r.Context())
	if err != nil {
		if plainClosureCheckFailedWarned.CompareAndSwap(false, true) {
			logging.L().Warnf("sync: plain-HTTP closure check failed, leaving plain HTTP open: %v", err)
		}
		return false
	}
	if !closed {
		return false
	}
	if plainClosureRefusedWarned.CompareAndSwap(false, true) {
		logging.L().Warnf("sync: refused a plain-HTTP %s %s: every paired till uses pinned TLS, so plain-HTTP peer sync is closed (ADR-0114 §7)", r.Method, r.URL.Path)
	}
	http.Error(w, "plain HTTP sync is closed on this till; use pinned TLS on the same port", http.StatusForbidden)
	return true
}

// plainClosureRefusedWarned and plainClosureCheckFailedWarned make
// refusePlainSyncWhenClosed log its first refusal, and a failing closure
// check, once per process: a peer retrying every few seconds (or a broken
// DB) would otherwise warn on every plain request.
var plainClosureRefusedWarned, plainClosureCheckFailedWarned atomic.Bool

// registerSyncAPI returns the enrolTokens store it wires up so callers
// that issue their own tokens (e.g. registerPairingAPI's approve handler,
// ADR-0033 part 2/3) burn/validate against the SAME one-time store as the
// QR flow's /api/sync/enroll, rather than a second, disconnected one.
func registerSyncAPI(mux *http.ServeMux, d *common.Deps) *enrolTokens {
	repo := data.NewTillsRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)
	pairingRepo := data.NewPairingRepo(d.Db)
	tokens := &enrolTokens{tokens: map[string]time.Time{}}
	// ut-docs#3219: short pairing codes are only 30 bits, so each source
	// gets 5 tries a minute at /api/sync/enroll (long tokens stay
	// unlimited, as before). Its own instance: unrelated to pair-request.
	shortCodeLimiter := newPairRateLimiter(time.Minute, 5)

	// Tills page (manager): enrolled replicas + Add-till QR.
	mux.HandleFunc("GET /tills", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") {
			http.Redirect(w, r, "/settings", http.StatusSeeOther)
			return
		}
		// The roster (names, last seen and — on a main till — each till's
		// live link, ut-docs#2742) is shared with GET /ui/tills/roster,
		// which the page polls; tills_roster.go has the details.
		m, err := tillsRosterData(r.Context(), d, w, r)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "sync.error.server", err)
			return
		}
		// ut-docs#2722: the main till has stopped answering this replica —
		// the same view as the status-bar chip that links here (#2742).
		lv := replicaLinkView(r.Context(), d)
		m["title"] = httpx.T(httpx.RequestLocale(r), "page.title.tills")
		m["theme"] = d.CurrentState().Theme
		m["menuItems"] = d.MenuSnapshot()
		m["MainUnreachable"] = lv.State == linkUnreachable
		m["MainUnreachableSince"] = lv.Since
		// ut-docs#4507: joining replaces this till's sales too — the join
		// section warns and asks for a confirm (same as the join notice).
		m["HasSales"] = hasSalesHistory(r.Context(), d)
		// ut-docs#5058 UT-5: a joined till's re-join sends these first.
		if isJoinedTill(r.Context(), d) {
			m["IsJoinedTill"] = true
			m["UnsentSales"] = countUnsentSales(r.Context(), d)
		}
		httpx.Render("ui/pages/tills.html", m)(w, r)
	})

	// Issue a one-time enrolment token; responds with the short code + this
	// till's address (what a person types, ut-docs#3219), the QR, and the
	// long code for copy & paste (encode/decodeEnrollCode keep it opaque).
	mux.HandleFunc("POST /api/sync/enroll-token", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		// ut-docs#405 (independent review finding): enrolling is the other
		// half of the revoke guard above — a code minted on a REPLICA would
		// encode the replica's own address, so the new till would enrol
		// against the replica's local (non-authoritative) tills table
		// instead of the shop's real primary. It looks like it worked
		// (QR shown, till joins, gets a real bearer) right up until the
		// next admin-bundle pull prunes that row — the primary never knew
		// about it — and the new till's access silently vanishes ~30s
		// later with no explanation. Fail here, before ever showing a QR.
		if d.SyncPrimaryURL(r.Context()) != "" {
			http.Error(w, "pair new tills on the primary till", http.StatusConflict)
			return
		}
		// ut-docs#5058: a replaced main till shows no pairing code.
		if _, replaced := tillReplaced(r, d); replaced {
			common.LocalizedError(w, r, http.StatusConflict, "tills.pairing.error.this_till_replaced")
			return
		}
		_ = r.ParseForm()
		tok, short := tokens.issueWithShort()
		// The replica needs OUR address as it sees us; take the Host the
		// manager's browser used (LAN address), overridable via the form.
		primaryURL := strings.TrimSpace(r.Form.Get("url"))
		if primaryURL == "" {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			// NOT r.Host directly — see advertisableHost (ut-docs#362).
			advHost, err := advertisableHost(r.Host)
			if err != nil {
				// 200, not 409: this response is swapped in by htmx, and
				// htmx does not swap non-2xx — a 409 here reached the
				// operator as a completely blank panel, which is exactly
				// what the "show pairing code does nothing" report was
				// (ut-docs#1499). Same class as ut-docs#1455. The status
				// is the only thing that changes; the message, the log and
				// the refusal to mint an undialable code all stand.
				logging.L().Warnf("sync_api: cannot mint an enrolment code: %v", err)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				fmt.Fprintf(w, `<p class="error" role="alert">%s</p>`,
					html.EscapeString(httpx.T(httpx.ResolveLocale(w, r), "sync.error.no_lan_address")))
				return
			}
			primaryURL = scheme + "://" + advHost
		}
		code := encodeEnrollCode(primaryURL, tok)
		png, err := qrcode.Encode(code, qrcode.Medium, 220)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_api", err)
			return
		}
		locale := httpx.ResolveLocale(w, r)
		t := func(key string) string { return html.EscapeString(httpx.T(locale, key)) }
		// ut-docs#3219: what a person types is the short code + this
		// address; the QR and the long code (for copy & paste) stay.
		// dir="ltr" on the code and address: they read left-to-right even
		// on an ar/fa till.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w,
			`<div class="pairing-code-panel" style="text-align:center">
			 <p class="muted" style="margin-block:0">%s</p>
			 <p class="pairing-short-code" dir="ltr" style="font-family:monospace;font-size:2.6rem;font-weight:700;letter-spacing:.12em;margin-block:.2rem">%s</p>
			 <p class="muted" style="margin-block:0">%s</p>
			 <p class="pairing-address" dir="ltr" style="font-family:monospace;font-size:1.6rem;font-weight:600;margin-block:.2rem;overflow-wrap:anywhere">%s</p>
			 <small class="muted">%s</small>
			 <p style="margin-block:.8rem 0"><img alt="%s" src="data:image/png;base64,%s"><br>
			 <small class="muted">%s</small></p>
			 <details style="margin-block-start:.6rem;text-align:start"><summary>%s</summary>
			 <div dir="ltr" style="overflow-wrap:anywhere;word-break:break-all"><code style="user-select:all">%s</code></div></details></div>`,
			t("tills.pair_short_code_label"),
			html.EscapeString(formatShortCode(short)),
			t("tills.pair_address_label"),
			html.EscapeString(displayPrimaryAddress(primaryURL)),
			t("tills.qr_expiry"),
			t("tills.pair_qr_alt"),
			base64.StdEncoding.EncodeToString(png),
			t("tills.qr_hint"),
			t("tills.pair_full_code"),
			html.EscapeString(code))
	})

	// Replica enrolment — token IS the auth (one-time, 10-min), so this
	// path is middleware-exempt like the login flow.
	mux.HandleFunc("POST /api/sync/enroll", func(w http.ResponseWriter, r *http.Request) {
		// ut-docs#4411: before the limiter and the one-time token below.
		if refusePlainSyncWhenClosed(w, r, repo) {
			return
		}
		// ut-docs#405: defense in depth alongside the enroll-token guard
		// above — a stale QR/code minted before this fix (or copy-pasted
		// to the wrong device by hand) must still not be honoured by a
		// replica. See that guard's comment for the failure mode this
		// prevents.
		if d.SyncPrimaryURL(r.Context()) != "" {
			http.Error(w, "pair new tills on the primary till", http.StatusConflict)
			return
		}
		// ut-docs#5058: nor a code shown before the replacement.
		if refuseIfReplaced(w, r, d) {
			return
		}
		var in struct {
			Token string `json:"token"`
			Name  string `json:"name"`
			// ut-docs#2781: the role the joining till chose; absent (an
			// older till) means an additional till.
			Role string `json:"role"`
			// ut-docs#5058 UT-5: a re-joining till's own receipt prefix
			// (T<n>-), kept when no other till holds or used it.
			ReceiptPrefix string `json:"receipt_prefix"`
		}
		if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			_ = json.NewDecoder(r.Body).Decode(&in)
		} else {
			_ = r.ParseForm()
			in.Token, in.Name, in.Role = r.Form.Get("token"), r.Form.Get("name"), r.Form.Get("role")
			in.ReceiptPrefix = r.Form.Get("receipt_prefix")
		}
		// Validated before the one-time token is spent below.
		role, ok := tillRoleFromForm(in.Role)
		if !ok {
			http.Error(w, "role must be additional or satellite", http.StatusBadRequest)
			return
		}
		presented := strings.TrimSpace(in.Token)
		// Satellites are TLS-only from enrolment (ADR-0114 §7, ut-docs#4411),
		// whether the satellite role is the till's own choice or the
		// manager's on the approval card (RoleForToken, read here without
		// spending the token). Refused before the token is consumed, so the
		// till can retry the same token over TLS.
		if r.TLS == nil {
			if role == data.TillRoleSatellite {
				http.Error(w, "a satellite enrols over pinned TLS only", http.StatusForbidden)
				return
			}
			// A read error fails closed before the token is spent: answering
			// on would let a manager-chosen satellite enrol over plain HTTP
			// if the later read succeeds (review finding).
			approved, ok, err := pairingRepo.RoleForToken(r.Context(), presented)
			if err != nil {
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_api", err)
				return
			}
			if ok && approved == data.TillRoleSatellite {
				http.Error(w, "a satellite enrols over pinned TLS only", http.StatusForbidden)
				return
			}
		}
		// ut-docs#3219: a short pairing code (typed by hand, 30 bits) is
		// rate-limited per source and spends the shop-wide failure budget;
		// a long token is neither, exactly as before.
		if short, isShort := normaliseShortCode(presented); isShort {
			if !shortCodeLimiter.allow(sourceOf(r)) {
				http.Error(w, "too many pairing attempts; wait a minute and try again", http.StatusTooManyRequests)
				return
			}
			if !tokens.consumeShort(short) {
				http.Error(w, "invalid or expired enrolment token", http.StatusForbidden)
				return
			}
		} else if !tokens.consume(presented) {
			http.Error(w, "invalid or expired enrolment token", http.StatusForbidden)
			return
		}
		name := strings.TrimSpace(in.Name)
		if name == "" {
			name = "till"
		}
		// ut-docs#1264: a till's name must be unique on the shop's network,
		// case-insensitively — against every enrolled sibling AND the
		// primary's own effective name (till.name, or its translated
		// default). 422 specifically, NOT 409: 409 on this handler already
		// means "pair new tills on the primary till" (above), and
		// completeJoin tells the two apart by status code alone.
		// httpx.DefaultLocale(), NOT ResolveLocale(w, r) (independent review
		// finding): this resolves the PRIMARY's own name for an identity
		// comparison, not text rendered for the caller, so it must be read in
		// the primary's own configured locale. ResolveLocale honours the
		// request's ?lang/ut_lang, which would let the caller pick which
		// locale's default till name it is compared against and slip past the
		// check with the primary's displayed name in another locale. For the
		// real machine-to-machine call (no cookie, no query) the two are
		// already identical, so this changes no legitimate flow.
		primaryName := tillNameOrDefault(r.Context(), d, httpx.DefaultLocale())
		nameTaken, err := repo.NameTaken(r.Context(), name)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_api", err)
			return
		}
		if strings.EqualFold(name, primaryName) || nameTaken {
			common.LogAndLocalizedError(w, r, http.StatusUnprocessableEntity, "sync.error.name_taken", "sync_api",
				fmt.Errorf("till name %q already in use", name))
			return
		}
		// An approve-to-pair token carries the manager's final role from the
		// approval card (ut-docs#2781), which wins over the till's own.
		if approved, ok, err := pairingRepo.RoleForToken(r.Context(), presented); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_api", err)
			return
		} else if ok {
			role = approved
		}
		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		bearer := hex.EncodeToString(raw)
		// ut-docs#5074 S0: the till row and its receipt prefix in one
		// transaction. The primary is till 1; additional tills number from
		// 2, and a number once granted is never granted to another till
		// (revoked, withdrawn or promoted tills included). A re-joining till
		// keeps its own when it is free (ut-docs#5058 UT-5).
		tillID, tillNo, err := repo.EnrolTill(r.Context(), name, hashBearer(bearer), role, in.ReceiptPrefix)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_api", err)
			return
		}
		// A satellite (only ever enrolled over TLS, above) is pinned from
		// enrolment: its bearer is refused on plain HTTP at once, not from
		// its first TLS bearer request (ut-docs#4411). A failed write is
		// logged and the enrolment still succeeds — that first TLS bearer
		// request records the pin anyway (syncTill).
		if role == data.TillRoleSatellite {
			if err := repo.MarkTLSPinned(r.Context(), tillID); err != nil {
				logging.L().Warnf("sync: record satellite %s as TLS-pinned at enrolment: %v", tillID, err)
			}
		}
		// ut-docs#894: pin THIS (primary) till's own register identity BEFORE
		// adding a second register below. Independent review finding: a fresh
		// shop finishes setup with exactly one register and no persisted
		// sync.till_register_id (setup_page.go calls EnsureRegister only, and
		// the pairing QR lives on /tills, which never resolves), so the moment
		// enrolment adds register #2 the PRIMARY itself starts returning
		// pos.ErrRegisterIdentityAmbiguous — the very failure this card
		// removes for the joining till, transplanted onto the primary's own
		// Pfandrückgabe/cash-drawer payout path (shifts_api.go). Resolving
		// here, while exactly one register still exists, is unambiguous by
		// construction and persists the answer. Best-effort: a shop that was
		// ALREADY ambiguous (2+ registers, nothing picked) stays a manager's
		// call in Settings → Tills and must not fail the enrolment.
		if _, resolveErr := pos.ResolveTillRegisterID(r.Context(), d.Db, d.Settings); resolveErr != nil &&
			!errors.Is(resolveErr, pos.ErrRegisterIdentityAmbiguous) {
			logging.L().Errorf("pin primary register identity before enrolment: %v", resolveErr)
		}
		// Auto-provision a register for the joining till, named after it, so
		// the register is part of the snapshot the till downloads next — no
		// manual Settings → Registers step needed after the join. Fail OPEN
		// on this specific step (independent review finding): by this point
		// InsertTill has already committed and the one-time enrolment token
		// is already burned (tokens.consume, above), so a hard 500 here would
		// force the manager to mint a fresh pairing code over what may be a
		// transient DB error, and leaves an orphan till row behind. An empty
		// register_id degrades gracefully to exactly the pre-#894 behaviour —
		// completeJoin/ApplyReplicaIdentity already treat "" as "older
		// primary, no register sent" and fall back to manual assignment.
		registerID, registerName, err := posRepo.CreateRegisterForEnrolment(r.Context(), name)
		if err != nil {
			logging.L().Errorf("auto-provision register for enrolling till %s: %v", tillID, err)
			registerID, registerName = "", ""
		}
		_ = posRepo.InsertAudit(r.Context(), nil, "system", "till", tillID, "till_enrolled",
			map[string]any{"name": name, "register_id": registerID, "register_name": registerName, "role": role,
				// ut-docs#5074: which number it got, and whether it was the
				// one it asked for (a re-join), so a reclaimed number shows.
				"till_no": tillNo, "requested_prefix": in.ReceiptPrefix},
			time.Now().UTC().Format(time.RFC3339), "")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"till_id":     tillID,
				"bearer":      bearer,
				"shop_name":   storeNameOrDefault(r.Context(), d),
				"till_no":     tillNo,
				"register_id": registerID,
				"role":        role,
			},
			"error": nil,
		})
	})

	// ut-docs#4566: a joining till whose join failed after it enrolled
	// (snapshot download or staging failed, or a sale landed mid-join)
	// withdraws the enrolment it just got, so the manager's retry with the
	// same name is not refused as "name already in use". Bearer-gated: the
	// till is the bearer's own, never one named in the request, so a bearer
	// can withdraw no till but its own. Only within joinWithdrawWindow of
	// enrolment — removing an established till stays the manager's revoke.
	mux.HandleFunc("POST /api/sync/enroll/withdraw", func(w http.ResponseWriter, r *http.Request) {
		if d.SyncPrimaryURL(r.Context()) != "" {
			http.Error(w, "withdraw must be done on the primary till", http.StatusConflict)
			return
		}
		till, ok := syncTill(r, repo)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !withinJoinWithdrawWindow(till.EnrolledAt, time.Now()) {
			http.Error(w, "enrolment is too old to withdraw; revoke it on the main till", http.StatusConflict)
			return
		}
		// Read before the delete: the audit row outlives the till, but the
		// register is looked up by the till's own enrolment.
		registerID, regErr := posRepo.EnrolledRegisterID(r.Context(), till.ID)
		if err := repo.DeleteTill(r.Context(), till.ID); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_api", err)
			return
		}
		if d.Link != nil {
			d.Link.Disconnect(till.ID)
		}
		// The register enrolment provisioned for it goes too, unless anything
		// already uses it. Best-effort: the till itself is withdrawn either way.
		registerRemoved := false
		if regErr != nil {
			logging.L().Warnf("sync: withdraw till %s: find its enrolment register: %v", till.ID, regErr)
		} else if registerRemoved, regErr = posRepo.DeleteUnusedEnrolmentRegister(r.Context(), registerID); regErr != nil {
			logging.L().Warnf("sync: withdraw till %s: remove its enrolment register %s: %v", till.ID, registerID, regErr)
		}
		_ = posRepo.InsertAudit(r.Context(), nil, "system", "till", till.ID, "till_enrolment_withdrawn",
			map[string]any{"name": till.Name, "register_id": registerID, "register_removed": registerRemoved},
			time.Now().UTC().Format(time.RFC3339), "")
		w.Header().Set("HX-Trigger", "tills-changed")
		w.WriteHeader(http.StatusNoContent)
	})

	// D2: full-DB snapshot for a joining replica (bearer-gated). Uses the
	// backup mechanism (VACUUM INTO) — safe while selling. Served from a
	// throwaway bearer_hash-redacted COPY (ut-docs#426): the sync-auth
	// secret of every OTHER till must never reach a replica — the same rule
	// the D4 admin-bundle redactCols enforces on every incremental pull. The
	// real backup snapshot itself stays pristine (it's a disaster-recovery
	// artifact whose restore must bring back the real till roster).
	mux.HandleFunc("GET /api/sync/snapshot", func(w http.ResponseWriter, r *http.Request) {
		till, ok := syncTill(r, repo)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		path, cleanup, err := db.RedactedJoinSnapshot(d.Db, d.Cfg.DBPath)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_api", err)
			return
		}
		defer cleanup()
		_ = posRepo.InsertAudit(r.Context(), nil, "system", "till", till.ID, "snapshot_served",
			nil, time.Now().UTC().Format(time.RFC3339), "")
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeFile(w, r, path)
	})

	// Bearer-gated liveness — the first consumer of the sync surface;
	// D2/D3 endpoints mount alongside it.
	mux.HandleFunc("GET /api/sync/ping", func(w http.ResponseWriter, r *http.Request) {
		till, ok := syncTill(r, repo)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "unauthorized"})
			return
		}
		// ut-docs#5058: a replaced main till advertises no link (its link
		// route refuses), so its tills poll instead of redialling every 30s.
		link := 1
		if _, replaced := tillReplaced(r, d); replaced {
			link = 0
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			// link: this main till serves GET /api/sync/link at this level
			// (ADR-0114 §11) — a replica dials only when advertised.
			// version: what a replica without the link follows (ut-docs#2738).
			"data": map[string]any{"till_id": till.ID, "shop_name": storeNameOrDefault(r.Context(), d), "link": link,
				"version": buildinfo.Version},
			"error": nil,
		})
	})

	// Revoke a replica (manager, primary only).
	mux.HandleFunc("POST /api/sync/tills/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		// ut-docs#405: the shop's till roster now syncs to every replica
		// (adminTables), so .Tills on a replica's own /tills page is no
		// longer always empty — without this check a replica could revoke
		// a sibling row in its OWN local copy (a real DELETE, so it looks
		// like it worked, the row vanishing and all), while the shop-wide roster
		// on the primary is untouched: the row just reappears on the next
		// ~30s admin-bundle pull. Revocation is a primary-authoritative
		// write, same rule ADR-0011 §2 already states for catalog/settings
		// ("replicas never write ... directly").
		if d.SyncPrimaryURL(r.Context()) != "" {
			http.Error(w, "revoke must be done on the primary till", http.StatusConflict)
			return
		}
		id := r.PathValue("id")
		if err := repo.DeleteTill(r.Context(), id); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_api", err)
			return
		}
		// ADR-0114 §1: a revoked till loses its link at once, not at its
		// next reconnect (its bearer no longer authenticates either).
		if d.Link != nil {
			d.Link.Disconnect(id)
		}
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "till", id, "till_revoked",
			nil, time.Now().UTC().Format(time.RFC3339), "")
		// No HX-Refresh (ut-docs#2904): the Revoke button refreshes only
		// #tills-roster (tills_roster.html, "ok refresh-region");
		// tills-changed re-fetches the nav sync chip at once.
		w.Header().Set("HX-Trigger", "tills-changed")
		w.WriteHeader(http.StatusNoContent)
	})

	// Promote a replica to standalone/primary (D4): clears the sync
	// identity (keeping the receipt prefix — numbering must not collide
	// with the old primary) so the push/pull loops stop on their next tick
	// and the Tills page can pair new replicas from here. Documented
	// procedure: docs architecture/lan-sync.md "Promoting a replica".
	mux.HandleFunc("POST /api/sync/promote", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		// Validate the request body BEFORE ever checking elevation
		// (ut-docs#557 review finding): burning a manager's PIN entry on a
		// request that was always going to 400/409 regardless of who
		// approved it is a needless cost.
		if strings.TrimSpace(r.Form.Get("confirm")) != "PROMOTE" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "tills.promote_confirm_hint"))
			return
		}
		if d.SyncPrimaryURL(r.Context()) == "" {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "tills.promote_not_replica"))
			return
		}

		// Mutating + audit-writing (ut-docs#557): a denied session gets an
		// in-place PIN re-auth instead of a flat 403.
		elev := checkOrElevate(d, r, "sync_management", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/sync/promote", "#promote-msg",
				httpx.T(locale, "elevation.summary.sync_promote"), []elevationHiddenField{
					{Name: "confirm", Value: r.Form.Get("confirm")},
				}, elev)
			return
		}
		actorID := elev.ActorID
		if elev.Outcome == elevated {
			actorID = elev.ApproverID
		}

		if err := data.NewSettingsRepo(d.Db).ClearReplicaIdentity(r.Context()); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_api", err)
			return
		}
		// ut-docs#2753: no longer a replica, so no main till vouches for it.
		enroll.ForgetReplicaVouch()
		resolveReplicaSyncProblems()
		now := time.Now().UTC().Format(time.RFC3339)
		if elev.Outcome == elevated {
			_ = posRepo.InsertAuditElevated(r.Context(), nil, actorID, elev.ActorID, "till", "-", "till_promoted", nil, now, "")
		} else {
			_ = posRepo.InsertAudit(r.Context(), nil, actorID, "till", "-", "till_promoted", nil, now, "")
		}
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "tills.promoted"))
	})

	// Replica side (manager): join a primary. Enrols, downloads the full
	// snapshot, stages restore + identity — takes effect on restart (D2).
	mux.HandleFunc("POST /api/sync/join", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		_ = r.ParseForm()
		role, ok := tillRoleFromForm(r.Form.Get("role")) // ut-docs#2781
		if !ok {
			http.Error(w, "role must be additional or satellite", http.StatusBadRequest)
			return
		}
		if refuseJoinWithoutReplaceConfirm(w, r, d) { // ut-docs#4507
			return
		}
		shopName, err := joinPrimary(r, d,
			strings.TrimSpace(r.Form.Get("code")), strings.TrimSpace(r.Form.Get("address")),
			strings.TrimSpace(r.Form.Get("name")), role, r.Form.Get("replace_local_data") == "1")
		if isJoinErrKind(err, joinErrHasSales) { // ut-docs#4560
			writeReplaceRefusal(w, r)
			return
		}
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			// Escaped: a joinError's dynamic detail can embed the
			// operator-pasted primary URL (`Post "http://…": dial tcp …`),
			// so an unescaped write reflects attacker-chosen markup back
			// into the page (ut-docs#36).
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, html.EscapeString(friendlyJoinError(locale, err)))
			return
		}
		// ut-docs#1615: give the operator the same real restart action
		// pairing_wait.html's "joined" branch gives the discovery-list flow
		// (ut-docs#1550) instead of the old dead-end text — manager-driven,
		// so no auto-fire (a configured, possibly-in-use till restarts only
		// on the explicit click).
		renderJoinSuccess(w, r, shopName, "/api/sync/pairing-restart", false)
	})

	// First-boot wizard fork (middleware-exempt like /setup, and refuses
	// once an operator exists): a brand-new till joins the shop before any
	// local setup — the snapshot brings catalog, settings AND operators.
	mux.HandleFunc("POST /api/setup/join", func(w http.ResponseWriter, r *http.Request) {
		if firstBoot, err := d.AuthSvc.NeedsFirstBoot(r.Context()); err != nil || !firstBoot {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		_ = r.ParseForm()
		role, ok := tillRoleFromForm(r.Form.Get("role")) // ut-docs#2781
		if !ok {
			http.Error(w, "role must be additional or satellite", http.StatusBadRequest)
			return
		}
		if refuseJoinWithoutReplaceConfirm(w, r, d) { // ut-docs#4507
			return
		}
		shopName, err := joinPrimary(r, d,
			strings.TrimSpace(r.Form.Get("code")), strings.TrimSpace(r.Form.Get("address")),
			strings.TrimSpace(r.Form.Get("name")), role, r.Form.Get("replace_local_data") == "1")
		if isJoinErrKind(err, joinErrHasSales) { // ut-docs#4560
			writeReplaceRefusal(w, r)
			return
		}
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			// Escaped: a joinError's dynamic detail can embed the
			// operator-pasted primary URL (`Post "http://…": dial tcp …`),
			// so an unescaped write reflects attacker-chosen markup back
			// into the page (ut-docs#36).
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, html.EscapeString(friendlyJoinError(locale, err)))
			return
		}
		// ut-docs#1615: same fix as /api/sync/join above, but auto-fires on
		// render — a first-boot Pi kiosk has no shell to press a button
		// from (same reasoning as pairing_wait.html's autoRestart).
		renderJoinSuccess(w, r, shopName, "/api/setup/pairing-restart", true)
	})

	return tokens
}

// refuseJoinWithoutReplaceConfirm answers a code join (POST /api/sync/join,
// POST /api/setup/join) with 409 before joinPrimary when this till already
// holds sales and the form lacks replace_local_data=1 (ut-docs#4507): no
// enrolment, no snapshot, no staged restore. Same escaped fragment as a
// join error; app.js's htmx:beforeSwap swaps a text/html 4xx in. The
// first-boot wizard has no tick box: a first-boot till normally has no
// sales (no operator exists to ring one up), so there this is a fail-closed
// backstop with no in-wizard way past it — join from the Tills page instead.
func refuseJoinWithoutReplaceConfirm(w http.ResponseWriter, r *http.Request, d *common.Deps) bool {
	if !joinNeedsReplaceConfirm(r, d) {
		return false
	}
	writeReplaceRefusal(w, r)
	return true
}

// writeReplaceRefusal answers a code join refused for this till's sales:
// 409, the warning where the join's own message goes, and (ut-docs#4560)
// the tick box itself, swapped out of band into #pairing-replace-slot above
// the join tabs. Without it a till whose first sale landed after the page
// rendered — or the first-boot wizard, which never renders one — had no
// way to confirm short of guessing that a reload brings the box.
func writeReplaceRefusal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusConflict)
	renderReplaceSlot(w, r, true)
}

// renderReplaceSlot renders join_replace_slot.html: the out-of-band
// #pairing-replace-slot fill, preceded by the inline refusal when inline.
func renderReplaceSlot(w http.ResponseWriter, r *http.Request, inline bool) {
	httpx.RenderPartial("ui/partials/join_replace_slot.html", map[string]any{"inline": inline})(w, r)
}

// isJoinErrKind reports whether err is a *joinError of kind k.
func isJoinErrKind(err error, k joinErrKind) bool {
	var je *joinError
	return errors.As(err, &je) && je.kind == k
}

// joinErrKind classifies why joinPrimary/completeJoin failed, so the
// /api/sync/join and /api/setup/join handlers can render a localized
// message instead of raw English (ut-docs#36: the success path was already
// i18n'd via httpx.T, but every error here was a hardcoded fmt.Errorf shown
// straight to the operator, so a mis-pasted code showed English on an
// ar/fa/tr till).
type joinErrKind int

const (
	joinErrBadCode joinErrKind = iota
	joinErrRequestFailed
	joinErrUnreachable
	joinErrNotATill
	joinErrRefused
	joinErrNameTaken
	joinErrSnapshotFailed
	joinErrStageSnapshotFailed
	joinErrStageIdentityFailed
	// ut-docs#3219: short pairing code entered without / with an unusable
	// main-till address, and the main till throttling short-code attempts.
	joinErrNeedAddress
	joinErrBadAddress
	joinErrTooManyAttempts
	// ut-docs#4560: a sale landed while the join was being staged, and the
	// manager had not confirmed replacing this till's sales.
	joinErrHasSales
	// ut-docs#5058 UT-5: a re-join that could not send this till's unsent
	// journal to the new main till, and one refused while day closes are
	// not in the cloud yet. detail is the count.
	joinErrUnsentNotSent
	joinErrUnsentDayCloses
	joinErrUnsentDayClosesRefused
	joinErrMainReplaced // ut-docs#5058: that main till was replaced
)

// joinErrLocaleKey maps each kind to its web/locales/*.json key. Every key
// here must exist, translated, in every locale file — guard-i18n.sh enforces
// that the locale files' key sets match en.json exactly.
var joinErrLocaleKey = map[joinErrKind]string{
	joinErrBadCode:                "tills.join_error.bad_code",
	joinErrRequestFailed:          "tills.join_error.request_failed",
	joinErrUnreachable:            "tills.join_error.unreachable",
	joinErrNotATill:               "tills.join_error.not_a_till",
	joinErrRefused:                "tills.join_error.refused",
	joinErrNameTaken:              "tills.join_error.name_taken",
	joinErrSnapshotFailed:         "tills.join_error.snapshot_failed",
	joinErrStageSnapshotFailed:    "tills.join_error.stage_snapshot_failed",
	joinErrStageIdentityFailed:    "tills.join_error.stage_identity_failed",
	joinErrNeedAddress:            "tills.join_error.need_address",
	joinErrBadAddress:             "tills.join_error.bad_address",
	joinErrTooManyAttempts:        "tills.join_error.too_many_attempts",
	joinErrHasSales:               "tills.discovery.ambient.has_sales_warning",
	joinErrUnsentNotSent:          "tills.join_error.unsent_not_sent",
	joinErrUnsentDayCloses:        "tills.join_error.unsent_day_closes",
	joinErrUnsentDayClosesRefused: "tills.join_error.unsent_day_closes_refused",
	joinErrMainReplaced:           "tills.join_error.replaced",
}

// joinError is what every joinPrimary/completeJoin failure path returns.
// detail carries non-translatable dynamic content (a URL, a wrapped network
// error, an HTTP status) that gets substituted into the locale string's
// %s placeholder by friendlyJoinError — the same fmt.Sprintf(httpx.T(...),
// dynamicValue) convention catalog.error.barcode_conflict already uses
// (internal/pages/common/barcode_conflict.go). Error() returns an
// untranslated fallback for logs/tests, never shown to an operator directly.
type joinError struct {
	kind   joinErrKind
	detail string // "" if the locale string has no placeholder
}

func (e *joinError) Error() string {
	if e.detail == "" {
		return joinErrLocaleKey[e.kind]
	}
	return joinErrLocaleKey[e.kind] + ": " + e.detail
}

// friendlyJoinError renders a join/enrolment failure for the operator,
// translated via httpx.T. Falls back to a translated generic message (the
// raw error's text substituted in, same %s-placeholder convention as every
// other kind above) for anything that isn't a *joinError — defensive only,
// since every joinPrimary/completeJoin return path produces one, but
// ut-docs#1544 found even this fallback reaching the operator as raw
// English when it did fire.
func friendlyJoinError(locale string, err error) string {
	var je *joinError
	if !errors.As(err, &je) {
		return fmt.Sprintf(httpx.T(locale, "tills.join_error.unexpected"), err.Error())
	}
	msg := httpx.T(locale, joinErrLocaleKey[je.kind])
	if je.detail == "" {
		return msg
	}
	return fmt.Sprintf(msg, je.detail)
}

// joinPrimary runs the whole replica-side join: enrol with the one-time
// code, download the snapshot, stage restore + identity for the restart.
//
// code is either the long encodeEnrollCode string (QR / copy & paste —
// carries the main till's URL, so address is ignored) or, since
// ut-docs#3219, the short XXX-XXX code, which needs the main till's address
// typed alongside it.
func joinPrimary(r *http.Request, d *common.Deps, code, address, name, role string, replaceConfirmed bool) (string, error) {
	if primaryURL, token, err := decodeEnrollCode(code); err == nil {
		return completeJoin(r, d, primaryURL, token, name, role, "", replaceConfirmed)
	}
	short, ok := normaliseShortCode(code)
	if !ok {
		return "", &joinError{kind: joinErrBadCode}
	}
	if strings.TrimSpace(address) == "" {
		return "", &joinError{kind: joinErrNeedAddress}
	}
	primaryURL, ok := primaryURLFromAddress(address)
	if !ok {
		return "", &joinError{kind: joinErrBadAddress}
	}
	return completeJoin(r, d, primaryURL, short, name, role, "", replaceConfirmed)
}

// primaryURLFromAddress turns the address a person typed next to a short
// code ("192.168.1.10:8080", what the main till shows for plain http) into
// the base URL completeJoin dials. No scheme means http. Anything with a
// path, query, fragment or userinfo is refused: the main till never shows
// one, and refusing keeps the typed field from steering the request
// anywhere but a till's root.
func primaryURLFromAddress(address string) (string, bool) {
	a := strings.TrimSpace(address)
	// Checked on the raw text: url.Parse reports an empty fragment/query
	// for a bare trailing "#"/"?", which would then reach completeJoin as
	// "http://x:1#/api/sync/enroll" and fail as "not a till" (review finding).
	if strings.ContainsAny(a, "#?") {
		return "", false
	}
	if !strings.Contains(a, "://") {
		a = "http://" + a
	}
	a = strings.TrimSuffix(a, "/")
	if !validPrimaryBaseURL(a) {
		return "", false
	}
	u, err := url.Parse(a)
	if err != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || u.User != nil || u.Opaque != "" {
		return "", false
	}
	return a, true
}

// completeJoin is joinPrimary's tail, extracted so the approve-to-pair flow
// (ut-docs#185) can drive it directly with a (primaryURL, token) pair it
// already holds — that flow never has an encodeEnrollCode-packed code to
// decode, just the two values decodeEnrollCode would have produced.
func completeJoin(r *http.Request, d *common.Deps, primaryURL, token, name, role, primaryCertPin string, replaceConfirmed bool) (string, error) {
	// ut-docs#5058 UT-5 (rejoin_replay.go): a joined till re-joining sends
	// its unsent journal to the new main till before the wipe. Day closes
	// the cloud has not acknowledged would be wiped instead: refuse first,
	// before anything is enrolled.
	rejoin := isJoinedTill(r.Context(), d)
	if rejoin {
		n, err := rejoinUnsentDayCloses(r.Context(), d)
		if err != nil {
			return "", &joinError{kind: joinErrRequestFailed, detail: err.Error()}
		}
		if n > 0 {
			kind := joinErrUnsentDayCloses
			if settingTrim(r.Context(), d, data.ReportArchiveCloudRefusedAtKey) != "" {
				kind = joinErrUnsentDayClosesRefused // waiting would not help
			}
			return "", &joinError{kind: kind, detail: strconv.Itoa(n)}
		}
	}
	base := strings.TrimSuffix(primaryURL, "/")
	client := netaccess.NewClient(60 * time.Second)
	if primaryCertPin != "" {
		// Pairing learned the main till's pin (ut-docs#4091): the one-time
		// token and the new bearer go over TLS verified by that pin, never
		// plain HTTP (ADR-0114 §7, ut-docs#4092).
		tlsBase, ok := lantls.HTTPSBase(base)
		if !ok {
			return "", &joinError{kind: joinErrRequestFailed, detail: "pinned main till has no LAN address: " + base}
		}
		base, client = tlsBase, lantls.NewPinnedClient(60*time.Second, primaryCertPin).Client
	}

	enrol := map[string]string{"token": token, "name": name, "role": role}
	if rejoin {
		// ut-docs#5058 UT-5: keep numbering where this till left off.
		enrol["receipt_prefix"] = settingTrim(r.Context(), d, "sync.receipt_prefix")
	}
	body, _ := json.Marshal(enrol)
	var resp *http.Response
	if primaryCertPin == "" {
		// A code join (QR, long or short code) tries TLS on the main till's
		// same LAN port first, learning its pin, exactly as sendPairRequest
		// does (ADR-0114 §7, ut-docs#4411): a main till that has closed
		// plain HTTP still answers it, a satellite can only enrol this way,
		// and the joined till dials pinned from the start. There is no
		// visual compare on this path, so the pin is trust-on-first-use —
		// no weaker than the plain enrolment plus primary-proof backfill it
		// replaces. Plain HTTP only when the TLS attempt got no answer
		// (a main till without TLS, or an older one — Exchange's plainOK).
		if tlsBase, ok := lantls.HTTPSBase(base); ok {
			tlsReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
				tlsBase+"/api/sync/enroll", strings.NewReader(string(body)))
			if err != nil {
				return "", &joinError{kind: joinErrRequestFailed, detail: err.Error()}
			}
			tlsReq.Header.Set("Content-Type", "application/json")
			pc := lantls.NewPinnedClient(60*time.Second, "")
			tlsResp, plainOK, err := pc.Exchange(tlsReq)
			switch {
			case err == nil:
				primaryCertPin = pc.Pin()
				base, client, resp = tlsBase, lantls.NewPinnedClient(60*time.Second, primaryCertPin).Client, tlsResp
			case !plainOK:
				// No till there, or the request already reached it: a
				// plain retry would only double the wait or the request.
				return "", &joinError{kind: joinErrUnreachable, detail: err.Error()}
			}
		}
	}
	if resp == nil {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
			base+"/api/sync/enroll", strings.NewReader(string(body)))
		if err != nil {
			return "", &joinError{kind: joinErrRequestFailed, detail: err.Error()}
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err = client.Do(req)
		if err != nil {
			return "", &joinError{kind: joinErrUnreachable, detail: err.Error()}
		}
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			TillID   string `json:"till_id"`
			Bearer   string `json:"bearer"`
			ShopName string `json:"shop_name"`
			TillNo   int    `json:"till_no"`
			// The register the primary auto-provisioned for this till
			// (ut-docs#894); empty from an older primary.
			RegisterID string `json:"register_id"`
			// The role the main till enrolled this till as (ut-docs#2781);
			// empty from an older main till.
			Role string `json:"role"`
		} `json:"data"`
	}
	if resp.StatusCode == http.StatusNotFound {
		// Something answered, but it is not a till's enrolment endpoint —
		// usually the code points at the wrong machine. Saying "code used or
		// expired" here is what made ut-docs#362 undiagnosable from the shop
		// floor: it blames the code, so the owner regenerates it forever.
		return "", &joinError{kind: joinErrNotATill, detail: base}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		// ut-docs#3219: the main till throttles short-code attempts per
		// source — "wait a minute", not "the code is wrong".
		return "", &joinError{kind: joinErrTooManyAttempts}
	}
	if answerIsTillReplaced(resp) {
		// ut-docs#5058: the shop's owner replaced that main till.
		return "", &joinError{kind: joinErrMainReplaced}
	}
	if resp.StatusCode == http.StatusUnprocessableEntity {
		// ut-docs#1264: the primary rejected the name as already in use on
		// this shop's network. 422 is reserved for exactly this on
		// /api/sync/enroll — 409 there means something unrelated ("pair new
		// tills on the primary till"), which stays in the generic catch-all.
		return "", &joinError{kind: joinErrNameTaken}
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil || out.Data.Bearer == "" {
		return "", &joinError{kind: joinErrRefused}
	}
	// ut-docs#4566: from here on the main till holds an enrolment for this
	// till. Any failure below withdraws it (best-effort), so a retry with
	// the same name is not refused as "name already in use"; the original
	// error is what the manager sees either way.
	fail := func(je *joinError) (string, error) {
		withdrawEnrolment(r.Context(), client, base, out.Data.Bearer)
		return "", je
	}

	// ut-docs#5058 UT-5: the unsent journal goes to the new main till
	// FIRST, so the copy downloaded next already holds it.
	var replay rejoinReplay
	if rejoin {
		unsent := countUnsentSales(r.Context(), d)
		var rerr error
		replay, rerr = replayUnsentJournal(r.Context(), d, client, base, out.Data.Bearer, "")
		if rerr != nil {
			logging.L().Errorf("re-join: send this till's unsent journal to the new main till: %v", rerr)
			return fail(&joinError{kind: joinErrUnsentNotSent, detail: strconv.Itoa(unsent)})
		}
	}

	// Download the shop snapshot with our new bearer and stage it.
	sreq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, base+"/api/sync/snapshot", nil)
	if err != nil {
		return fail(&joinError{kind: joinErrRequestFailed, detail: err.Error()})
	}
	sreq.Header.Set("Authorization", "Bearer "+out.Data.Bearer)
	sresp, err := client.Do(sreq)
	if err != nil {
		return fail(&joinError{kind: joinErrSnapshotFailed, detail: err.Error()})
	}
	defer sresp.Body.Close()
	if sresp.StatusCode != http.StatusOK {
		return fail(&joinError{kind: joinErrSnapshotFailed, detail: sresp.Status})
	}
	if err := db.StageRestoreFromReader(d.Cfg.DBPath, sresp.Body); err != nil {
		return fail(&joinError{kind: joinErrStageSnapshotFailed, detail: err.Error()})
	}
	draw := make([]byte, 16)
	_, _ = rand.Read(draw)
	if err := db.StageReplicaIdentity(d.Cfg.DBPath, db.ReplicaIdentity{
		PrimaryURL:    primaryURL,
		TillID:        out.Data.TillID,
		Bearer:        out.Data.Bearer,
		ReceiptPrefix: fmt.Sprintf("T%d-", out.Data.TillNo),
		TillName:      name,
		DeviceID:      "till-" + hex.EncodeToString(draw),
		RegisterID:    out.Data.RegisterID,
		TillRole:      out.Data.Role,
		// ut-docs#4091: the pin pairing learned, or (ut-docs#4411) the
		// one a code join learned over TLS; "" after a plain enrolment.
		PrimaryCertPin: primaryCertPin,
	}); err != nil {
		// The snapshot is already staged: withdraw it too, or the restart
		// would restore the main till's copy with no replica identity.
		// Still staged → keep the enrolment rather than strand the restore.
		if uerr := db.UnstageJoin(d.Cfg.DBPath); uerr != nil {
			logging.L().Errorf("join: withdraw the staged snapshot after the identity failed: %v", uerr)
			return "", &joinError{kind: joinErrStageIdentityFailed, detail: err.Error()}
		}
		return fail(&joinError{kind: joinErrStageIdentityFailed, detail: err.Error()})
	}
	// ut-docs#4560: the caller checked for sales before enrolling, but the
	// enrolment and snapshot download take up to about a minute, and the
	// till keeps selling meanwhile. Unconfirmed and a sale has landed →
	// withdraw what was just staged, so the restart keeps this till's own
	// DB, and the enrolment with it (ut-docs#4566). A staged join that
	// could not be withdrawn still applies on restart, so its enrolment
	// stays.
	if !replaceConfirmed && hasSalesHistory(r.Context(), d) {
		if err := db.UnstageJoin(d.Cfg.DBPath); err != nil {
			logging.L().Errorf("join: withdraw the staged join after a sale landed: %v", err)
			return "", &joinError{kind: joinErrStageSnapshotFailed, detail: err.Error()}
		}
		return fail(&joinError{kind: joinErrHasSales})
	}
	if rejoin {
		// A sale rung while the copy downloaded is not in it: a last pass
		// sends it on, or the staged wipe is withdrawn.
		// From the stored cursor again, not from the newest sale sent: a sale
		// in the same second would be skipped, and the main till's apply
		// skips what it already has.
		last, err := replayUnsentJournal(r.Context(), d, client, base, out.Data.Bearer, "")
		if err != nil {
			logging.L().Errorf("re-join: last pass of this till's journal: %v", err)
			if uerr := db.UnstageJoin(d.Cfg.DBPath); uerr != nil {
				logging.L().Errorf("join: withdraw the staged join after the last journal pass failed: %v", uerr)
				return "", &joinError{kind: joinErrStageSnapshotFailed, detail: uerr.Error()}
			}
			return fail(&joinError{kind: joinErrUnsentNotSent, detail: strconv.Itoa(countUnsentSales(r.Context(), d))})
		}
		replay.Sales += last.Sales
		replay.NoSales += last.NoSales
		replay.ClearedBaskets += last.ClearedBaskets
	}
	posRepo := data.NewPOSRepo(d.Db)
	if rejoin {
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "till", out.Data.TillID, "rejoin_journal_sent",
			map[string]any{"primary": primaryURL, "sales": replay.Sales, "no_sales": replay.NoSales, "cleared_baskets": replay.ClearedBaskets},
			time.Now().UTC().Format(time.RFC3339), "")
		logging.L().Infof("re-join: sent %d unsent sale(s), %d no-sale open(s), %d cleared basket(s) to the new main till before the wipe (ut-docs#5058)",
			replay.Sales, replay.NoSales, replay.ClearedBaskets)
	}
	_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "till", out.Data.TillID, "joined_primary",
		map[string]any{"primary": primaryURL}, time.Now().UTC().Format(time.RFC3339), "")
	return out.Data.ShopName, nil
}

// withdrawEnrolment asks the main till at base to drop the enrolment that
// bearer was just issued for (POST /api/sync/enroll/withdraw, ut-docs#4566).
// Best-effort: a failure is logged, never returned — the join's own error
// is the one the manager sees, and a ghost entry can still be revoked on the
// main till's Tills page. It outlives a cancelled request so a manager who
// navigated away still gets the cleanup.
func withdrawEnrolment(ctx context.Context, client *http.Client, base, bearer string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/sync/enroll/withdraw", nil)
	if err != nil {
		logging.L().Warnf("join: withdraw the enrolment on the main till: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		logging.L().Warnf("join: withdraw the enrolment on the main till: %v", err)
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		logging.L().Warnf("join: withdraw the enrolment on the main till: %s", resp.Status)
	}
}
