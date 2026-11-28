package pages

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/lanaccess"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Settings key for "Allow other tills on this network" (ut-docs#6955).
// A desktop main till (Mac/Windows) listens on every interface, but
// internal/lanaccess refuses devices other than this machine unless:
//
//	auto (default): a pairing code was shown in the last 10 minutes, or the
//	                shop already has another till (so it can keep syncing);
//	on:             always;
//	off:            never.
const (
	keyLANAccess = "lan.access"
	lanModeAuto  = "auto"
	lanModeOn    = "on"
	lanModeOff   = "off"
)

// lanPolicyTTL is a var only as a test seam (ut-docs#7091).
var lanPolicyTTL = 2 * time.Second

// lanAllowed is the lanaccess policy. It is read once per incoming
// connection, so the answer is cached briefly: a port scan must not become a
// database load.
func lanAllowed(d *common.Deps, repo *data.TillsRepo) func() bool {
	var (
		mu      sync.Mutex
		at      time.Time
		last    bool
		hasLast bool
	)
	return func() bool {
		mu.Lock()
		defer mu.Unlock()
		// A pairing window is cheap and must open instantly.
		if lanaccess.WindowOpen() {
			return true
		}
		if hasLast && time.Since(at) < lanPolicyTTL {
			return last
		}
		last, hasLast, at = lanDecide(context.Background(), d, repo), true, time.Now()
		return last
	}
}

func lanMode(ctx context.Context, d *common.Deps) string {
	if d.Settings == nil {
		return lanModeAuto
	}
	v, ok, err := d.Settings.Get(ctx, keyLANAccess)
	if err != nil || !ok {
		return lanModeAuto
	}
	switch v {
	case lanModeOn, lanModeOff:
		return v
	}
	return lanModeAuto
}

func lanDecide(ctx context.Context, d *common.Deps, repo *data.TillsRepo) bool {
	switch lanMode(ctx, d) {
	case lanModeOn:
		return true
	case lanModeOff:
		return false
	}
	tills, err := repo.ListTills(ctx)
	return err == nil && len(tills) > 0
}

// registerLANAccess installs the policy and the Settings control.
func registerLANAccess(mux *http.ServeMux, d *common.Deps, repo *data.TillsRepo) {
	lanaccess.SetPolicy(lanAllowed(d, repo))

	mux.HandleFunc("GET /ui/settings/lan-access", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		writeLANAccessFragment(w, r, d, "")
	})
	mux.HandleFunc("POST /api/settings/lan-access", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		mode := r.Form.Get("mode")
		if mode != lanModeAuto && mode != lanModeOn && mode != lanModeOff {
			http.Error(w, "bad mode", http.StatusBadRequest)
			return
		}
		if err := d.Settings.Set(r.Context(), keyLANAccess, mode); err != nil {
			logging.L().Warnf("settings: save lan access: %v", err)
			http.Error(w, "save failed", http.StatusInternalServerError)
			return
		}
		if mode == lanModeOff {
			// "never" also ends a pairing window opened a moment ago.
			lanaccess.CloseWindow()
		}
		logging.L().Infof("settings: other tills on this network = %s", mode)
		writeLANAccessFragment(w, r, d, httpx.T(httpx.ResolveLocale(w, r), "settings.lan.saved"))
	})
}

func writeLANAccessFragment(w http.ResponseWriter, r *http.Request, d *common.Deps, note string) {
	loc := httpx.ResolveLocale(w, r)
	cur := lanMode(r.Context(), d)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div class="set-gap-s" data-testid="lan-access"><h3>%s</h3><p class="muted">%s</p>`,
		html.EscapeString(httpx.T(loc, "settings.lan.title")), html.EscapeString(httpx.T(loc, "settings.lan.help")))
	for _, m := range []string{lanModeAuto, lanModeOn, lanModeOff} {
		cls := "btn btn-touch secondary"
		if m == cur {
			cls = "btn btn-touch"
		}
		fmt.Fprintf(w, `<button type="button" class="%s" data-testid="lan-access-%s" aria-pressed="%t" hx-post="/api/settings/lan-access" hx-vals='{"mode":"%s"}' hx-target="closest [data-testid=lan-access]" hx-swap="outerHTML">%s</button> `,
			cls, m, m == cur, m, html.EscapeString(httpx.T(loc, "settings.lan.mode."+m)))
	}
	if note != "" {
		fmt.Fprintf(w, `<p role="status">%s</p>`, html.EscapeString(note))
	}
	fmt.Fprint(w, `</div>`)
}
