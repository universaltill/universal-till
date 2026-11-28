package pages

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pluginview"
)

// Basket lines from the sell-screen overlay (ADR-0121 amendment 2026-11-28,
// ut-docs#7206; format: ut-docs reference/plugin-views.md). A page view
// opened over the sell screen (opens_view) may draw `suggestions` whose
// items are add_to_basket with an optional opaque per-line ref. Core draws
// each as a button it has signed for exactly (plugin, sku, qty, ref); the
// button posts to pluginViewPickRoute, which verifies the signature, checks
// the plugin is still active and holds view:sales, and adds the line through
// the /api/pos/scan handler (every scan rule applies, the price is the
// catalogue's) stamped with the ref. A plugin proposes, the operator commits.

const (
	pluginViewPickRoute = "/api/pos/plugin-view/pick"
	// pluginViewPickMaxBytes bounds the pick's form body (five short values).
	pluginViewPickMaxBytes = 4 << 10
	// overlayViewSalesPerm is the consent the seam needs: the ref returns on
	// sale.completed, which carries sales data.
	overlayViewSalesPerm = "view:sales"
)

type overlaySeamKey struct{}

// withOverlaySeam marks ctx as asking for a view drawn in the sell-screen
// overlay: the answer is validated for pluginview.SeamSellOverlay.
func withOverlaySeam(ctx context.Context) context.Context {
	return context.WithValue(ctx, overlaySeamKey{}, true)
}

func inOverlaySeam(ctx context.Context) bool {
	v, _ := ctx.Value(overlaySeamKey{}).(bool)
	return v
}

// pluginHoldsViewSales is a plain read (no audit row per overlay draw): the
// plugin is installed and active and its view:sales consent is granted.
func pluginHoldsViewSales(ctx context.Context, d *common.Deps, pluginID string) bool {
	repo := data.NewPluginRepo(d.Db)
	if _, active, err := repo.GetActivePluginVersion(ctx, pluginID); err != nil || !active {
		return false
	}
	granted, exists, err := repo.CheckPermission(ctx, pluginID, overlayViewSalesPerm)
	return err == nil && exists && granted
}

// overlayPickKey signs the pick buttons; random per process, so a button
// from before a restart is refused.
var overlayPickKey = func() []byte {
	k := make([]byte, 32)
	_, _ = rand.Read(k) // crypto/rand.Read never fails (it crashes instead)
	return k
}()

func overlayPickSig(pluginID, sku string, qty int, ref string) string {
	m := hmac.New(sha256.New, overlayPickKey)
	for _, part := range []string{pluginID, sku, strconv.Itoa(qty), ref} {
		m.Write([]byte(part))
		m.Write([]byte{0})
	}
	return hex.EncodeToString(m.Sum(nil))
}

// signOverlaySuggestions fills Plugin and Sig on every add_to_basket
// suggestion in v, so the template can draw its pick button.
func signOverlaySuggestions(v *pluginview.View, pluginID string) {
	for i := range v.Components {
		for j := range v.Components[i].Suggestions {
			s := &v.Components[i].Suggestions[j]
			if s.SKU == "" {
				continue
			}
			s.Plugin = pluginID
			s.Sig = overlayPickSig(pluginID, s.SKU, s.Qty, s.Ref)
		}
	}
}

// scanRefKey carries a pick's ref intent to the scan handler.
type scanRefKey struct{}

type scanRefIntent struct{ PluginID, Ref string }

func scanRefFrom(ctx context.Context) (scanRefIntent, bool) {
	i, ok := ctx.Value(scanRefKey{}).(scanRefIntent)
	return i, ok
}

// servePluginViewPick adds one signed overlay suggestion to the basket
// through scan. A cashier's sale action, session-gated under /api/pos/*.
func servePluginViewPick(w http.ResponseWriter, r *http.Request, d *common.Deps, scan http.HandlerFunc) {
	refuse := func(status int) { http.Error(w, http.StatusText(status), status) }
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		refuse(http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, pluginViewPickMaxBytes)
	if err := r.ParseForm(); err != nil {
		refuse(http.StatusBadRequest)
		return
	}
	pluginID := r.PostForm.Get("plugin")
	sku := r.PostForm.Get("sku")
	ref := r.PostForm.Get("ref")
	qty, err := strconv.Atoi(r.PostForm.Get("qty"))
	if err != nil || pluginID == "" || sku == "" {
		refuse(http.StatusBadRequest)
		return
	}
	want := overlayPickSig(pluginID, sku, qty, ref)
	if !hmac.Equal([]byte(r.PostForm.Get("sig")), []byte(want)) {
		refuse(http.StatusForbidden)
		return
	}
	// Revoking the consent (or removing the plugin) stops its buttons at once.
	if !pluginHoldsViewSales(r.Context(), d, pluginID) {
		refuse(http.StatusForbidden)
		return
	}
	// The scan reads only its own fields; the pick's plugin/ref/sig never
	// reach it. A pick is always the item, never a voucher/promo sharing its
	// code (ut-docs#4344).
	r.Form.Set("code", sku)
	r.Form.Set("qty", strconv.Itoa(qty))
	r.Form.Set("kind", scanKindItem)
	for _, k := range []string{"plugin", "ref", "sig", "sku"} {
		r.Form.Del(k)
		r.PostForm.Del(k)
	}
	scan(w, r.WithContext(context.WithValue(r.Context(), scanRefKey{}, scanRefIntent{PluginID: pluginID, Ref: ref})))
}
