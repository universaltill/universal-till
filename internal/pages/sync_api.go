package pages

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
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
		if !isManagerOrAuthOff(r) {
			http.Redirect(w, r, "/settings", http.StatusSeeOther)
			return
		}
		list, err := repo.ListTills(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		httpx.Render("ui/pages/tills.html", map[string]any{
			"title":       "Tills",
			"theme":       d.CurrentState().Theme,
			"menuItems":   d.Menu,
			"Tills":       list,
			"SyncPrimary": d.SyncPrimaryURL(r.Context()),
		})(w, r)
	})

	// Issue a one-time enrolment token; responds with the QR + manual code.
	// (encode/decodeEnrollCode keep the pairing "code" opaque — see below.)
	mux.HandleFunc("POST /api/sync/enroll-token", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
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
			primaryURL = scheme + "://" + r.Host
		}
		code := encodeEnrollCode(primaryURL, tok)
		png, err := qrcode.Encode(code, qrcode.Medium, 220)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
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
		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		bearer := hex.EncodeToString(raw)
		tillID, err := repo.InsertTill(r.Context(), name, hashBearer(bearer))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = posRepo.InsertAudit(r.Context(), nil, "system", "till", tillID, "till_enrolled",
			map[string]any{"name": name}, time.Now().UTC().Format(time.RFC3339), "")
		// The primary is till 1; replicas number from 2 (receipt prefixes).
		tillNo := 2
		if list, err := repo.ListTills(r.Context()); err == nil {
			tillNo = len(list) + 1
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"till_id":   tillID,
				"bearer":    bearer,
				"shop_name": storeNameOrDefault(r.Context(), d),
				"till_no":   tillNo,
			},
			"error": nil,
		})
	})

	// D2: full-DB snapshot for a joining replica (bearer-gated). Uses the
	// backup mechanism (VACUUM INTO) — safe while selling.
	mux.HandleFunc("GET /api/sync/snapshot", func(w http.ResponseWriter, r *http.Request) {
		till, ok := syncTill(r, repo)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		path, err := db.Snapshot(d.Db, d.Cfg.DBPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
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

	// Revoke a replica (manager).
	mux.HandleFunc("POST /api/sync/tills/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		id := r.PathValue("id")
		if err := repo.DeleteTill(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
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
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
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
		if err := data.NewSettingsRepo(d.Db).ClearReplicaIdentity(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "till", "-", "till_promoted",
			nil, time.Now().UTC().Format(time.RFC3339), "")
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "tills.promoted"))
	})

	// Replica side (manager): join a primary. Enrols, downloads the full
	// snapshot, stages restore + identity — takes effect on restart (D2).
	mux.HandleFunc("POST /api/sync/join", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		shopName, err := joinPrimary(r, d,
			strings.TrimSpace(r.Form.Get("code")), strings.TrimSpace(r.Form.Get("name")))
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			// Escaped: these errors embed the operator-pasted primary URL
			// (`Post "http://…": dial tcp …`), so an unescaped write reflects
			// attacker-chosen markup back into the page.
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, html.EscapeString(err.Error()))
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
			// Escaped: these errors embed the operator-pasted primary URL
			// (`Post "http://…": dial tcp …`), so an unescaped write reflects
			// attacker-chosen markup back into the page.
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		fmt.Fprintf(w, `<span>✓ %s: %s — %s</span>`, httpx.T(locale, "tills.joined"),
			shopName, httpx.T(locale, "tills.restart_to_finish"))
	})

	return tokens
}

// joinPrimary runs the whole replica-side join: enrol with the one-time
// code, download the snapshot, stage restore + identity for the restart.
func joinPrimary(r *http.Request, d *common.Deps, code, name string) (string, error) {
	primaryURL, token, err := decodeEnrollCode(code)
	if err != nil {
		return "", fmt.Errorf("paste the full code shown on the other till")
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
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot reach the primary till: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			TillID   string `json:"till_id"`
			Bearer   string `json:"bearer"`
			ShopName string `json:"shop_name"`
			TillNo   int    `json:"till_no"`
		} `json:"data"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil || out.Data.Bearer == "" {
		return "", fmt.Errorf("primary refused the enrolment (code used or expired?)")
	}

	// Download the shop snapshot with our new bearer and stage it.
	sreq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, base+"/api/sync/snapshot", nil)
	if err != nil {
		return "", err
	}
	sreq.Header.Set("Authorization", "Bearer "+out.Data.Bearer)
	sresp, err := client.Do(sreq)
	if err != nil {
		return "", fmt.Errorf("snapshot download failed: %w", err)
	}
	defer sresp.Body.Close()
	if sresp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("snapshot download failed: %s", sresp.Status)
	}
	if err := db.StageRestoreFromReader(d.Cfg.DBPath, sresp.Body); err != nil {
		return "", fmt.Errorf("stage snapshot: %w", err)
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
	}); err != nil {
		return "", fmt.Errorf("stage identity: %w", err)
	}
	posRepo := data.NewPOSRepo(d.Db)
	_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "till", out.Data.TillID, "joined_primary",
		map[string]any{"primary": primaryURL}, time.Now().UTC().Format(time.RFC3339), "")
	return out.Data.ShopName, nil
}
