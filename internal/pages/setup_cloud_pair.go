package pages

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/languard"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Pair-first setup (ADR-0166, ut-docs#4610): in the first-run wizard, "Join
// an existing shop" -> "Main till (first till of the shop)" takes the
// my.universaltill.com pairing code. POST /api/setup/cloud-pair pairs with
// first_till:true and HOLDS the answer in memory only; the final POST
// /api/setup (cloud_paired=1) takes the shop's name, country and currency
// from it, runs setup as usual and only then persists the credential. So no
// check-in or sync can run on an unfinished wizard.
//
// The route is auth-exempt (a first-boot till has no operator), so it is
// guarded against a web page in the till's own browser pairing the till
// into an attacker's shop (review 2026-10-17): a per-process wizard nonce,
// a Host allow-list (DNS rebinding), and Sec-Fetch-Site/Origin checks.

const (
	// cloudPairHold is how long a pairing is held in memory (ADR-0166 §4).
	cloudPairHold = 30 * time.Minute
	// cloudPairTimeout bounds the one synchronous call to the cloud.
	cloudPairTimeout = 20 * time.Second

	// setupNonceHeader is how the HTMX calls send the nonce; the final
	// native form posts it as the setup_nonce field.
	setupNonceHeader = "X-UT-Setup-Nonce"
	setupNonceField  = "setup_nonce"

	keyCloudExpired = "setup.cloud.error.expired"
)

// setupCloudPairLimiter is the per-source cap on the wizard's cloud routes,
// the same budget as /api/setup/pair-start (ADR-0166 §2). Package-level so a
// test can widen it.
var setupCloudPairLimiter = newPairRateLimiter(time.Minute, 5)

// cloudPairNow is the clock seam for the hold's expiry.
var cloudPairNow = time.Now

type heldCloudPairing struct {
	p        *enroll.FirstTillPairing
	tillName string
	at       time.Time
}

// setupCloudState is the wizard's in-memory cloud state: one nonce per
// process (minted by the first first-boot render, never rotated on later
// ones so the wizard's own redirects back to /setup keep a held pairing)
// and at most one held pairing.
type setupCloudState struct {
	mu    sync.Mutex
	nonce string
	held  *heldCloudPairing
}

func (s *setupCloudState) ensureNonce() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nonce == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			// No randomness: leave the nonce empty, so every guarded call
			// is refused (fail closed).
			return ""
		}
		s.nonce = hex.EncodeToString(b[:])
	}
	return s.nonce
}

func (s *setupCloudState) nonceOK(got string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nonce != "" && got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.nonce)) == 1
}

func (s *setupCloudState) hold(p *enroll.FirstTillPairing, tillName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nonce == "" {
		// Setup finished while the pair call was in flight (finish() clears
		// the nonce): holding now would strand the hold on a till that is
		// already set up.
		return
	}
	s.held = &heldCloudPairing{p: p, tillName: tillName, at: cloudPairNow()}
	// ut-docs#5127: no till-born store while the shop's identity is pending.
	enroll.HoldRegistration()
}

// current is the held pairing, nil when none or older than cloudPairHold.
func (s *setupCloudState) current() *heldCloudPairing {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held != nil && cloudPairNow().Sub(s.held.at) > cloudPairHold {
		s.held = nil
		enroll.ReleaseRegistration()
	}
	return s.held
}

func (s *setupCloudState) drop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.held = nil
	enroll.ReleaseRegistration()
}

// finish discards the nonce and any pairing: setup completed.
func (s *setupCloudState) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.held, s.nonce = nil, ""
	enroll.ReleaseRegistration()
}

// guard answers 403 unless the request is a same-origin call from the till's
// own page: loopback / own-interface / allow-listed Host, Sec-Fetch-Site
// absent or same-origin, Origin absent or this request's own origin, and the
// wizard nonce (header or form field, constant-time compare).
func (s *setupCloudState) guard(allow []*net.IPNet, w http.ResponseWriter, r *http.Request) bool {
	deny := func() bool {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return false
	}
	if !languard.HostAllowed(r.Host, allow) {
		return deny()
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return deny()
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.EqualFold(u.Host, r.Host) {
			return deny()
		}
	}
	got := r.Header.Get(setupNonceHeader)
	if got == "" {
		got = r.PostFormValue(setupNonceField)
	}
	if !s.nonceOK(got) {
		return deny()
	}
	return true
}

// cloudPairErrorKey maps a pairing failure to the wizard's in-place message.
func cloudPairErrorKey(err error) string {
	var he *enroll.RegisterHTTPError
	switch {
	case errors.Is(err, enroll.ErrNotConfigured), errors.Is(err, enroll.ErrPairIdentityPinned):
		return "setup.cloud.error.unavailable"
	case errors.As(err, &he):
		switch {
		case he.Status == http.StatusTooManyRequests:
			return "setup.cloud.error.too_many"
		case he.Status == http.StatusForbidden || he.Status == http.StatusBadRequest:
			return "setup.cloud.error.invalid_code"
		}
		if he.Status == http.StatusConflict {
			switch he.Code {
			case "replace_target_changed":
				return "setup.cloud.error.replace_target_changed"
			case "replace_needs_owner":
				return "setup.cloud.error.replace_needs_owner"
			case "replace_code_main_till_only":
				return "setup.cloud.error.replace_main_till_only"
			}
		}
		return "setup.cloud.error.failed"
	}
	var netErr net.Error
	var urlErr *url.Error
	if errors.As(err, &netErr) || errors.As(err, &urlErr) {
		return "setup.cloud.error.no_network"
	}
	return "setup.cloud.error.failed"
}

func cloudPairMessage(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<span class="error" role="alert">%s</span>`, html.EscapeString(msg))
}

// maxReplaceTargetLen is the cloud's limit on replace_device_id.
const maxReplaceTargetLen = 200

// bidiIsolate wraps a cloud-supplied name or a date in Unicode first-strong
// isolates, so it keeps its own direction inside an RTL sentence (ar, fa).
func bidiIsolate(s string) string { return "\u2068" + s + "\u2069" }

// cloudTillName is a till's fleet-row name, or the "unnamed" word.
func cloudTillName(locale, name string) string {
	if strings.TrimSpace(name) == "" {
		return httpx.T(locale, "setup.cloud.error.unnamed_till")
	}
	return name
}

// lastSeenText is the cloud's RFC 3339 last-seen instant as "YYYY-MM-DD
// HH:MM UTC" in a bidi isolate: the owner compares it with when the old till
// died (design §2 R7), so the time matters, not only the date. "-" when the
// cloud has none.
func lastSeenText(raw string) string {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return "-"
	}
	// Non-breaking hyphens and spaces: a date split over two lines reads
	// out of order in the RTL locales.
	s := t.UTC().Format("2006-01-02 15:04") + " UTC"
	s = strings.NewReplacer("-", "\u2060-\u2060", " ", "\u00a0").Replace(s)
	return bidiIsolate(s)
}

// renderReplaceConfirm answers 409 replace_confirm_required with the
// "Replace <old till>?" panel (ut-docs#5058, design §4.2). Nothing is held.
func renderReplaceConfirm(w http.ResponseWriter, r *http.Request, locale string, rc *enroll.ReplaceConfirmError) {
	_, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(rc.LastUsedAt))
	httpx.RenderPartial("ui/partials/setup_cloud_replace.html", map[string]any{
		"Name":        bidiIsolate(cloudTillName(locale, rc.Name)),
		"LastSeen":    lastSeenText(rc.LastUsedAt),
		"HasLastSeen": parseErr == nil,
		// Only a cloud-master shop's products, staff and settings come
		// back from the cloud (design §1 table, D5).
		"CloudCatalog": rc.Store != nil && strings.EqualFold(strings.TrimSpace(rc.Store.CatalogMaster), "cloud"),
		"DeviceID":     rc.DeviceID,
	})(w, r)
}

func registerSetupCloudPair(mux *http.ServeMux, d *common.Deps, cs *setupCloudState, allow []*net.IPNet) {
	gate := rateLimited(setupCloudPairLimiter, firstBootGate(d))

	mux.HandleFunc("POST /api/setup/cloud-pair", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r) {
			return
		}
		_ = r.ParseForm()
		if !cs.guard(allow, w, r) {
			return
		}
		locale := httpx.ResolveLocale(w, r)
		tillName, nameErr := validateTillName(r.PostFormValue("name"))
		if nameErr != nil {
			cloudPairMessage(w, httpx.T(locale, keyTillNameRequired))
			return
		}
		// ut-docs#5058: the confirm panel's Replace button alone sends
		// confirm_replace=1; only then does the cloud's replace target travel.
		// A stale hidden field left by any other submit is ignored.
		replaceID := ""
		confirming := r.PostFormValue("confirm_replace") == "1"
		if confirming {
			replaceID = strings.TrimSpace(r.PostFormValue("replace_device_id"))
			if replaceID == "" || len(replaceID) > maxReplaceTargetLen {
				cloudPairMessage(w, httpx.T(locale, "setup.cloud.error.failed"))
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), cloudPairTimeout)
		defer cancel()
		// The code is a credential: never logged.
		p, err := enroll.PairFirstTill(ctx, d.Cfg, r.PostFormValue("code"), tillName, replaceID)
		if err != nil {
			var has *enroll.StoreHasTillError
			if errors.As(err, &has) {
				name := bidiIsolate(cloudTillName(locale, has.Name))
				cloudPairMessage(w, fmt.Sprintf(httpx.T(locale, "setup.cloud.error.store_has_main_till"), name, lastSeenText(has.LastUsedAt)))
				return
			}
			var rc *enroll.ReplaceConfirmError
			if errors.As(err, &rc) {
				if confirming {
					// The cloud ignored the confirmation (a cloud that does
					// not replace yet): never loop on the same panel.
					cloudPairMessage(w, httpx.T(locale, "setup.cloud.error.replace_unsupported"))
					return
				}
				renderReplaceConfirm(w, r, locale, rc)
				return
			}
			var online *enroll.MainTillOnlineError
			if errors.As(err, &online) {
				cloudPairMessage(w, fmt.Sprintf(httpx.T(locale, "setup.cloud.error.main_till_online"), lastSeenText(online.LastSeen)))
				return
			}
			// Already Warn-logged once by enroll.PairFirstTill.
			cloudPairMessage(w, httpx.T(locale, cloudPairErrorKey(err)))
			return
		}
		cs.hold(p, tillName)
		// A full load: GET /setup with a held pairing opens "Your shop".
		w.Header().Set("HX-Redirect", "/setup")
		w.WriteHeader(http.StatusOK)
	})

	// "This is not my shop": drop the held pairing. Its credential was never
	// used, so the cloud revokes it after 24 hours (ADR-0166 §3).
	mux.HandleFunc("POST /api/setup/cloud-unpair", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r) {
			return
		}
		_ = r.ParseForm()
		if !cs.guard(allow, w, r) {
			return
		}
		cs.drop()
		w.Header().Set("HX-Redirect", "/setup")
		w.WriteHeader(http.StatusOK)
	})
}

// uiAllowNets is UT_UI_ALLOW_CIDRS parsed for the Host allow-list; an invalid
// value allows nothing extra (as pages.Init does for the LAN guard).
func uiAllowNets(d *common.Deps) []*net.IPNet {
	if d == nil || d.Cfg == nil {
		return nil
	}
	nets, err := languard.ParseCIDRs(strings.Split(d.Cfg.UIAllowCIDRs, ","))
	if err != nil {
		return nil
	}
	return nets
}

// cloudShop is the held pairing's shop, as the "Your shop" step shows it.
// A field the cloud left empty (older cloud, till-born store) is absent, and
// the wizard asks the local step instead.
type cloudShop struct {
	Name            string
	HasName         bool
	CountryCode     string
	CountryNameKey  string
	HasCountry      bool
	Currency        string // a code this build knows, else ""
	CurrencyDecimal *int
	Tax             setupCountry
	// ShopType is the type chosen on my. (ADR-0171), a valid ADR-0026 value
	// or "": when set the wizard skips its shop-type step.
	ShopType string
	// Replaced is set when this pairing replaced the shop's main till
	// (ut-docs#5058): "Your shop" then names it instead of offering "This
	// is not my shop" — the owner confirmed before the code was spent
	// (ADR-0166 §4 amendment). ReplacedName is the cloud's, "" if unnamed.
	Replaced     bool
	ReplacedName string
}

func cloudShopView(r *http.Request, h *heldCloudPairing, countries []setupCountry) cloudShop {
	var v cloudShop
	if rp := h.p.Replaced; rp != nil {
		v.Replaced = true
		if strings.TrimSpace(rp.Name) != "" {
			v.ReplacedName = bidiIsolate(rp.Name)
		}
	}
	st := h.p.Store
	if st == nil {
		return v
	}
	// The same refusal as the final post (isRefusedStoreName, incl. the
	// localized placeholder): a name it would refuse is asked locally.
	if n := strings.TrimSpace(st.Name); n != "" && !isRefusedStoreName(r, n) {
		v.Name, v.HasName = n, true
	}
	if isValidShopType(st.ShopType) {
		v.ShopType = st.ShopType
	}
	code := strings.ToUpper(strings.TrimSpace(st.Country))
	for _, c := range countries {
		if code != "" && c.Code == code {
			v.CountryCode, v.CountryNameKey, v.HasCountry, v.Tax = c.Code, c.NameKey, true, c
		}
	}
	if cur := strings.ToUpper(strings.TrimSpace(st.Currency)); cur != "" && httpx.IsKnownCurrency(cur) {
		v.Currency = cur
		if d := st.CurrencyDecimals; d != nil && *d >= 0 && *d <= 4 {
			v.CurrencyDecimal = d
		}
	}
	return v
}

// applyCloudShop replaces the posted country, currency, tax prefill and shop
// name with the held pairing's (ADR-0166 §4 step 2), so a tampered form
// cannot diverge from the cloud. A field the cloud left empty keeps the
// posted value, which the usual validation then checks.
func applyCloudShop(r *http.Request, d *common.Deps, h *heldCloudPairing) {
	countries, err := wizardCountries(r.Context(), d.Db)
	if err != nil {
		countries = builtinSetupCountries()
	}
	shop := cloudShopView(r, h, countries)
	set := func(k, v string) {
		r.Form.Set(k, v)
		r.PostForm.Set(k, v)
	}
	if shop.HasName {
		set("store_name", shop.Name)
	}
	if shop.HasCountry {
		set("country", shop.CountryCode)
		set("tax_rate_pct", shop.Tax.TaxRate)
		if shop.Tax.TaxInclusive {
			set("tax_inclusive", "on")
		} else {
			set("tax_inclusive", "off")
		}
		set("currency", shop.Tax.Currency)
	}
	if shop.Currency != "" {
		set("currency", shop.Currency)
		set("currency_touched", "1") // the shop chose it: KeyCurrencyConfirmed
	}
	// ADR-0171 §4: the cloud's shop type wins over any posted one, so the
	// one shop-type save (saveSetupShopType) cannot be steered by a crafted
	// post. With none from the cloud the posted value stays (asked locally).
	if shop.ShopType != "" {
		set("shop_type", shop.ShopType)
	}
}

// commitCloudPairing persists the held pairing's credential (a seam so the
// tests can prove it runs after the seed, and fail it).
var commitCloudPairing = func(ctx context.Context, d *common.Deps, p *enroll.FirstTillPairing) error {
	_, err := p.Commit(ctx, d.Cfg, d.Settings)
	return err
}

// persistCloudShopCurrency records the cloud's currency and decimals as the
// shop currency this till adopted (ADR-0163 amendment), so a later sync
// neither re-adopts nor reports it.
func persistCloudShopCurrency(r *http.Request, d *common.Deps, h *heldCloudPairing) {
	ctx := r.Context()
	countries, err := wizardCountries(ctx, d.Db)
	if err != nil {
		countries = builtinSetupCountries()
	}
	shop := cloudShopView(r, h, countries)
	if shop.Currency == "" {
		return
	}
	kv := map[string]string{
		entitlement.KeyShopCurrency:        shop.Currency,
		entitlement.KeyShopCurrencyAdopted: shop.Currency,
	}
	if shop.CurrencyDecimal != nil {
		kv[entitlement.KeyShopCurrencyDecimals] = strconv.Itoa(*shop.CurrencyDecimal)
	}
	// settings-write:allow first-boot wizard of a main till
	if err := d.Settings.SetMany(ctx, kv); err != nil {
		logging.L().Warnf("setup wizard: save the shop currency from the cloud: %v", err)
	}
}
