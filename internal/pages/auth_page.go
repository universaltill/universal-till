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
func setSessionCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// sanitizeLoginNext allow-lists the login flow's post-auth destination —
// it must never echo an arbitrary redirect target back from client input
// (open-redirect risk). "kiosk" is the only recognized value today: the
// self-order kiosk's exit-with-PIN link (ut-docs#208) needs a destination
// other than "/", since "/" itself redirects right back to /self-order for
// every session while display.mode=self_order (see registerIndex) — that
// redirect exists for anonymous customers, but it also caught out a
// manager arriving via /login for the exact same reason, silently looping
// them back into the kiosk instead of ever reaching settings.
func sanitizeLoginNext(v string) string {
	if v == "kiosk" {
		return "kiosk"
	}
	return ""
}

// loginDestination maps a sanitized "next" to the actual redirect target.
func loginDestination(next string) string {
	if next == "kiosk" {
		return "/settings"
	}
	return "/"
}

func registerAuth(mux *http.ServeMux, d *common.Deps, svc *auth.Service) {
	posRepo := data.NewPOSRepo(d.Db)

	renderLogin := func(w http.ResponseWriter, r *http.Request, errKey, next string) {
		firstBoot, err := svc.NeedsFirstBoot(r.Context())
		if err != nil {
			http.Error(w, "auth unavailable", http.StatusInternalServerError)
			return
		}
		data := map[string]any{
			"firstBoot": firstBoot,
			"errKey":    errKey,
			"next":      next,
		}
		// The kiosk exit's own back-out and idle-reset: without these, one
		// stray tap into the PIN pad — easy on an unattended touchscreen —
		// strands the terminal indefinitely, since /login itself has no
		// exit/idle mechanism of its own (ut-docs#208 review finding).
		if next == "kiosk" {
			data["kioskIdleResetSecs"] = d.CurrentState().KioskIdleResetSeconds
		}
		httpx.RenderPartial("ui/pages/login.html", data)(w, r)
	}

	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		next := sanitizeLoginNext(r.URL.Query().Get("next"))
		if c, err := r.Cookie(auth.CookieName); err == nil {
			if _, ok := svc.Resolve(r.Context(), c.Value); ok {
				http.Redirect(w, r, loginDestination(next), http.StatusSeeOther)
				return
			}
		}
		// A brand-new till goes through the guided wizard instead of the
		// bare PIN form (docs repo: architecture/zero-touch-setup.md).
		if firstBoot, err := svc.NeedsFirstBoot(r.Context()); err == nil && firstBoot {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		renderLogin(w, r, "", next)
	})

	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		pin := r.PostFormValue("pin")
		next := sanitizeLoginNext(r.PostFormValue("next"))
		user, token, err := svc.Login(r.Context(), pin)
		now := time.Now().UTC().Format(time.RFC3339)
		switch {
		case errors.Is(err, auth.ErrLockedOut):
			_ = posRepo.InsertAudit(r.Context(), nil, "", "user", "-", "login_locked_out", nil, now, "")
			renderLogin(w, r, "auth.error.locked", next)
			return
		case errors.Is(err, auth.ErrInvalidPIN):
			_ = posRepo.InsertAudit(r.Context(), nil, "", "user", "-", "login_failed", nil, now, "")
			renderLogin(w, r, "auth.error.invalid", next)
			return
		case err != nil:
			http.Error(w, "login failed", http.StatusInternalServerError)
			return
		}
		_ = posRepo.InsertAudit(r.Context(), nil, user.ID, "user", user.ID, "login", nil, now, "")
		setSessionCookie(w, token, int(auth.SessionTTL.Seconds()))
		http.Redirect(w, r, loginDestination(next), http.StatusSeeOther)
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
			renderLogin(w, r, "auth.error.pin_format", "")
			return
		}
		if pin != pin2 {
			renderLogin(w, r, "auth.error.pin_mismatch", "")
			return
		}
		hash, err := auth.HashPIN(pin)
		if err != nil {
			renderLogin(w, r, "auth.error.pin_format", "")
			return
		}
		// A fresh till needs a real usable register the moment this bare
		// fallback completes first boot too — same gap and fix as the
		// guided wizard (ut-docs#429).
		if _, err := posRepo.EnsureRegister(r.Context()); err != nil {
			http.Error(w, "setup failed", http.StatusInternalServerError)
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

	// Self-service PIN change (pos-auth follow-up): any operator, own PIN
	// only; wrong current PIN counts against the shared device lockout;
	// success revokes the user's sessions → sign back in with the new PIN.
	mux.HandleFunc("GET /pin", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.FromContext(r.Context()); !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		httpx.Render("ui/pages/pin.html", map[string]any{
			"title":     "Change PIN",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.Menu,
			"errKey":    r.URL.Query().Get("err"),
		})(w, r)
	})

	// NOT under /api/auth/ — that prefix is middleware-exempt for the login
	// flow, and this endpoint needs the authenticated operator in context.
	mux.HandleFunc("POST /api/pin/change", func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.FromContext(r.Context())
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		_ = r.ParseForm()
		newPIN := r.PostFormValue("new_pin")
		if newPIN != r.PostFormValue("new_pin2") {
			http.Redirect(w, r, "/pin?err=auth.error.pin_mismatch", http.StatusSeeOther)
			return
		}
		err := svc.ChangeOwnPIN(r.Context(), u.ID, r.PostFormValue("current_pin"), newPIN)
		now := time.Now().UTC().Format(time.RFC3339)
		switch {
		case errors.Is(err, auth.ErrLockedOut):
			_ = posRepo.InsertAudit(r.Context(), nil, u.ID, "user", u.ID, "pin_change_locked_out", nil, now, "")
			http.Redirect(w, r, "/pin?err=auth.error.locked", http.StatusSeeOther)
			return
		case errors.Is(err, auth.ErrInvalidPIN):
			_ = posRepo.InsertAudit(r.Context(), nil, u.ID, "user", u.ID, "pin_change_failed", nil, now, "")
			http.Redirect(w, r, "/pin?err=auth.error.invalid", http.StatusSeeOther)
			return
		case errors.Is(err, auth.ErrPINTaken):
			http.Redirect(w, r, "/pin?err=users.error.pin_taken", http.StatusSeeOther)
			return
		case err != nil:
			http.Redirect(w, r, "/pin?err=auth.error.pin_format", http.StatusSeeOther)
			return
		}
		_ = posRepo.InsertAudit(r.Context(), nil, u.ID, "user", u.ID, "pin_changed", nil, now, "")
		setSessionCookie(w, "", -1)
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
