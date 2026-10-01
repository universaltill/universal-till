package pages

import (
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
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/lanip"
	"github.com/universaltill/universal-till/internal/logging"
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
func syncTill(r *http.Request, repo *data.TillsRepo) (data.TillRow, bool) {
	h := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if h == "" {
		return data.TillRow{}, false
	}
	t, ok, err := repo.TillByBearerHash(r.Context(), hashBearer(h))
	if err != nil || !ok {
		return data.TillRow{}, false
	}
	return t, true
}

// registerSyncAPI returns the enrolTokens store it wires up so callers
// that issue their own tokens (e.g. registerPairingAPI's approve handler,
// ADR-0033 part 2/3) burn/validate against the SAME one-time store as the
// QR flow's /api/sync/enroll, rather than a second, disconnected one.
func registerSyncAPI(mux *http.ServeMux, d *common.Deps) *enrolTokens {
	repo := data.NewTillsRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)
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
		// ut-docs#405: defense in depth alongside the enroll-token guard
		// above — a stale QR/code minted before this fix (or copy-pasted
		// to the wrong device by hand) must still not be honoured by a
		// replica. See that guard's comment for the failure mode this
		// prevents.
		if d.SyncPrimaryURL(r.Context()) != "" {
			http.Error(w, "pair new tills on the primary till", http.StatusConflict)
			return
		}
		var in struct {
			Token string `json:"token"`
			Name  string `json:"name"`
		}
		if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			_ = json.NewDecoder(r.Body).Decode(&in)
		} else {
			_ = r.ParseForm()
			in.Token, in.Name = r.Form.Get("token"), r.Form.Get("name")
		}
		// ut-docs#3219: a short pairing code (typed by hand, 30 bits) is
		// rate-limited per source and spends the shop-wide failure budget;
		// a long token is neither, exactly as before.
		presented := strings.TrimSpace(in.Token)
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
		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		bearer := hex.EncodeToString(raw)
		tillID, err := repo.InsertTill(r.Context(), name, hashBearer(bearer))
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_api", err)
			return
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
			map[string]any{"name": name, "register_id": registerID, "register_name": registerName},
			time.Now().UTC().Format(time.RFC3339), "")
		// The primary is till 1; replicas number from 2 (receipt prefixes).
		tillNo := 2
		if list, err := repo.ListTills(r.Context()); err == nil {
			tillNo = len(list) + 1
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"till_id":     tillID,
				"bearer":      bearer,
				"shop_name":   storeNameOrDefault(r.Context(), d),
				"till_no":     tillNo,
				"register_id": registerID,
			},
			"error": nil,
		})
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
		_ = json.NewEncoder(w).Encode(map[string]any{
			// link: this main till serves GET /api/sync/link at this level
			// (ADR-0114 §11) — a replica dials only when advertised.
			// version: what a replica without the link follows (ut-docs#2738).
			"data": map[string]any{"till_id": till.ID, "shop_name": storeNameOrDefault(r.Context(), d), "link": 1,
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
		// like it worked, HX-Refresh and all), while the shop-wide roster
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
		w.Header().Set("HX-Refresh", "true")
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
		shopName, err := joinPrimary(r, d,
			strings.TrimSpace(r.Form.Get("code")), strings.TrimSpace(r.Form.Get("address")),
			strings.TrimSpace(r.Form.Get("name")))
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
		shopName, err := joinPrimary(r, d,
			strings.TrimSpace(r.Form.Get("code")), strings.TrimSpace(r.Form.Get("address")),
			strings.TrimSpace(r.Form.Get("name")))
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
)

// joinErrLocaleKey maps each kind to its web/locales/*.json key. Every key
// here must exist, translated, in every locale file — guard-i18n.sh enforces
// that the locale files' key sets match en.json exactly.
var joinErrLocaleKey = map[joinErrKind]string{
	joinErrBadCode:             "tills.join_error.bad_code",
	joinErrRequestFailed:       "tills.join_error.request_failed",
	joinErrUnreachable:         "tills.join_error.unreachable",
	joinErrNotATill:            "tills.join_error.not_a_till",
	joinErrRefused:             "tills.join_error.refused",
	joinErrNameTaken:           "tills.join_error.name_taken",
	joinErrSnapshotFailed:      "tills.join_error.snapshot_failed",
	joinErrStageSnapshotFailed: "tills.join_error.stage_snapshot_failed",
	joinErrStageIdentityFailed: "tills.join_error.stage_identity_failed",
	joinErrNeedAddress:         "tills.join_error.need_address",
	joinErrBadAddress:          "tills.join_error.bad_address",
	joinErrTooManyAttempts:     "tills.join_error.too_many_attempts",
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
func joinPrimary(r *http.Request, d *common.Deps, code, address, name string) (string, error) {
	if primaryURL, token, err := decodeEnrollCode(code); err == nil {
		return completeJoin(r, d, primaryURL, token, name)
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
	return completeJoin(r, d, primaryURL, short, name)
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
func completeJoin(r *http.Request, d *common.Deps, primaryURL, token, name string) (string, error) {
	base := strings.TrimSuffix(primaryURL, "/")
	client := &http.Client{Timeout: 60 * time.Second}

	body, _ := json.Marshal(map[string]string{"token": token, "name": name})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		base+"/api/sync/enroll", strings.NewReader(string(body)))
	if err != nil {
		return "", &joinError{kind: joinErrRequestFailed, detail: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", &joinError{kind: joinErrUnreachable, detail: err.Error()}
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

	// Download the shop snapshot with our new bearer and stage it.
	sreq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, base+"/api/sync/snapshot", nil)
	if err != nil {
		return "", &joinError{kind: joinErrRequestFailed, detail: err.Error()}
	}
	sreq.Header.Set("Authorization", "Bearer "+out.Data.Bearer)
	sresp, err := client.Do(sreq)
	if err != nil {
		return "", &joinError{kind: joinErrSnapshotFailed, detail: err.Error()}
	}
	defer sresp.Body.Close()
	if sresp.StatusCode != http.StatusOK {
		return "", &joinError{kind: joinErrSnapshotFailed, detail: sresp.Status}
	}
	if err := db.StageRestoreFromReader(d.Cfg.DBPath, sresp.Body); err != nil {
		return "", &joinError{kind: joinErrStageSnapshotFailed, detail: err.Error()}
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
	}); err != nil {
		return "", &joinError{kind: joinErrStageIdentityFailed, detail: err.Error()}
	}
	posRepo := data.NewPOSRepo(d.Db)
	_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "till", out.Data.TillID, "joined_primary",
		map[string]any{"primary": primaryURL}, time.Now().UTC().Format(time.RFC3339), "")
	return out.Data.ShopName, nil
}
