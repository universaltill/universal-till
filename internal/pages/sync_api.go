package pages

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/universaltill/universal-till/internal/data"
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

func registerSyncAPI(mux *http.ServeMux, d *common.Deps) {
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
			"title":     "Tills",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.Menu,
			"Tills":     list,
		})(w, r)
	})

	// Issue a one-time enrolment token; responds with the QR + manual code.
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
		payload, _ := json.Marshal(map[string]string{"url": primaryURL, "token": tok})
		png, err := qrcode.Encode(string(payload), qrcode.Medium, 220)
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
			httpx.T(locale, "tills.qr_hint"), string(payload),
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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]string{
				"till_id":   tillID,
				"bearer":    bearer,
				"shop_name": storeNameOrDefault(r.Context(), d),
			},
			"error": nil,
		})
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
			"data": map[string]string{"till_id": till.ID, "shop_name": storeNameOrDefault(r.Context(), d)},
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

	// Replica side: join a primary (manager). Stores the enrolment; the
	// snapshot/pull loop lands with D2.
	mux.HandleFunc("POST /api/sync/join", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		code := strings.TrimSpace(r.Form.Get("code")) // the QR JSON, scanned or pasted
		var payload struct {
			URL   string `json:"url"`
			Token string `json:"token"`
		}
		if err := json.Unmarshal([]byte(code), &payload); err != nil || payload.URL == "" || payload.Token == "" {
			http.Error(w, "paste the full code from the primary till's QR", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.Form.Get("name"))
		body, _ := json.Marshal(map[string]string{"token": payload.Token, "name": name})
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
			strings.TrimSuffix(payload.URL, "/")+"/api/sync/enroll", strings.NewReader(string(body)))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "cannot reach the primary till: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		var out struct {
			Data struct {
				TillID   string `json:"till_id"`
				Bearer   string `json:"bearer"`
				ShopName string `json:"shop_name"`
			} `json:"data"`
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil || out.Data.Bearer == "" {
			http.Error(w, "primary refused the enrolment (token used or expired?)", http.StatusBadGateway)
			return
		}
		_ = d.Settings.Set(r.Context(), "sync.primary_url", payload.URL)
		_ = d.Settings.Set(r.Context(), "sync.till_id", out.Data.TillID)
		_ = d.Settings.Set(r.Context(), "sync.bearer", out.Data.Bearer)
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "till", out.Data.TillID, "joined_primary",
			map[string]any{"primary": payload.URL}, time.Now().UTC().Format(time.RFC3339), "")
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<span>✓ %s: %s</span>`, httpx.T(locale, "tills.joined"), out.Data.ShopName)
	})
}
