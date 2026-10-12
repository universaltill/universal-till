package pages

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/universaltill/universal-till/internal/agentaction"
	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pluginview"
	"github.com/universaltill/universal-till/internal/pos"
)

// The AI-agent write gate's core half (ADR-0153 Decision 3, ut-docs#2838):
// the operator stamp on a ui ask, the confirm card a plugin's view names
// by proposal id, and the core routes its buttons post to. A plugin can
// neither word the card nor reach these routes; everything is re-checked
// at confirm, and the batch is applied in one transaction.

const agentActionResultEvent = "agent.action.result"

// agentResultTimeout bounds the best-effort result event to the plugin.
var agentResultTimeout = 5 * time.Second

// withAgentOperator stamps the signed-in operator of a ui ask on ctx
// (plugins.WithAskOperator); the WASM runtime hands it to action_propose
// only for a hop-0 ui.view.ask / ui.action.ask. Never from the payload.
func withAgentOperator(ctx context.Context) context.Context {
	u, ok := auth.FromContext(ctx)
	session := auth.SessionKey(ctx)
	if !ok || u.ID == "" || session == "" {
		return ctx
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ctx
	}
	return plugins.WithAskOperator(ctx, agentaction.Operator{UserID: u.ID, Role: u.Role, Session: session, Request: hex.EncodeToString(b[:])})
}

// resolveAgentConfirms fills v's confirm cards for the signed-in operator;
// only pluginID's own proposals resolve in its view.
func resolveAgentConfirms(ctx context.Context, d *common.Deps, v *pluginview.View, pluginID, locale string) {
	u, _ := auth.FromContext(ctx)
	session := auth.SessionKey(ctx)
	v.ResolveConfirms(func(id string) *pluginview.ViewConfirm {
		p := agentaction.Proposals.Peek(ctx, d.Db, id, u.ID, session)
		if p == nil || p.PluginID != pluginID {
			return nil
		}
		return agentConfirmCard(p, locale)
	})
}

func agentConfirmCard(p *agentaction.Proposal, locale string) *pluginview.ViewConfirm {
	c := &pluginview.ViewConfirm{
		ID:           p.ID,
		Title:        httpx.T(locale, p.Action.LabelKey),
		ConfirmLabel: fmt.Sprintf(httpx.T(locale, "agent.confirm.button"), httpx.FormatDecimal(strconv.Itoa(len(p.Rows)), locale)),
	}
	for _, r := range p.Rows {
		c.Rows = append(c.Rows, pluginview.ViewConfirmRow{
			Item: r.Item, Field: httpx.T(locale, r.FieldKey),
			Before: agentValue(r.Before, locale), After: agentValue(r.After, locale),
		})
	}
	return c
}

func agentValue(v agentaction.Value, locale string) string {
	switch v.Kind {
	case "text":
		return v.Text
	case "money":
		return httpx.FormatMoneyIn(v.Minor, httpx.ActiveCurrency().Code, locale)
	case "number":
		return httpx.FormatDecimal(strconv.FormatFloat(v.Number, 'f', -1, 64), locale)
	}
	return "—"
}

// agentNotice is the fragment that replaces a confirm card.
type agentNotice struct {
	Level, Key string
	Count      string
}

func registerAgentProposals(mux *http.ServeMux, d *common.Deps) {
	agentaction.SetStockLocationResolver(func(ctx context.Context, db *sql.DB) (string, error) {
		return posResolveTillStockLocation(ctx, d, db)
	})
	mux.HandleFunc("POST /ui/agent/proposals/{id}/confirm", func(w http.ResponseWriter, r *http.Request) {
		agentProposalDecide(w, r, d, true)
	})
	mux.HandleFunc("POST /ui/agent/proposals/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		agentProposalDecide(w, r, d, false)
	})
}

// agentProposalDecide consumes the proposal (single use) and, on confirm,
// re-checks the ai.agent declaration, the ai.tools.write:<area> grant, the
// operator's role permission and that the values are still current, then
// applies the batch in one transaction with the confirmed audit row.
func agentProposalDecide(w http.ResponseWriter, r *http.Request, d *common.Deps, confirm bool) {
	ctx := r.Context()
	locale := httpx.ResolveLocale(w, r)
	render := func(status int, n agentNotice) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		httpx.RenderWith(pluginViewFiles, httpx.FuncsFor(locale))("agent_notice", n)(w, r)
	}
	// Cross-site posts never carry the SameSite=Lax session cookie; a
	// browser that says so outright is refused before anything else.
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		render(http.StatusForbidden, agentNotice{Level: "warn", Key: "agent.notice.refused"})
		return
	}
	u, ok := auth.FromContext(ctx)
	p := agentaction.Proposals.Take(ctx, d.Db, r.PathValue("id"), u.ID, auth.SessionKey(ctx))
	if !ok || p == nil {
		render(http.StatusConflict, agentNotice{Level: "warn", Key: "agent.notice.invalid"})
		return
	}
	if !confirm {
		auditAgent(ctx, d, nil, "cancelled", p, "cancelled")
		render(http.StatusOK, agentNotice{Level: "info", Key: "agent.notice.cancelled"})
		notifyAgentPlugin(ctx, d, p, "cancelled")
		return
	}
	if reason := agentConfirmRefusal(ctx, d, p, u); reason != "" {
		auditAgent(ctx, d, nil, "refused", p, reason)
		render(http.StatusForbidden, agentNotice{Level: "warn", Key: "agent.notice.refused"})
		notifyAgentPlugin(ctx, d, p, "refused")
		return
	}
	res, err := p.Action.Apply(ctx, agentaction.Env{DB: d.Db}, p.Args(), p.State(), u.ID, func(tx *sql.Tx, res agentaction.Result) error {
		return agentaction.Audit(ctx, d.Db, tx, "confirmed", p, fmt.Sprintf("applied %d", res.Changes))
	})
	switch {
	case errors.Is(err, agentaction.ErrStale):
		auditAgent(ctx, d, nil, "refused", p, "stale")
		render(http.StatusConflict, agentNotice{Level: "warn", Key: "agent.notice.stale"})
		notifyAgentPlugin(ctx, d, p, "stale")
		return
	case err != nil:
		logging.L().Warnf("agent action %s (%s) failed: %v", p.Action.Name, p.PluginID, err)
		auditAgent(ctx, d, nil, "refused", p, "failed")
		render(http.StatusInternalServerError, agentNotice{Level: "error", Key: "agent.notice.failed"})
		notifyAgentPlugin(ctx, d, p, "failed")
		return
	}
	// The manual screens' after-commit steps: mirror stock changes to
	// inventory connectors and nudge linked tills (ADR-0114 §2). A new
	// item needs no nudge: its row insert's trigger already does it, as
	// for the catalog screen.
	for _, s := range res.Stock {
		publishStockAdjusted(ctx, d, plugins.StockAdjustedEvent{ItemID: s.ItemID, DeltaQty: s.Delta, Reason: stockMovementReason("adjust"), Location: s.LocationID})
	}
	if len(res.Stock) > 0 {
		d.NudgeLink(fleetlink.ScopeStock)
	}
	render(http.StatusOK, agentNotice{Level: "info", Key: "agent.notice.applied", Count: httpx.FormatDecimal(strconv.Itoa(res.Changes), locale)})
	notifyAgentPlugin(ctx, d, p, "applied")
}

// agentConfirmRefusal re-checks everything a proposal needed: "" when it
// may still be applied, else the audited reason.
func agentConfirmRefusal(ctx context.Context, d *common.Deps, p *agentaction.Proposal, u auth.User) string {
	m, ok, err := plugins.InstalledManifest(ctx, d.Db, p.PluginID)
	if err != nil {
		return "manifest unreadable"
	}
	if !ok || m == nil || !slices.Contains(m.Permissions, plugins.AIAgentPermission) {
		return "ai.agent not declared"
	}
	if err := plugins.CheckPermission(ctx, d.Db, p.PluginID, plugins.AIToolsWritePermissionPrefix+p.Action.Area); err != nil {
		return "ai.tools.write:" + p.Action.Area + " not granted"
	}
	if d.AuthSvc == nil {
		return "no role check"
	}
	if !agentRoleAllows(ctx, d, u, p.Action.RolePermission) {
		return "role lacks " + p.Action.RolePermission
	}
	if p.Action.Area == "catalog" && d.SyncPrimaryURL(ctx) != "" {
		// A till that follows a main till forwards catalogue saves there;
		// an agent change is never forwarded.
		return "catalogue is edited on the main till"
	}
	return ""
}

// agentRoleAllows is the manual screen's role check for an action's role
// permission. Each arm names its permission literally, so the permission
// page guard sees every action this route gates on; an unknown one is
// refused.
func agentRoleAllows(ctx context.Context, d *common.Deps, u auth.User, perm string) bool {
	var can bool
	var err error
	switch perm {
	case "catalog_management":
		can, err = d.AuthSvc.Can(ctx, u, "catalog_management")
	case "stock_management":
		can, err = d.AuthSvc.Can(ctx, u, "stock_management")
	default:
		return false
	}
	return err == nil && can
}

func auditAgent(ctx context.Context, d *common.Deps, tx *sql.Tx, event string, p *agentaction.Proposal, result string) {
	if err := agentaction.Audit(ctx, d.Db, tx, event, p, result); err != nil {
		logging.L().Warnf("audit agent.action.%s: %v", event, err)
	}
}

// notifyAgentPlugin tells the proposing plugin the outcome, best-effort:
// never blocks the response, never fails it.
func notifyAgentPlugin(ctx context.Context, d *common.Deps, p *agentaction.Proposal, status string) {
	payload := map[string]any{"proposal": p.ID, "action": p.Action.Name, "status": status}
	go func() {
		defer logging.RecoverAndLog("pages.agentActionResult")
		nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentResultTimeout)
		defer cancel()
		if _, _, err := plugins.SharedBus(d.Db).AskPlugin(nctx, p.PluginID, agentActionResultEvent, payload); err != nil {
			logging.L().Infof("agent.action.result to %s: %v", p.PluginID, err)
		}
	}()
}

// posResolveTillStockLocation is the manual goods-in screen's location:
// this till's register's, else the shop's default.
func posResolveTillStockLocation(ctx context.Context, d *common.Deps, db *sql.DB) (string, error) {
	return pos.ResolveStockLocationID(ctx, db, tillRegisterIDBestEffort(ctx, d))
}
