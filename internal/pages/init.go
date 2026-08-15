package pages

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/ai"
	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/issuereport"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/catalog"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/ui"
	"github.com/universaltill/universal-till/web/locales"
)

// Init builds the page mux and returns the *common.Deps instance it wired
// every handler with, so app.Run can join Deps.AsyncWork (best-effort
// print/kitchen/invoice goroutines started after a request already
// responded, ut-docs#425) into its own shutdown drain — without this, Deps
// was constructed and kept entirely inside Init, and Run's deferred
// database.Close() had no way to wait for those goroutines to actually
// finish touching Db first (ut-docs#513). bgCtx and wg belong to app.Run's
// background-service drain. bgCtx (not ctx) is load-bearing for anything
// joined on wg:
// app.Run's stopBg() cancels ONLY bgCtx, so a wg-joined loop hung off ctx
// would never be signalled on the return paths where the caller's ctx is
// still live (an early startup error, server.Start's own bind failure) and
// the drain would eat its full 10s timeout on every shutdown —
// app.TestRun_JoinsBackgroundGoroutinesOnEarlyServerError gates exactly this.
// StartCloudSync, StartSyncPush, StartSyncPull, StartEODScheduler and
// StartAutoUpdateScheduler are all wired this way now (ut-docs#153).
// internal/plugins.Supervisor's monitorProcess goroutines and the wasm
// runtime's per-plugin event-channel drainer are joined too now, though not
// both the same way (ut-docs#380, #503). Supervisor.Shutdown waits on its
// own internal WaitGroup and IS reached via this wg (server.Start's
// wg-tracked shutdown goroutine calls it). Manager.Close is deliberately
// NOT called from that same goroutine: it closes the same subscriber
// channels EventBus.publish sends on, and racing that close against a
// still-live publisher on this wg (cloudsync's ticker, in particular) is a
// real "send on closed channel" panic (ut-docs#503). app.Run calls
// Manager.Close from its own deferred cleanup instead, strictly after this
// wg's drain has joined everything, publishers included.
func Init(ctx, bgCtx context.Context, cfg *config.Config, pm *plugins.Manager, db *sql.DB, catalogRepo *marketplace.CatalogRepository, wg *sync.WaitGroup) (http.Handler, *common.Deps) {
	log := logging.L()
	mux := http.NewServeMux()

	// Point the plugin page/theme/icon asset dirs at the resolved data
	// directory (paths.Init ran in config.Init). These stay package vars so
	// tests can override them, but production reads them from the stable
	// data dir.
	pluginPagesDir = paths.Plugins()
	pluginThemesDir = paths.Plugins()
	pluginIconsDir = paths.Plugins()
	issuereport.PendingDir = paths.Data("issue-reports", "pending")

	// settings + state
	setStore := settings.NewStore(db)
	state := common.LoadState(ctx, setStore, cfg)
	// Ensure defaults are persisted (e.g., theme). Non-fatal on error
	// (offline-first: a boot must never be blocked on a settings write) —
	// the in-memory state above is already correct for this boot either way.
	// Pass the RuntimeState LoadState just resolved, not a hand-copied
	// subset of its fields — a prior partial literal here silently dropped
	// IdleLockMinutes/KioskIdleResetSeconds (ut-docs#177), disabling
	// auto-lock and kiosk idle-reset from the second boot onward.
	// MUST run before the UT_UI_SCALE resolution below: state.UIScale is
	// still whatever LoadState found in the store (0 if never configured),
	// so SaveState's own `> 0` guard (state.go) skips writing it here —
	// this is what keeps the env-derived provisioning scale from being
	// persisted per-till (see the rederiveSettings comment further down).
	// Moving this call after the UIScale resolution would silently start
	// persisting UT_UI_SCALE as if a manager had set it.
	if err := common.SaveState(ctx, setStore, state); err != nil {
		log.Errorf("persist default settings: %v", err)
	}

	// i18n / currency
	i18n, err := config.NewI18nFS(locales.FS, cfg.Locales.Locale)
	if err != nil {
		log.Fatalf("failed to load locales: %v", err)
	}
	httpx.InitI18n(i18n, cfg.Locales.Locale)
	pm.SetLocalizer(i18n) // language-pack plugins merge into the translator
	// Shop translation overrides (manager edits) win over base + plugin
	// strings; loaded once here, refreshed by the /translations editor.
	if overrides, err := data.NewTranslationRepo(db).ListOverrides(ctx); err == nil {
		i18n.SetShopOverrides(overrides)
	}
	httpx.InitCurrency(state.Currency)
	// Dedicated till: larger touch targets, no text selection (UT_KIOSK=1).
	httpx.InitKiosk(os.Getenv("UT_KIOSK") == "1")
	// Interface scale: the saved setting wins; UT_UI_SCALE env is the
	// provisioning default for a till that hasn't been configured yet.
	if state.UIScale <= 0 {
		state.UIScale = 1
		if v := strings.TrimSpace(os.Getenv("UT_UI_SCALE")); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
				state.UIScale = f
			}
		}
	}
	httpx.InitUIScale(state.UIScale)
	httpx.InitOSKMode(state.OSKMode)

	btnStore := ui.NewButtonStore(db)
	resolver := ui.PriceResolverAdapter{Store: btnStore}
	engine := pos.NewServiceWithResolver(pos.Config{
		TaxInclusive:                 state.TaxInclusive,
		TaxRateBasisPoints:           state.TaxRatePct * 100,
		ServiceChargeRateBasisPoints: state.ServiceChargeRateBasisPoints,
	}, resolver)
	// Country-specific tax rules (e.g. Germany's dine-in/takeaway VAT
	// switch) are entirely a plugin's call — core has no built-in opinion,
	// see pluginTaxRateAsker. ONE shared asker instance for both engines
	// below: it's self-locked and its cache is keyed only by
	// (ItemID, TaxCodeID, TaxRateBP, OrderType) — nothing basket-specific —
	// so sharing is safe and reuses the cache.
	taxAsker := &pluginTaxRateAsker{db: db}
	engine.SetTaxRateAsker(taxAsker)
	// The self-order kiosk gets its OWN engine (ut-docs#449) — see
	// common.Deps.KioskEngine for why this must never be the same instance
	// the cashier's handlers use. The resolver is a stateless read-only
	// adapter, safe to share.
	kioskEngine := pos.NewServiceWithResolver(pos.Config{
		TaxInclusive:                 state.TaxInclusive,
		TaxRateBasisPoints:           state.TaxRatePct * 100,
		ServiceChargeRateBasisPoints: state.ServiceChargeRateBasisPoints,
	}, resolver)
	kioskEngine.SetTaxRateAsker(taxAsker)

	// Labels are locale keys; the nav renders them through T (unknown keys —
	// e.g. plugin menu labels from manifests — pass through unchanged).
	baseMenu := []common.MenuItem{
		{Href: "/", Label: "nav.home"},
		{Href: "/designer", Label: "nav.designer"},
		{Href: "/inventory", Label: "nav.inventory"},
		{Href: "/shifts", Label: "nav.shifts"},
		{Href: "/journal", Label: "nav.journal"},
		{Href: "/orders", Label: "nav.orders"},
		{Href: "/reports", Label: "nav.reports"},
		{Href: "/settings", Label: "nav.settings"},
		{Href: "/plugins", Label: "nav.plugins"},
		{Href: "/catalog", Label: "nav.catalog"},
	}

	// One auth service for the whole till: login, sessions AND manager-PIN
	// approvals share a single device-wide lockout.
	authSvc := auth.NewService(db)
	authDisabled := auth.Disabled(os.Getenv("UT_AUTH"))
	// Idle auto-lock (docs: pos-auth.md): server-side check + audit hook.
	// The cosmetic client timer (data-idle-lock) is only published when the
	// middleware actually enforces sessions.
	authSvc.SetIdleLockMinutes(state.IdleLockMinutes)
	idleAuditRepo := data.NewPOSRepo(db)
	authSvc.SetIdleLockAudit(func(ctx context.Context, userID string) {
		_ = idleAuditRepo.InsertAudit(ctx, nil, userID, "user", userID, "idle_lock", nil,
			time.Now().UTC().Format(time.RFC3339), "")
	})
	if !authDisabled {
		httpx.InitIdleLock(state.IdleLockMinutes)
	}

	// Assistive AI (camera identify). No UT_AI_API_KEY → disabled and
	// invisible; never on the checkout path (ADR-0003).
	// AI resolves PER REQUEST via aiService (docs: ai-plugin.md): the
	// marketplace AI plugin's settings first, env as the dev override,
	// otherwise invisible. Deps.AI stays nil here — it is a test seam.
	if ai.New(ai.FromEnv()).Enabled() {
		log.Infof("AI env override present (UT_AI_*) — plugin settings take precedence when the AI plugin is active")
	}

	dp := &common.Deps{
		Cfg:         cfg,
		Pm:          pm,
		Db:          db,
		Settings:    setStore,
		State:       state,
		BaseMenu:    baseMenu,
		Menu:        common.BuildMenu(baseMenu, pm),
		Engine:      engine,
		KioskEngine: kioskEngine,
		BtnStore:    btnStore,
		CatalogRepo: catalogRepo,
		AuthSvc:     authSvc,
		// Order-status pub/sub (ut-docs#526): one instance for the process —
		// the one-tap endpoint publishes, future KDS/pager surfaces subscribe.
		OrderStatus: pos.NewOrderStatusBroadcaster(),
		WindowCtl:   common.NoopWindowController{},
	}

	// Register routes
	registerStatic(mux)
	registerIndex(mux, dp)
	registerDesigner(mux, dp)
	registerSettings(mux, dp)
	registerThemes(mux, dp)
	registerPluginIcons(mux)
	registerPluginsPage(mux, dp)
	registerPluginAPI(mux, dp)
	registerPluginPages(mux, dp)
	registerButtonsAPI(mux, dp)
	registerPOSAPI(mux, dp)
	registerFiscalAPI(mux, dp)       // German TSE hard-gate owner override (ADR-0048)
	registerPOSModifiersAPI(mux, dp) // item customization step, ADR-0020
	registerAIAPI(mux, dp)
	registerAskAPI(mux, dp)
	registerPrintAPI(mux, dp)
	registerKitchenPrintAPI(mux, dp)
	registerBackupAPI(mux, dp)
	registerDataAPI(mux, dp)
	registerEODAPI(mux, dp)
	registerReportArchiveAPI(mux, dp) // ADR-0040 card 1: report retention mode + archive export
	registerImport(mux, dp)
	registerReceiptDesigner(mux, dp)
	registerPluginSettings(mux, dp)
	enrolTokens := registerSyncAPI(mux, dp)
	registerPairingAPI(mux, dp, authSvc, enrolTokens) // ADR-0033 part 2/3
	registerDiscoveryAPI(mux, dp)                     // ADR-0033 part 1/3
	registerPairingJoinAPI(mux, dp)                   // ADR-0033 part 3/3 (replica side)
	registerPendingPairingsUI(mux, dp)                // ADR-0033 part 3/3 (primary side)
	registerSyncSales(mux, dp)
	registerSyncAdmin(mux, dp)
	registerSyncAssets(mux, dp)
	StartSyncPush(bgCtx, dp, wg) // replica journal loop (ADR-0011 D3); joined by app.Run's drain
	// Re-derive everything Init computed from settings — same moves as the
	// settings handlers make on a manual edit. Shared by the replica drift
	// loop (ADR-0011 D2b) and cloud set_setting directives (ADR-0018).
	rederiveSettings := func(c context.Context) {
		st := common.LoadState(c, setStore, cfg)
		applied := dp.UpdateState(func(s *common.RuntimeState) {
			// display.ui_scale is per-till; when unset keep the env-derived
			// value Init resolved instead of LoadState's zero.
			if st.UIScale <= 0 {
				st.UIScale = s.UIScale
			}
			*s = st
		})
		httpx.InitCurrency(applied.Currency)
		// In-place tax swap: replacing the engine (as the settings
		// handlers do) would empty the basket of a sale in progress.
		// Both engines: the kiosk's separate instance (ut-docs#449) must
		// see the same tax config or a kiosk checkout would silently
		// charge stale rates after a settings drift.
		if newCfg := (pos.Config{
			TaxInclusive:                 applied.TaxInclusive,
			TaxRateBasisPoints:           applied.TaxRatePct * 100,
			ServiceChargeRateBasisPoints: applied.ServiceChargeRateBasisPoints,
		}); dp.Engine.Config() != newCfg {
			dp.Engine.SetConfig(newCfg)
			dp.KioskEngine.SetConfig(newCfg)
		}
		authSvc.SetIdleLockMinutes(applied.IdleLockMinutes)
		if !authDisabled {
			httpx.InitIdleLock(applied.IdleLockMinutes)
		}
		if overrides, err := data.NewTranslationRepo(dp.Db).ListOverrides(c); err == nil {
			i18n.SetShopOverrides(overrides)
		}
		// The replica drift loop / cloud directives can rewrite
		// plugin_settings rows (sync_admin ApplyAdmin) without a plugin
		// reload — cached ".ask" answers must not survive that
		// (ut-docs#222 review finding).
		plugins.SharedBus(dp.Db).BumpGeneration()
	}
	StartSyncPull(bgCtx, dp, rederiveSettings, wg)  // joined by app.Run's drain
	StartCloudSync(bgCtx, dp, rederiveSettings, wg) // ADR-0018 cloud heartbeat + directives; joined by app.Run's drain
	StartEODScheduler(bgCtx, dp, wg)                // background Z-report (docs: G30); joined by app.Run's drain
	StartAutoUpdateScheduler(bgCtx, dp, wg)         // background unattended update (ut-docs#79); joined by app.Run's drain
	StartBasePluginRetry(bgCtx, dp, wg)             // retry country base-plugin auto-install while offline (ut-docs#591); joined by app.Run's drain
	StartFiscalSignRetry(bgCtx, dp, wg)             // re-sign sales declared unsigned by fiscal.sign.ask (ADR-0044 proceed-and-declare, ut-docs#675); joined by app.Run's drain
	registerInvoices(mux, dp)                       // VAT invoices + credit notes (G31)
	registerHoldAPI(mux, dp)
	registerSuggestions(mux, dp)
	registerInventoryAPI(mux, dp)
	registerInventoryPage(mux, dp)
	registerShiftsAPI(mux, dp)
	registerShiftsPage(mux, dp)
	registerReportsPage(mux, dp)
	registerBackofficePage(mux, dp)  // manager dashboard (ADR-0018 back-office home)
	registerSelfOrder(mux, dp)       // self-order kiosk shell, auth-exempt (ADR-0020)
	registerSelfOrderShop(mux, dp)   // kiosk browse/search/customize/cart, auth-exempt (ADR-0020 Phase 3)
	registerAuditPage(mux, dp)       // manager-only audit-trail browse/filter page
	registerIssueReportPage(mux, dp) // manager-only "report an issue" capture (ADR-0022)
	registerMyReportsPage(mux, dp)   // manager-only sent-report tracking list (ut-docs#348)
	registerHelp(mux, dp)
	registerUpdateAPI(mux, dp)
	registerMenu(mux, dp)
	catalog.Register(mux, dp)
	registerBasket(mux, dp)
	registerJournal(mux, dp)
	registerOrderStatus(mux, dp) // order lifecycle status one-tap surface (ut-docs#526)
	registerHealth(mux)
	registerExternalProxy(mux, dp)
	registerPluginStore(mux, dp) // Marketplace plugin store
	registerMarketplaceV1Stub(mux, dp)

	// Operator PIN login (docs: architecture/pos-auth.md). UT_AUTH=off is
	// the CI/dev-tooling escape hatch; a real till runs with auth on.
	registerAuth(mux, dp, authSvc)
	registerRefund(mux, dp, authSvc)
	registerUsers(mux, dp, authSvc)
	registerPermissionSettings(mux, dp) // super_admin-only role→action grant matrix editor (ut-docs#556)
	registerLocations(mux, dp)
	registerPromotions(mux, dp)      // promo-code admin: create/edit/deactivate/list (ut-docs#634)
	registerKitchenStations(mux, dp) // kitchen station routing (ut-docs#516)
	registerCountrySettings(mux, dp) // per-country defaults (ut-docs#659)
	registerTranslations(mux, dp, i18n)
	registerSetup(mux, dp, authSvc)
	if authDisabled {
		log.Warnf("UT_AUTH=off — operator login disabled")
		return mux, dp
	}
	return auth.Middleware(mux, authSvc), dp
}
