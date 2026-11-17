package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#6402: the About screen carries Privacy notice, Terms, Open-source
// licences and Delete your account on EVERY channel, for every signed-in role
// (links, not manager-gated content), and never leads to a purchase page.

func TestAboutPage_LinksOnEveryChannelAndRole(t *testing.T) {
	roles := map[string]auth.User{"manager": mgrUser, "cashier": cashUser}
	for _, c := range allChannels {
		for roleName, u := range roles {
			t.Run(string(c)+"/"+roleName, func(t *testing.T) {
				withChannel(t, c)
				mux, _ := checkNowMux(t)
				registerAbout(mux, nil)
				code, body := ppGet(mux, "/about", u)
				if code != http.StatusOK {
					t.Fatalf("GET /about = %d", code)
				}
				// The licences link and version show everywhere. The three external
				// addresses show as links where the platform can follow them and as
				// plain text on a unix kiosk (crossdevicelinkactionable), so the
				// test holds on macOS, Windows and Linux CI alike.
				for _, want := range []string{
					`data-testid="about-licences-link"`, `href="/about/licences"`,
					`data-testid="about-version"`,
					aboutPrivacyURL, aboutTermsURL, aboutDeleteAccountURL,
				} {
					if !strings.Contains(body, want) {
						t.Errorf("%s About missing %s", c, want)
					}
				}
				if !strings.Contains(body, `data-testid="about-privacy-link"`) && !strings.Contains(body, `data-testid="about-addresses"`) {
					t.Errorf("%s About shows the privacy address neither as a link nor as text", c)
				}
				low := strings.ToLower(body)
				for _, bad := range []string{"/plans", "checkout", "pricing"} {
					if strings.Contains(low, bad) {
						t.Errorf("%s About contains %q", c, bad)
					}
				}
			})
		}
	}
}

// Review M5: through the REAL auth middleware with real sessions, not
// auth.WithUser on the bare mux. Every role reads About and the in-app
// licences page (with a back link); a stranger is sent to login.
func TestAboutPage_ThroughRealAuthMiddleware(t *testing.T) {
	for _, role := range []string{"manager", "cashier"} {
		t.Run(role, func(t *testing.T) {
			mux, svc, d := newFullAuthDeps(t)
			registerAbout(mux, d)
			id, err := svc.Repo().CreateUser(t.Context(), "u-"+role, "U "+role, role)
			if err != nil {
				t.Fatal(err)
			}
			tok, err := svc.CreateSession(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			h := auth.Middleware(mux, svc)
			get := func(path string, withCookie bool) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				if withCookie {
					req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: tok})
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				return rec
			}
			if rec := get("/about", false); rec.Code == http.StatusOK {
				t.Fatalf("anonymous GET /about = 200, want a redirect to login")
			}
			if rec := get("/about", true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `data-testid="about-licences-link"`) {
				t.Fatalf("%s GET /about = %d", role, rec.Code)
			}
			rec := get("/about/licences", true)
			body := rec.Body.String()
			if rec.Code != http.StatusOK {
				t.Fatalf("%s GET /about/licences = %d", role, rec.Code)
			}
			for _, want := range []string{`data-testid="about-licences-back"`, `href="/about"`, `data-testid="about-licences-text"`, "Open-source licences used by Universal Till", `id="about-licences"`} {
				if !strings.Contains(body, want) {
					t.Errorf("%s licences page missing %q", role, want)
				}
			}
			if strings.Contains(body, `target="_blank"`) {
				t.Errorf("licences page must not open a new window")
			}
		})
	}
}

// Review M5: where a followed link has no way back (unix kiosk) the website
// addresses are shown as text, never as links or new-window targets.
func TestAboutPage_KioskShowsAddressesAsText(t *testing.T) {
	orig := httpx.CrossDeviceLinkActionable
	t.Cleanup(func() { httpx.CrossDeviceLinkActionable = orig })
	for _, actionable := range []bool{true, false} {
		mux, _ := checkNowMux(t)
		registerAbout(mux, nil)
		httpx.CrossDeviceLinkActionable = func() bool { return actionable } // after the mux: its helpers snapshot/reset httpx seams
		_, body := ppGet(mux, "/about", mgrUser)
		if strings.Contains(body, `target="_blank"`) != actionable {
			t.Errorf("actionable=%v: target=_blank present=%v", actionable, !actionable)
		}
		if strings.Contains(body, `href="`+aboutPrivacyURL+`"`) != actionable {
			t.Errorf("actionable=%v: privacy href mismatch", actionable)
		}
		if !actionable && !strings.Contains(body, aboutPrivacyURL) {
			t.Errorf("kiosk must still show the privacy address as text")
		}
		if !strings.Contains(body, `href="/about/licences"`) {
			t.Errorf("licences link is in-app and must always be a link")
		}
	}
}

func TestAboutURLs(t *testing.T) {
	if !strings.HasSuffix(aboutPrivacyURL, "/legal/app-privacy/") {
		t.Errorf("privacy link must be the app privacy notice (#6398): %s", aboutPrivacyURL)
	}
	for name, u := range map[string]string{"privacy": aboutPrivacyURL, "terms": aboutTermsURL, "delete": aboutDeleteAccountURL} {
		if !strings.HasPrefix(u, "https://") || !strings.Contains(u, "/en-gb/") {
			t.Errorf("%s URL %q: want https with the locale (en-gb) in the path", name, u)
		}
	}
}
