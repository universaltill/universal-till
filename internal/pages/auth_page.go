package pages

import (
	"errors"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerAuth wires the PIN login flow (docs: architecture/pos-auth.md):
// the /login keypad (with one-time first-boot admin setup), session
// create/revoke, and the nav operator chip.
func registerAuth(mux *http.ServeMux, d *common.Deps, svc *auth.Service) {
	posRepo := data.NewPOSRepo(d.Db)

	setSessionCookie := func(w http.ResponseWriter, token string, maxAge int) {
		http.SetCookie(w, &http.Cookie{
			Name:     auth.CookieName,
			Value:    token,
			Path:     "/",
			MaxAge:   maxAge,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
	}

	renderLogin := func(w http.ResponseWriter, r *http.Request, errKey string) {
		firstBoot, err := svc.NeedsFirstBoot(r.Context())
		if err != nil {
			http.Error(w, "auth unavailable", http.StatusInternalServerError)
			return
		}
		httpx.RenderPartial("ui/pages/login.html", map[string]any{
			"firstBoot": firstBoot,
			"errKey":    errKey,
		})(w, r)
	}

	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(auth.CookieName); err == nil {
			if _, ok := svc.Resolve(r.Context(), c.Value); ok {
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
		}
		renderLogin(w, r, "")
	})

	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		pin := r.PostFormValue("pin")
		user, token, err := svc.Login(r.Context(), pin)
		now := time.Now().UTC().Format(time.RFC3339)
		switch {
		case errors.Is(err, auth.ErrLockedOut):
			_ = posRepo.InsertAudit(r.Context(), nil, "", "user", "-", "login_locked_out", nil, now, "")
			renderLogin(w, r, "auth.error.locked")
			return
		case errors.Is(err, auth.ErrInvalidPIN):
			_ = posRepo.InsertAudit(r.Context(), nil, "", "user", "-", "login_failed", nil, now, "")
			renderLogin(w, r, "auth.error.invalid")
			return
		case err != nil:
			http.Error(w, "login failed", http.StatusInternalServerError)
			return
		}
		_ = posRepo.InsertAudit(r.Context(), nil, user.ID, "user", user.ID, "login", nil, now, "")
		setSessionCookie(w, token, int(auth.SessionTTL.Seconds()))
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	// One-time first-boot: set the seeded admin's PIN and sign in. Refuses
	// to run once any operator can log in.
	mux.HandleFunc("POST /api/auth/setup", func(w http.ResponseWriter, r *http.Request) {
		firstBoot, err := svc.NeedsFirstBoot(r.Context())
		if err != nil || !firstBoot {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		_ = r.ParseForm()
		pin, pin2 := r.PostFormValue("pin"), r.PostFormValue("pin_confirm")
		if auth.ValidatePINFormat(pin) != nil {
			renderLogin(w, r, "auth.error.pin_format")
			return
		}
		if pin != pin2 {
			renderLogin(w, r, "auth.error.pin_mismatch")
			return
		}
		hash, err := auth.HashPIN(pin)
		if err != nil {
			renderLogin(w, r, "auth.error.pin_format")
			return
		}
		// The seeded 'system' user stays a service identity; first boot
		// creates (or reuses) a real admin operator.
		adminID, err := ensureFirstBootAdmin(r, svc)
		if err != nil {
			http.Error(w, "setup failed", http.StatusInternalServerError)
			return
		}
		if err := svc.Repo().SetUserPIN(r.Context(), adminID, hash); err != nil {
			http.Error(w, "setup failed", http.StatusInternalServerError)
			return
		}
		token, err := svc.CreateSession(r.Context(), adminID)
		if err != nil {
			http.Error(w, "setup failed", http.StatusInternalServerError)
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, adminID, "user", adminID, "first_boot_setup", nil, now, "")
		setSessionCookie(w, token, int(auth.SessionTTL.Seconds()))
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(auth.CookieName); err == nil {
			if u, ok := svc.Resolve(r.Context(), c.Value); ok {
				now := time.Now().UTC().Format(time.RFC3339)
				_ = posRepo.InsertAudit(r.Context(), nil, u.ID, "user", u.ID, "logout", nil, now, "")
			}
			svc.Logout(r.Context(), c.Value)
		}
		setSessionCookie(w, "", -1)
		// HTMX lock button and plain forms both land on the keypad.
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", "/login")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})

	// Operator chip in the nav: shows who is signed in + the Lock button.
	mux.HandleFunc("GET /ui/session-chip", func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.FromContext(r.Context())
		if !ok {
			w.WriteHeader(http.StatusOK)
			return
		}
		httpx.RenderPartial("ui/partials/session_chip.html", map[string]any{
			"name":      u.DisplayName,
			"isManager": u.IsManager(),
		})(w, r)
	})
}

// ensureFirstBootAdmin returns the operator to attach the first-boot PIN to:
// an existing active non-system admin, else any dormant 'admin' user
// (reactivated — its username would collide on create), else a fresh one.
func ensureFirstBootAdmin(r *http.Request, svc *auth.Service) (string, error) {
	users, err := svc.Repo().ListUsers(r.Context())
	if err != nil {
		return "", err
	}
	for _, u := range users {
		if u.Role == "admin" && u.IsActive && u.ID != "system" {
			return u.ID, nil
		}
	}
	for _, u := range users {
		if u.Username == "admin" && u.ID != "system" {
			if err := svc.Repo().SetUserActive(r.Context(), u.ID, true); err != nil {
				return "", err
			}
			return u.ID, nil
		}
	}
	return svc.Repo().CreateUser(r.Context(), "admin", "Administrator", "admin")
}
