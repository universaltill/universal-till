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
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
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
type enrolTokens struct {
	mu     sync.Mutex
	tokens map[string]time.Time // token → expiry
}

func (e *enrolTokens) issue() string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	tok := hex.EncodeToString(raw)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tokens[tok] = time.Now().Add(10 * time.Minute)
	return tok
}

// consume validates and burns a token (one-time).
func (e *enrolTokens) consume(tok string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	exp, ok := e.tokens[tok]
	delete(e.tokens, tok)
	return ok && time.Now().Before(exp)
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

// lanIPv4 picks this host's first up, non-loopback IPv4 address. Deliberately
// enumerates interfaces rather than dialling out: the till is offline-first
// (ADR-0003) and must be able to pair two boxes on an isolated shop LAN with
// no gateway, no DNS and no internet at all.
func lanIPv4() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("cannot list network interfaces: %w", err)
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				return v4.String(), nil
			}
		}
	}
	return "", fmt.Errorf("this till has no network address other than loopback — " +
		"connect it to the shop network before pairing another till")
}

// encodeEnrollCode packs the primary's URL + one-time token into ONE opaque,
// copy-pasteable code (base64url of the JSON), so the manual pairing "code"
// never shows raw {"url":…,"token":…} to whoever reads it off the screen.
// The QR carries the same string. (Issue #7 — until LAN auto-discovery
// replaces manual pairing entirely.)
func encodeEnrollCode(url, token string) string {
	b, _ := json.Marshal(map[string]string{"url": url, "token": token})
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeEnrollCode reverses encodeEnrollCode. It also accepts a raw-JSON
// payload (a QR/paste from a not-yet-upgraded primary) — base64url can't
// contain '{' or '"', so the two forms never collide.
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

	// Tills page (manager): enrolled replicas + Add-till QR.
	mux.HandleFunc("GET /tills", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") {
			http.Redirect(w, r, "/settings", http.StatusSeeOther)
			return
		}
		list, err := repo.ListTills(r.Context())
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_api", err) // page-error:allow not yet migrated, tracked in ut-docs#1458
			return
		}
		// The primary's own name (ut-docs#396's till.name setting): shown
		// on this page regardless of role. till.name is NOT in
		// PerTillSettingPrefixes, so on a replica it's already the value
		// synced down from the primary via the admin bundle (ut-docs#405 —
		// this used to be primary-only: "a replica showing its primary is
		// a separate, out-of-scope concern," which is exactly the gap this
		// card closes; tillNameOrDefault needs no replica-specific branch
		// because that setting key means "the primary's name" everywhere
		// it's read, on any till).
		primaryURL := d.SyncPrimaryURL(r.Context())
		primaryName := tillNameOrDefault(r.Context(), d, httpx.ResolveLocale(w, r))
		// This device's own till id, when it's itself a replica — used
		// below only to tag its own row in .Tills (now populated on a
		// replica too, ut-docs#405's adminTables addition) as "(this
		// till)" rather than just another sibling.
		var thisTillID string
		if primaryURL != "" {
			thisTillID, _, _ = d.Settings.Get(r.Context(), "sync.till_id")
		}
		httpx.Render("ui/pages/tills.html", map[string]any{
			"title":           "Tills",
			"theme":           d.CurrentState().Theme,
			"menuItems":       d.MenuSnapshot(),
			"Tills":           list,
			"PrimaryTillName": primaryName,
			"SyncPrimary":     primaryURL,
			"ThisTillID":      thisTillID,
		})(w, r)
	})

	// Issue a one-time enrolment token; responds with the QR + manual code.
	// (encode/decodeEnrollCode keep the pairing "code" opaque — see below.)
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
		tok := tokens.issue()
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
				common.LogAndLocalizedError(w, r, http.StatusConflict, "sync.error.no_lan_address", "sync_api", err)
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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w,
			`<div style="text-align:center"><img alt="enrol QR" src="data:image/png;base64,%s"><br>
			 <small class="muted">%s</small><br>
			 <code style="user-select:all">%s</code><br>
			 <small class="muted">%s</small></div>`,
			base64.StdEncoding.EncodeToString(png),
			httpx.T(locale, "tills.qr_hint"), code,
			httpx.T(locale, "tills.qr_expiry"))
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
		if !tokens.consume(strings.TrimSpace(in.Token)) {
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
			"data":  map[string]string{"till_id": till.ID, "shop_name": storeNameOrDefault(r.Context(), d)},
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
			strings.TrimSpace(r.Form.Get("code")), strings.TrimSpace(r.Form.Get("name")))
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
		fmt.Fprintf(w, `<span>✓ %s: %s — %s</span>`, httpx.T(locale, "tills.joined"),
			shopName, httpx.T(locale, "tills.restart_to_finish"))
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
			strings.TrimSpace(r.Form.Get("code")), strings.TrimSpace(r.Form.Get("name")))
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
		fmt.Fprintf(w, `<span>✓ %s: %s — %s</span>`, httpx.T(locale, "tills.joined"),
			shopName, httpx.T(locale, "tills.restart_to_finish"))
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
// translated via httpx.T. Falls back to the raw error text (English,
// HTML-escaped by the caller) for anything that isn't a *joinError —
// defensive only, since every joinPrimary/completeJoin return path
// produces one.
func friendlyJoinError(locale string, err error) string {
	var je *joinError
	if !errors.As(err, &je) {
		return err.Error()
	}
	msg := httpx.T(locale, joinErrLocaleKey[je.kind])
	if je.detail == "" {
		return msg
	}
	return fmt.Sprintf(msg, je.detail)
}

// joinPrimary runs the whole replica-side join: enrol with the one-time
// code, download the snapshot, stage restore + identity for the restart.
func joinPrimary(r *http.Request, d *common.Deps, code, name string) (string, error) {
	primaryURL, token, err := decodeEnrollCode(code)
	if err != nil {
		return "", &joinError{kind: joinErrBadCode}
	}
	return completeJoin(r, d, primaryURL, token, name)
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
