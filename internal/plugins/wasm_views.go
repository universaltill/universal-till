package plugins

import (
	"context"
	"errors"
	"slices"

	"github.com/tetratelabs/wazero/api"

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
// than viewCallsPerEvent calls in this event → -5; unknown view → -1; view not in the plugin's manifest views_used, or its
// view:<class> not granted → -2, audited; arguments not a JSON object of
// in-bounds integers → -4; query error or deadline → -3; result over
// viewResultCap → -5 (never truncated).
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

// viewListed reports whether the plugin's installed manifest lists name in
// views_used. The manifest is read once per event; no readable installed
// manifest → not listed (fail closed). The view:<class> grant is still
// checked on every call, so revocation stays live.
func viewListed(ctx context.Context, s *hostState, name string) bool {
	s.viewsOnce.Do(func() {
		m, ok, err := InstalledManifest(ctx, s.db, s.pluginID)
		if err != nil {
			logging.L().Warnf("[wasm:%s] manifest unreadable, view_query refused: %v", s.pluginID, err)
			return
		}
		if ok && m != nil {
			s.viewsUsed, s.viewsOK = m.ViewsUsed, true
		}
	})
	return s.viewsOK && slices.Contains(s.viewsUsed, name)
}
