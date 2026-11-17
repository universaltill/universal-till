package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/web"
)

// The About screen (ut-docs#6402, ADR-0194 §2.3, legal record 6380): the
// links a store review and a privacy regulator expect inside the app. They
// exist on EVERY channel and for every signed-in role, because they are
// links, not manager content. None of them leads to checkout or plans.
//
// The links open the website's chrome-less in-app legal view (/app/legal/, no
// Pricing or sign-up in the header; ut-website, review M1). The website legal pages carry the locale in the URL (en-gb: English is the
// operative text, same rule as enroll.TermsURL).
const (
	// The in-app privacy notice is the app notice (card #6398), not the
	// website/billing policy.
	aboutPrivacyURL = "https://universaltill.com/en-gb/app/legal/app-privacy/"
	aboutTermsURL   = "https://universaltill.com/en-gb/app/legal/terms/"
	// PLACEHOLDER (ut-docs#6402): the self-service Universal Till ID deletion
	// page does not exist yet. Card #6399 delivers it and sets the final URL;
	// replace this constant (and nothing else) when it lands. Until then this
	// path must not be shipped to a store build.
	aboutDeleteAccountURL = "https://universaltill.com/en-gb/account/delete/"
	// aboutLicencesPath is an in-app page (inside the till layout, with a back
	// link): a raw .txt in a new window would be a dead end on a kiosk or the
	// desktop webview. The text is generated at build time (tools/gen-licenses,
	// `make licenses`) and embedded with web/public.
	aboutLicencesPath = "/about/licences"
)

func registerAbout(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /about/licences", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		text, err := web.FS.ReadFile("public/licenses.txt")
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "about_licences", err)
			return
		}
		data := map[string]any{
			"title":        httpx.T(locale, "about.licences_link"),
			"licencesText": string(text),
		}
		if d != nil {
			data["theme"] = d.ResolvedTheme(r)
			data["menuItems"] = d.MenuSnapshot()
		}
		httpx.Render("ui/pages/about_licences.html", data)(w, r)
	})
	mux.HandleFunc("GET /about", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		data := map[string]any{
			"title":        httpx.T(locale, "about.title"),
			"privacyURL":   aboutPrivacyURL,
			"termsURL":     aboutTermsURL,
			"deleteURL":    aboutDeleteAccountURL,
			"licencesPath": aboutLicencesPath,
		}
		if d != nil {
			data["theme"] = d.ResolvedTheme(r)
			data["menuItems"] = d.MenuSnapshot()
		}
		httpx.Render("ui/pages/about.html", data)(w, r)
	})
}
