package plugins

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/agentaction"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// view_query (ADR-0121 §5, ut-docs#3158): the only way a plugin reads core
// data. The views themselves — names, view:<class> permissions, argument
// bounds, the 5 s deadline — live in internal/data (core_views.go); this is
// the host-side gate. Contract: ut-docs reference/contracts/plugin-views.md.

// viewResultCap is the largest JSON result view_query returns; a var so a
// test can lower it.
var viewResultCap = data.CoreViewMaxResult

const (
	viewNameMax = 128     // a versioned view name, e.g. sales.by_day.v1
	viewArgsMax = 4 << 10 // the arguments object
	// viewCallsPerEvent caps view_query calls in one event (ADR-0121 §3:
	// every call counts against the plugin's quota); past it: -5.
	viewCallsPerEvent = 64
)

// hostViewQuery runs one core read view for the plugin and writes its JSON
// result (an array) into dst per the buffer ABI. Checks, in order: more
// than viewCallsPerEvent calls in this event → -5; unknown view → -1; view
// not in the plugin's manifest views_used, or its view:<class> not granted
// → -2, audited; for a plugin whose installed manifest declares ai.agent
// (ADR-0153 Decision 1), the view's data class not granted as
// ai.send.<class>, or the view has no class → -2, audited (shop-class views
// need no ai.send.* grant); for such a plugin too (agentReadAllowed,
// ut-docs#4143): no ui-ask operator, or a job whose session ended → -2;
// past the request's tool-call ceiling → -5; the operator's role lacks the
// view's data-class permission → -2 (each audited); arguments not a JSON object of in-bounds
// integers → -4; query error or deadline → -3; result over viewResultCap →
// -5 (never truncated). Every grant is checked on every call, so a revoke
// is live.
//
// A guest that undersized dst and calls again re-runs the query: views are
// read-only, so the retry has no side effect (unlike http_request's cache).
func hostViewQuery(ctx context.Context, m api.Module, namePtr, nameLen, argsPtr, argsLen, dstPtr, dstCap uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	s.viewCalls++
	if s.viewCalls > viewCallsPerEvent {
		return hostErrQuota
	}
	if nameLen == 0 || nameLen > viewNameMax || argsLen > viewArgsMax {
		return hostErrInvalid
	}
	nameB, nok := readGuest(m, namePtr, nameLen)
	argsB, aok := readGuest(m, argsPtr, argsLen)
	if !nok || !aok {
		return hostErrInvalid
	}
	name := string(nameB)
	view, ok := data.LookupCoreView(name)
	if !ok {
		return hostErrNotFound
	}
	if !viewListed(ctx, s, name) {
		if err := auditPermissionDenial(ctx, s.db, s.pluginID, view.Permission, "view "+name+" not listed in manifest views_used"); err != nil {
			logging.L().Warnf("failed to audit view denial: %v", err)
		}
		return hostErrDenied
	}
	if err := CheckPermission(ctx, s.db, s.pluginID, view.Permission); err != nil {
		return hostErrDenied
	}
	if !aiDataClassAllowed(ctx, s.db, s.pluginID, s.aiAgent, view) {
		return hostErrDenied
	}
	if code := agentReadAllowed(ctx, s, view); code != 0 {
		return code
	}
	args, err := view.ParseArgs(argsB)
	if err != nil {
		return hostErrInvalid
	}
	out, err := data.RunCoreView(ctx, s.db, view, args, viewResultCap)
	switch {
	case errors.Is(err, data.ErrCoreViewTooLarge):
		return hostErrQuota
	case err != nil:
		logging.L().Warnf("[wasm:%s] view %s failed: %v", s.pluginID, name, err)
		return hostErrInternal
	}
	return writeGuest(m, dstPtr, dstCap, out)
}

// aiDataClassAllowed is ADR-0153 Decision 1's second gate. A plugin that
// does not declare ai.agent is unaffected. For one that does, a view with
// no data class is refused (fail closed, audited), a shop-class view is
// allowed, and any other class needs ai.send.<class> — CheckPermission
// audits a not-declared or not-granted denial.
func aiDataClassAllowed(ctx context.Context, db *sql.DB, pluginID string, aiAgent bool, view data.CoreView) bool {
	if !aiAgent {
		return true
	}
	switch view.DataClass {
	case "":
		if err := auditPermissionDenial(ctx, db, pluginID, AIAgentPermission, "view "+view.Name+" has no AI data class"); err != nil {
			logging.L().Warnf("failed to audit view denial: %v", err)
		}
		return false
	case data.DataClassShop:
		return true
	}
	return CheckPermission(ctx, db, pluginID, AISendPermissionPrefix+view.DataClass) == nil
}

// agentReadAllowed is ADR-0153's operator rule for an ai.agent plugin's
// view_query (ut-docs#4143); 0 = allowed, else the host code. A plugin
// that does not declare ai.agent is unaffected. Otherwise the call needs
// the operator of a ui ask (or of a live job started from one: "Who is
// asking"), counts against that request's tool-call ceiling (Decision 6,
// one chat turn across its events and jobs), and needs the role
// permission of the view's data class (Decision 1, "The operator's role
// still applies to reads"; a cashier gets no sales, staff or till-health
// view). Every refusal is audited.
func agentReadAllowed(ctx context.Context, s *hostState, view data.CoreView) int32 {
	if !s.aiAgent {
		return 0
	}
	deny := func(perm, reason string) {
		if err := auditPermissionDenial(ctx, s.db, s.pluginID, perm, reason); err != nil {
			logging.L().Warnf("failed to audit view denial: %v", err)
		}
	}
	op, why := s.askingOperator(ctx)
	if op == nil {
		deny(AIAgentPermission, "view_query "+why)
		return hostErrDenied
	}
	if !agentaction.ToolCalls.Take(*op) {
		deny(AIAgentPermission, "view_query: tool-call ceiling of one request reached")
		return hostErrQuota
	}
	perm, ok := data.AIReadRolePermission(view.DataClass)
	if !ok {
		deny(AIAgentPermission, "view "+view.Name+": no operator role rule for data class "+view.DataClass)
		return hostErrDenied
	}
	if perm == "" {
		return 0
	}
	can, err := data.NewAuthRepo(s.db).HasPermission(ctx, op.Role, perm)
	if err != nil {
		logging.L().Warnf("[wasm:%s] view_query role check: %v", s.pluginID, err)
		return hostErrInternal
	}
	if !can {
		deny(perm, "view "+view.Name+": operator role "+op.Role+" lacks "+perm)
		return hostErrDenied
	}
	return 0
}

// viewListed reports whether the plugin's installed manifest lists name in
// views_used. No readable installed manifest → not listed (fail closed).
// The view:<class> and ai.send.<class> grants are still checked on every
// call, so revocation stays live.
func viewListed(ctx context.Context, s *hostState, name string) bool {
	return loadManifestFacts(ctx, s) && slices.Contains(s.viewsUsed, name)
}

// loadManifestFacts reads the plugin's installed manifest once per event
// and records its views_used and whether it declares ai.agent. It reports
// false when no installed manifest is readable; callers fail closed. A
// manifest swapped mid-event is seen from the next event on: events are
// short and every grant is re-checked per call.
func loadManifestFacts(ctx context.Context, s *hostState) bool {
	s.viewsOnce.Do(func() {
		m, ok, err := InstalledManifest(ctx, s.db, s.pluginID)
		if err != nil {
			logging.L().Warnf("[wasm:%s] manifest unreadable, view_query refused: %v", s.pluginID, err)
			return
		}
		if ok && m != nil {
			s.viewsUsed, s.viewsOK = m.ViewsUsed, true
			s.aiAgent = slices.Contains(m.Permissions, AIAgentPermission)
		}
	})
	return s.viewsOK
}
