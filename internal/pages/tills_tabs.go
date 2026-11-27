package pages

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/tills"
)

// The one "Tills & devices" page (ut-docs#7057): GET /tills?tab=… renders
// the whole page, GET /ui/tills/tab/{id} the same tab as a fragment (HTMX
// enhancement; the tab bar is made of real links, so it works without JS).
// Every tab keeps the gate the page always had (sync_management).

// tillsTabIDs is the tab order, and the only values ?tab= accepts.
var tillsTabIDs = []string{"this", "others", "add", "readers", "settings"}

func validTillsTab(id string) bool {
	for _, t := range tillsTabIDs {
		if t == id {
			return true
		}
	}
	return false
}

// tillsStaleAfter is how long an unseen till must have been quiet for its
// "not in your account" row to read "safe to remove".
const tillsStaleAfter = 24 * time.Hour

// fleetCache keeps the last account list for a minute: the Other tills tab
// would otherwise make a network call on every poll, and the tab bar's
// counts must never wait on the network.
var fleetCache struct {
	mu      sync.Mutex
	at      time.Time
	devices []enroll.Device
	ok      bool
}

const (
	fleetCacheTTL     = time.Minute
	fleetFetchTimeout = 3 * time.Second
)

// tillsFleetFetch is a var so tests can stand in for the marketplace.
var tillsFleetFetch = func(ctx context.Context, d *common.Deps) ([]enroll.Device, error) {
	return enroll.Fleet(ctx, d.Cfg)
}

// accountFleet returns the account's till list and whether it could be read.
// refresh bypasses the cache (the "Try again" button). Never blocks longer
// than fleetFetchTimeout; any failure is "unknown", never "empty" (ADR-0003).
func accountFleet(ctx context.Context, d *common.Deps, refresh bool) ([]enroll.Device, bool) {
	fleetCache.mu.Lock()
	defer fleetCache.mu.Unlock()
	if !refresh && !fleetCache.at.IsZero() && time.Since(fleetCache.at) < fleetCacheTTL {
		return fleetCache.devices, fleetCache.ok
	}
	if d.Cfg == nil {
		return nil, false
	}
	cctx, cancel := context.WithTimeout(ctx, fleetFetchTimeout)
	defer cancel()
	devs, err := tillsFleetFetch(cctx, d)
	fleetCache.at, fleetCache.devices, fleetCache.ok = time.Now(), devs, err == nil
	if err != nil {
		fleetCache.devices = nil
	}
	return fleetCache.devices, fleetCache.ok
}

// cachedAccountFleet is accountFleet without ever fetching.
func cachedAccountFleet() ([]enroll.Device, bool) {
	fleetCache.mu.Lock()
	defer fleetCache.mu.Unlock()
	return fleetCache.devices, fleetCache.ok && !fleetCache.at.IsZero()
}

// tillsOthersRows is the merged "Other tills" list and the counts the page
// needs from it.
type tillsOthers struct {
	Rows         []tills.Row
	CloudOK      bool
	Disagree     int // rows whose state is not ok (excluding this till)
	Stale        []tills.Row
	StaleLong    []tills.Row // stale AND unseen past the window: the only ones the bulk remove offers
	CloudOnly    []tills.Row
	CanRemove    bool
	FirstStaleID string
}

func tillsOthersView(ctx context.Context, d *common.Deps, m map[string]any, refresh, fetch bool) (tillsOthers, error) {
	var out tillsOthers
	var devs []enroll.Device
	if fetch {
		devs, out.CloudOK = accountFleet(ctx, d, refresh)
	} else {
		devs, out.CloudOK = cachedAccountFleet()
	}
	list, err := data.NewTillsRepo(d.Db).ListTills(ctx)
	if err != nil {
		return out, err
	}
	selfID, _ := discovery.ReportedTillID(ctx, d.Settings)
	self := tills.SelfInfo{ID: selfID, Name: deviceNameOrDefault(ctx, d, httpx.DefaultLocale()), Role: tills.RoleMain}
	var src tills.Source
	if sp, _ := m["SyncPrimary"].(string); sp != "" {
		self.Role = ownTillRole(ctx, d)
	}
	src = tills.Source{Cloud: devs, CloudOK: out.CloudOK, Local: list, Self: self}
	if rep, ok := m["Replaced"].(*enroll.Replaced); ok {
		src.Replaced = rep
	}
	out.Rows = tills.Merge(src, time.Now(), tillsStaleAfter)
	for _, r := range out.Rows {
		if r.IsSelf {
			continue
		}
		switch r.State {
		case tills.StateLocalOnly, tills.StateOldEntry:
			out.Stale = append(out.Stale, r)
			if r.LongUnseen {
				out.StaleLong = append(out.StaleLong, r)
			}
			out.Disagree++
		case tills.StateCloudOnly:
			out.CloudOnly = append(out.CloudOnly, r)
			out.Disagree++
		case tills.StateRoleMismatch:
			out.Disagree++
		}
	}
	if len(out.Stale) > 0 {
		out.FirstStaleID = out.Stale[0].ID
	}
	return out, nil
}

func asString(v any) string { s, _ := v.(string); return s }

// tillsPageData is everything the page's templates read, for tab.
func tillsPageData(w http.ResponseWriter, r *http.Request, d *common.Deps, tab string) (map[string]any, error) {
	ctx := r.Context()
	m, err := tillsRosterData(ctx, d, w, r)
	if err != nil {
		return nil, err
	}
	locale := httpx.RequestLocale(r)
	// ut-docs#2722: the main till has stopped answering this replica.
	lv := replicaLinkView(ctx, d)
	m["MainUnreachable"] = lv.State == linkUnreachable
	m["MainUnreachableSince"] = lv.Since
	m["NeedsRejoin"] = lv.State == linkRejoin
	// ut-docs#4507: joining replaces this till's sales too.
	m["HasSales"] = hasSalesHistory(ctx, d)
	if isJoinedTill(ctx, d) {
		m["IsJoinedTill"] = true
		m["UnsentSales"] = countUnsentSales(ctx, d)
	}
	rep, replaced := tillReplaced(r, d)
	if replaced {
		m["Replaced"] = &rep
	}
	m["IsReplaced"] = replaced
	m["ReplacedBy"] = rep.By
	// ADR-0222: the same sentence and one button as the status-bar chip.
	hst := healChipStatus()
	if hv, show := healChip(locale, hst, storeHasOwner(ctx, d.Settings)); show {
		m["Heal"] = hv
	}
	if !hst.LastHealedAt.IsZero() {
		m["LastReconnected"] = fmt.Sprintf(httpx.T(locale, "tills.this.last_reconnected"), httpx.FormatDateTime(hst.LastHealedAt.Local(), locale))
	}
	m["Tab"] = tab
	m["TabIDs"] = tillsTabIDs
	m["IsMain"] = asString(m["SyncPrimary"]) == ""
	m["ShopName"] = shopNameOrEmpty(ctx, d)
	m["ThisSentenceKey"] = tillsSentenceKey(m)
	// The count tags on the tab bar come from what is already known: the
	// pending queue (a local read) and the cached account list. Neither
	// waits on the network.
	if m["IsMain"].(bool) {
		if list, perr := data.NewPairingRepo(d.Db).ListPendingReadOnly(ctx); perr == nil {
			n := 0
			for _, p := range list {
				if p.RequestedRole != data.TillRoleCardReader {
					n++
				}
			}
			m["PendingCount"] = n
		}
	}
	switch tab {
	case "this":
		tillsThisData(ctx, d, r, m, locale)
	case "others":
		o, oerr := tillsOthersView(ctx, d, m, r.URL.Query().Get("refresh") == "1", true)
		if oerr != nil {
			return nil, oerr
		}
		m["Others"] = o
		m["OthersCount"] = o.Disagree
		if d.SyncPrimaryURL(ctx) == "" { // ut-docs#7098: optional, default off, throttled
			maybeAutoRetireRegisters(ctx, d, o.Rows, o.CloudOK)
		}
	case "add":
		tillsAddData(r, m)
	case "settings":
		tillsSettingsData(ctx, d, m)
	}
	if tab != "others" {
		if o, oerr := tillsOthersView(ctx, d, m, false, false); oerr == nil && o.CloudOK {
			m["OthersCount"] = o.Disagree
		}
	}
	return m, nil
}

// tillsSentenceKey picks the one sentence the This till tab opens with.
func tillsSentenceKey(m map[string]any) string {
	switch {
	case m["IsReplaced"] == true:
		return "tills.this.sentence.replaced"
	case m["IsMain"] == true:
		if asString(m["ShopName"]) == "" && len(asTills(m["Tills"])) == 0 {
			return "tills.this.sentence.alone"
		}
		return "tills.this.sentence.main"
	case m["MainUnreachable"] == true && m["ThisRole"] == data.TillRoleSatellite:
		return "tills.this.sentence.unreachable_satellite"
	case m["MainUnreachable"] == true:
		return "tills.this.sentence.unreachable_additional"
	}
	return ""
}

func asTills(v any) []tillRosterRow { r, _ := v.([]tillRosterRow); return r }

func tillsThisData(ctx context.Context, d *common.Deps, r *http.Request, m map[string]any, locale string) {
	role := ownTillRole(ctx, d)
	m["ThisRole"] = role
	m["TillName"] = deviceNameOrDefault(ctx, d, locale)
	tillRegisterID := ""
	if resolved, err := pos.ResolveTillRegisterID(ctx, d.Db, d.Settings); err == nil {
		tillRegisterID = resolved
	}
	m["TillRegisterID"] = tillRegisterID
	// The Registers link is offered only to a session /registers admits.
	m["CanManageRegisters"] = canPerform(d, r, "stock_location_management")
	regs, err := data.NewPOSRepo(d.Db).ListRegisters(ctx)
	if err != nil {
		logging.L().Errorf("tills: list registers: %v", err)
	}
	m["registers"] = regs
	m["MainTillAddr"] = ""
	if role != "" {
		m["MainTillAddr"] = mainTillHostPort(d.SyncPrimaryURL(ctx))
	}
	if key := tillsSentenceKey(m); key != "" {
		m["ThisSentenceKey"] = key
	} else if role == data.TillRoleSatellite {
		m["ThisSentenceKey"] = "tills.this.sentence.satellite"
	} else {
		m["ThisSentenceKey"] = "tills.this.sentence.additional"
	}
	m["ThisSentence"] = tillsSentence(m, locale)
	// A till that fixes itself says so in the status sentence (no banner, no
	// button); the other states carry their sentence in the banner below.
	if hv, ok := m["Heal"].(healView); ok && hv.Waiting {
		m["ThisSentence"] = hv.Sentence
	}
	if d.Cfg != nil {
		m["ShopConnected"] = enroll.CredentialsComplete(enroll.Effective(d.Cfg).Marketplace)
	}
}

// tillsAddData: which chooser option is selected. ?need=another|join|replace;
// the default is "another" on a main till and "join" on one that has no shop.
func tillsAddData(r *http.Request, m map[string]any) {
	need := r.URL.Query().Get("need")
	switch need {
	case "another", "join", "replace":
	default:
		need = "another"
		if m["IsMain"] != true || m["IsReplaced"] == true {
			need = "join"
		}
		if m["IsMain"] == true && len(asTills(m["Tills"])) == 0 && m["ShopName"] == "" {
			need = "join" // a fresh standalone till is most often joining
		}
	}
	m["Need"] = need
}

func tillsSettingsData(ctx context.Context, d *common.Deps, m map[string]any) {
	if m["IsMain"] != true {
		return
	}
	n, err := data.NewPOSRepo(d.Db).CountJournalQuarantine(ctx)
	if err != nil {
		logging.L().Errorf("tills: count quarantine: %v", err)
	}
	m["QuarantineCount"] = n
	m["ShowQuarantine"] = n > 0 || len(asTills(m["Tills"])) > 0
}

// registerTillsTabs wires the page shell's fragment route and the bulk
// removal of stale tills. GET /tills itself stays in registerSyncAPI.
func registerTillsTabs(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/tills/tab/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		id := r.PathValue("id")
		if !validTillsTab(id) {
			http.NotFound(w, r)
			return
		}
		m, err := tillsPageData(w, r, d, id)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "tills_tab", err)
			return
		}
		m["OOB"] = true // the tab bar rides along, swapped out of band
		httpx.RenderBlock("ui/pages/tills.html", "tills_fragment", m)(w, r)
	})
	registerTillsRevokeStale(mux, d)
}

// tillsTabFromRequest reads ?tab= (also the #fragment-style ids the old
// anchors used, never trusted beyond the fixed list).
func tillsTabFromRequest(r *http.Request) string {
	if t := r.URL.Query().Get("tab"); validTillsTab(t) {
		return t
	}
	return "this"
}

// tillsSentence is the one sentence the This till tab opens with.
func tillsSentence(m map[string]any, locale string) string {
	key, _ := m["ThisSentenceKey"].(string)
	if key == "" {
		return ""
	}
	shop := asString(m["ShopName"])
	if shop == "" {
		shop = httpx.T(locale, "tills.this.shop_fallback")
	}
	mainName := asString(m["PrimaryTillName"])
	switch key {
	case "tills.this.sentence.main", "tills.this.sentence.additional":
		return fmt.Sprintf(httpx.T(locale, key), shop)
	case "tills.this.sentence.satellite", "tills.this.sentence.unreachable_satellite", "tills.this.sentence.unreachable_additional":
		return fmt.Sprintf(httpx.T(locale, key), mainName)
	}
	return httpx.T(locale, key)
}
