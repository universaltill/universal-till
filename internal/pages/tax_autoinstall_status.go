package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ADR-0172 §5 (ut-docs#6287): the automatic tax install is surfaced, not
// silent. This file holds its status (installing / installed / retrying with
// an error class), the chip that shows it on the page the setup wizard lands
// on and in Settings, and the "declined" record an uninstall or dismiss
// leaves so the retry never reinstalls it.

const (
	taxStateInstalling = "installing"
	taxStateInstalled  = "installed"
	taxStateRetrying   = "retrying"

	taxErrOffline   = "offline"
	taxErrNotListed = "not_listed"
	taxErrRefused   = "refused"
)

// taxAutoInstallStatus is the persisted state of this till's automatic tax
// install (one per till).
type taxAutoInstallStatus struct {
	Market     string // the shop's country the install is for
	State      string // taxState*
	ErrorClass string `json:",omitempty"` // taxErr*, while retrying
	PluginID   string `json:",omitempty"` // known once installed
	PluginName string `json:",omitempty"` // display name from the catalogue
}

func loadTaxAutoInstallStatus(ctx context.Context, d *common.Deps) *taxAutoInstallStatus {
	if d == nil || d.Settings == nil {
		return nil
	}
	raw, ok, err := d.Settings.Get(ctx, common.KeyTaxAutoInstallStatus)
	if err != nil || !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	var st taxAutoInstallStatus
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		logging.L().Warnf("tax auto-install: status unreadable, ignoring: %v", err)
		return nil
	}
	return &st
}

// saveTaxAutoInstallStatus persists st; nil clears it (no chip).
func saveTaxAutoInstallStatus(ctx context.Context, d *common.Deps, st *taxAutoInstallStatus) {
	ctx = context.WithoutCancel(ctx)
	val := ""
	if st != nil {
		b, err := json.Marshal(st)
		if err != nil {
			return
		}
		val = string(b)
	}
	if err := d.Settings.Set(ctx, common.KeyTaxAutoInstallStatus, val); err != nil {
		logging.L().Warnf("tax auto-install: persist status: %v", err)
	}
}

// isAutoTaxSpec: the spec the automatic path queues (a tax spec naming a
// market the operator did not choose from the prompted tile).
func isAutoTaxSpec(s basePluginSpec) bool {
	return s.CanonicalType == "tax" && s.Market != "" && !s.Prompted
}

// taxErrorClass maps an attempt's failure to the chip's error class. ok=false
// means the failure says nothing about the install (a cancelled or timed-out
// attempt: the state stays as it was).
func taxErrorClass(err error) (class string, ok bool) {
	switch {
	case err == nil:
		return "", false
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "", false
	case errors.Is(err, errBasePluginRefused):
		return taxErrRefused, true
	case errors.Is(err, errBasePluginNotPublished):
		return taxErrNotListed, true
	case errors.Is(err, errBasePluginCatalogUnreachable):
		return taxErrOffline, true
	case strings.Contains(err.Error(), "status 404"):
		// Listed in the catalogue but the marketplace no longer serves it.
		return taxErrNotListed, true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return taxErrOffline, true
	}
	return taxErrRefused, true // a bundle the till would not accept (signature, checksum, compatibility)
}

// recordTaxAutoInstallFailure moves the chip to "retrying" with the failure's
// class, keeping what is already known (name, market).
func recordTaxAutoInstallFailure(ctx context.Context, d *common.Deps, spec basePluginSpec, err error) {
	class, ok := taxErrorClass(err)
	if !ok {
		return
	}
	st := loadTaxAutoInstallStatus(ctx, d)
	if st == nil || st.State == taxStateInstalled {
		st = &taxAutoInstallStatus{}
	}
	st.Market = strings.ToUpper(spec.Market)
	st.State = taxStateRetrying
	st.ErrorClass = class
	saveTaxAutoInstallStatus(ctx, d, st)
}

// recordTaxAutoInstallInstalling notes the chosen listing while the install
// runs, so the chip can name it.
func recordTaxAutoInstallInstalling(ctx context.Context, d *common.Deps, spec basePluginSpec, name string) {
	st := &taxAutoInstallStatus{Market: strings.ToUpper(spec.Market), State: taxStateInstalling, PluginName: name}
	saveTaxAutoInstallStatus(ctx, d, st)
}

// recordTaxAutoInstallDone marks the install finished: installed with its
// plugin id (read from the install-status record of its listing), or no chip
// at all when nothing was installed (listingID "").
func recordTaxAutoInstallDone(ctx context.Context, d *common.Deps, spec basePluginSpec, listingID, name string) {
	if listingID == "" {
		saveTaxAutoInstallStatus(ctx, d, nil)
		return
	}
	st := &taxAutoInstallStatus{Market: strings.ToUpper(spec.Market), State: taxStateInstalled, PluginName: name}
	if rec, had, err := plugins.NewInstallStatusStore(d.Db).Get(ctx, listingID); err == nil && had {
		st.PluginID = rec.PluginID
		if rec.PluginName != "" {
			st.PluginName = rec.PluginName
		}
	}
	saveTaxAutoInstallStatus(ctx, d, st)
}

// taxAutoInstallDeclined returns the declined record ("" = none).
func taxAutoInstallDeclined(ctx context.Context, d *common.Deps) string {
	v, _, err := d.Settings.Get(ctx, common.KeyTaxAutoInstallDeclined)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// declineTaxAutoInstall is what an uninstall of the automatically installed
// plugin, or a dismiss of its pending row, does (ADR-0172 §5): the pending
// spec goes, the plugin id (or, before one is known, the market) is recorded
// as declined so nothing reinstalls it, and the chip is cleared. Reinstalling
// is a normal marketplace install.
func declineTaxAutoInstall(ctx context.Context, d *common.Deps, pluginID string) {
	ctx = context.WithoutCancel(ctx)
	st := loadTaxAutoInstallStatus(ctx, d)
	declined := pluginID
	if declined == "" && st != nil {
		declined = st.PluginID
	}
	if declined == "" && st != nil {
		declined = "market:" + st.Market
	}
	if declined == "" {
		declined = "declined"
	}
	if err := d.Settings.Set(ctx, common.KeyTaxAutoInstallDeclined, declined); err != nil {
		logging.L().Errorf("tax auto-install: record declined: %v", err)
	}
	if pending, err := loadPendingBasePlugins(ctx, d); err == nil {
		var drop []basePluginSpec
		for _, s := range pending {
			if isAutoTaxSpec(s) {
				drop = append(drop, s)
			}
		}
		if err := removePendingBasePlugins(ctx, d, drop); err != nil {
			logging.L().Errorf("tax auto-install: drop pending spec: %v", err)
		}
		for _, s := range drop {
			setBasePluginNotPublished(ctx, d, s, false)
		}
	}
	saveTaxAutoInstallStatus(ctx, d, nil)
}

// taxAutoInstalledPlugin reports whether pluginID is the tax plugin this
// till installed automatically (the status record names it), and for which
// market.
func taxAutoInstalledPlugin(ctx context.Context, d *common.Deps, pluginID string) (market string, ok bool) {
	st := loadTaxAutoInstallStatus(ctx, d)
	if st == nil || st.State != taxStateInstalled || st.PluginID == "" || st.PluginID != pluginID {
		return "", false
	}
	return st.Market, true
}

// taxChipView is what the chip partial renders. Text is the full sentence
// (the glyph is decoration), ErrorText the second line while retrying.
type taxChipView struct {
	State      string
	ErrorClass string
	Text       string
	ErrorText  string
	Poll       bool // installing / retrying: the partial re-polls itself
}

func taxAutoInstallChipView(ctx context.Context, d *common.Deps, locale string) *taxChipView {
	st := loadTaxAutoInstallStatus(ctx, d)
	if st == nil || st.State == "" {
		return nil
	}
	name := st.PluginName
	if name == "" {
		name = httpx.T(locale, "setup.tax_autoinstall.plugin_fallback_name")
	}
	v := &taxChipView{State: st.State, ErrorClass: st.ErrorClass}
	switch st.State {
	case taxStateInstalled:
		v.Text = sprintfKey(locale, "setup.tax_autoinstall.installed", name)
	case taxStateRetrying:
		v.Text = sprintfKey(locale, "setup.tax_autoinstall.retrying", name)
		switch st.ErrorClass {
		case taxErrOffline, taxErrNotListed, taxErrRefused:
			v.ErrorText = httpx.T(locale, "setup.tax_autoinstall.error."+st.ErrorClass)
		}
		v.Poll = true
	default:
		v.State = taxStateInstalling
		v.Text = sprintfKey(locale, "setup.tax_autoinstall.installing", name)
		v.Poll = true
	}
	return v
}

// registerTaxAutoInstallChip: GET /ui/tax-autoinstall-chip, the chip's own
// poll while it is installing or retrying. Empty 200 when there is nothing to
// say or the viewer may not open Settings (a cashier never sees this).
func registerTaxAutoInstallChip(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/tax-autoinstall-chip", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		var v *taxChipView
		if canPerform(d, r, "settings") {
			v = taxAutoInstallChipView(r.Context(), d, locale)
		}
		if v == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		httpx.RenderPartial("ui/partials/tax_autoinstall_chip.html", v)(w, r)
	})
}

// taxPluginForCountry is the one tax entry ADR-0172 adds to setup's pending
// list: the shop's country, resolved against the catalogue's `markets` when
// it is attempted. It names no country and no plugin. Not queued when the
// operator already declined an automatic install on this till.
func taxPluginForCountry(ctx context.Context, d *common.Deps, country string) []basePluginSpec {
	market := strings.ToUpper(strings.TrimSpace(country))
	if market == "" {
		return nil
	}
	if taxAutoInstallDeclined(ctx, d) != "" {
		return nil
	}
	return []basePluginSpec{{CanonicalType: "tax", Market: market}}
}

// countryDisplayName is the shop country's name in the viewer's language,
// by the same locale key the wizard's country tiles use; the bare code when
// the country has no row.
func countryDisplayName(ctx context.Context, d *common.Deps, locale, code string) string {
	cs, ok, err := data.NewCountrySettingsRepo(d.Db).Get(ctx, code)
	if err != nil || !ok || cs.NameKey == "" {
		return code
	}
	if n := httpx.T(locale, cs.NameKey); n != "" && n != cs.NameKey {
		return n
	}
	return code
}

// sprintfKey formats the translation of key with args.
func sprintfKey(locale, key string, args ...any) string {
	return fmt.Sprintf(httpx.T(locale, key), args...)
}
