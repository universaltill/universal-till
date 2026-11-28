package pages

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
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

// Tills & devices (ut-docs#7057, restructured by ut-docs#7173): the sections
// of the Settings tree. Each live section is a lazy fragment,
// GET /ui/tills/section/{id}, loaded into its Settings card the first time
// the card is shown (the page itself makes no network call for them,
// ADR-0003). The old tabbed /tills page is gone: GET /tills answers a 303
// into Settings (tillsRedirectTarget) and GET /ui/tills/tab/{id} serves the
// same fragments for one more release. Every section keeps the gate the page
// always had (sync_management).

// tillsSectionIDs are the fragment ids; "old" shares the account view with
// "others", "sync" is the Sync and network part of Shop and sync.
var tillsSectionIDs = []string{"this", "others", "old", "add", "readers", "sync"}

func validTillsSection(id string) bool {
	for _, t := range tillsSectionIDs {
		if t == id {
			return true
		}
	}
	return false
}

// tillsTabIDs are the legacy tab ids: the only values ?tab= and
// /ui/tills/tab/{id} accept.
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
	if d.Cfg != nil {
		m["ShopConnected"] = enroll.CredentialsComplete(enroll.Effective(d.Cfg).Marketplace)
	}
	m["IsMain"] = asString(m["SyncPrimary"]) == ""
	m["ShopName"] = shopNameOrEmpty(ctx, d)
	m["ThisSentenceKey"] = tillsSentenceKey(m)
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
		old := oldEntriesFrom(o)
		m["Old"] = old
		m["OldCount"] = len(old)
		m["OldBanner"] = oldBannerFrom(old, locale, httpx.T(locale, "tills.tree.and"))
		m["OldRemoveURLs"] = oldRemoveURLs(old)
	case "add":
		tillsAddData(r, m)
	case "settings":
		tillsSettingsData(ctx, d, m)
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
	// ut-docs#7173: the role tag (Main till / Additional till / Satellite).
	roleKey := "settings.tills.role_main"
	switch role {
	case "":
	case data.TillRoleSatellite:
		roleKey = "tills.role.satellite"
	default:
		roleKey = "tills.role.replica"
	}
	m["ThisRoleLabel"] = httpx.T(locale, roleKey)
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
	// ut-docs#7173: device ID and key fingerprint, behind "Technical details".
	m["TechFacts"] = registerFactsView(r, d, locale)
}

// tillsNeedFromQuery is ?need= when it names a chooser option, else "".
func tillsNeedFromQuery(r *http.Request) string {
	switch need := r.URL.Query().Get("need"); need {
	case "another", "join", "replace":
		return need
	}
	return ""
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

// tillsSectionTab maps a fragment id to the data tab its view is built from.
func tillsSectionTab(id string) string {
	switch id {
	case "old":
		return "others"
	case "sync":
		return "settings"
	}
	return id
}

// tillsSectionTemplate is the partial a fragment id renders.
func tillsSectionTemplate(id string) string {
	switch id {
	case "others":
		return "tills_section_others.html"
	case "old":
		return "tills_section_old.html"
	case "add":
		return "tills_tab_add.html"
	case "readers":
		return "tills_tab_readers.html"
	case "sync":
		return "tills_tab_settings.html"
	}
	return "tills_tab_this.html"
}

// tillsTabSection maps a legacy ?tab= value to its section card id in
// Settings (the card ids are the tree's anchors).
func tillsTabSection(tab string) string {
	switch tab {
	case "others":
		return "settings-tills-others"
	case "add":
		return "settings-tills-add"
	case "readers":
		return "settings-tills-readers"
	case "settings":
		return "settings-tills-shop"
	}
	return "settings-tills-this"
}

// tillsRedirectTarget is where an old /tills URL goes: the same function in
// the Settings tree, query kept where the new section reads it (?need= for
// Add a till, ?refresh=1 for Other tills). A #fragment never reaches the
// server, so the old per-tab anchors (#account, #network, #kiosk, #notices)
// land on the tab's own section; the page's script maps them itself when a
// hash is present on /settings.
func tillsRedirectTarget(r *http.Request) string {
	tab := tillsTabFromRequest(r)
	q := url.Values{}
	switch tab {
	case "add":
		switch need := r.URL.Query().Get("need"); need {
		case "another", "join", "replace":
			q.Set("need", need)
		}
	case "others":
		if r.URL.Query().Get("refresh") == "1" {
			q.Set("refresh", "1")
		}
	}
	out := "/settings"
	if len(q) > 0 {
		out += "?" + q.Encode()
	}
	return out + "#" + tillsTabSection(tab)
}

// registerTillsTabs wires the section fragments, their legacy tab route and
// the bulk removal of stale tills. GET /tills itself (a redirect) stays in
// registerSyncAPI.
func registerTillsTabs(mux *http.ServeMux, d *common.Deps) {
	serve := func(w http.ResponseWriter, r *http.Request, section string) {
		if !canPerform(d, r, "sync_management") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		m, err := tillsPageData(w, r, d, tillsSectionTab(section))
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "tills_section", err)
			return
		}
		m["Section"] = section
		m["SectionTemplate"] = tillsSectionTemplate(section)
		httpx.RenderBlock("ui/pages/tills_sections.html", "tills_fragment", m)(w, r)
	}
	mux.HandleFunc("GET /ui/tills/section/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validTillsSection(id) {
			http.NotFound(w, r)
			return
		}
		serve(w, r, id)
	})
	// One more release: an open page or a cached link may still ask for a tab.
	mux.HandleFunc("GET /ui/tills/tab/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validTillsTab(id) {
			http.NotFound(w, r)
			return
		}
		if id == "settings" {
			id = "sync"
		}
		serve(w, r, id)
	})
	registerTillsRevokeStale(mux, d)
	registerTillsOldEntries(mux, d)
}

// tillsTabFromRequest reads ?tab= (never trusted beyond the fixed list).
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
