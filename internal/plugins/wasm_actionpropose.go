package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/agentaction"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// action_propose (ADR-0153 Decision 3, ut-docs#2838): an ai.agent plugin
// asks core to make a change; core plans it and keeps it in memory for the
// operator to confirm on a core route. Nothing in the database changes
// here. Docs: architecture/wasm-runtime.md.

const actionNameMax = 128

// The operator of a ui ask (ADR-0153 "Who is asking"): core stamps it on
// the context in the page handler; the WASM runtime copies it into the
// host state only for a hop-0 ui.view.ask / ui.action.ask, never from the
// plugin's payload.
type askOperatorKey struct{}

// WithAskOperator returns ctx carrying the signed-in operator of a ui ask.
func WithAskOperator(ctx context.Context, op agentaction.Operator) context.Context {
	return context.WithValue(ctx, askOperatorKey{}, op)
}

// AskOperatorFrom returns the operator WithAskOperator stamped (a full
// one: user and session set).
func AskOperatorFrom(ctx context.Context) (agentaction.Operator, bool) {
	op, ok := ctx.Value(askOperatorKey{}).(agentaction.Operator)
	return op, ok && op.UserID != "" && op.Session != ""
}

// askSessionLiveKey carries a job's session check (ADR-0153 "Who is
// asking": a job whose session has ended is refused, ut-docs#4143).
type askSessionLiveKey struct{}

// WithAskSessionLive returns ctx carrying live, which reports whether the
// session of the ask operator on ctx is still signed in. The page layer
// stamps it, with the ask's operator, on a job it starts from a ui ask; a
// job without it gets no operator.
func WithAskSessionLive(ctx context.Context, live func(context.Context) bool) context.Context {
	return context.WithValue(ctx, askSessionLiveKey{}, live)
}

// AskSessionLiveFrom returns the session check WithAskSessionLive stamped.
func AskSessionLiveFrom(ctx context.Context) (func(context.Context) bool, bool) {
	live, ok := ctx.Value(askSessionLiveKey{}).(func(context.Context) bool)
	return live, ok && live != nil
}

// askOperatorFor is the operator the host state gets for ev, and the
// session check to run before each use (nil: none needed). A core-raised
// (hop 0) ui.view.ask / ui.action.ask has the operator the page layer
// stamped; so does a hop-0 job started from one, which must also carry a
// session check. Every other call has none.
func askOperatorFor(ctx context.Context, ev Event, isJob bool) (*agentaction.Operator, func(context.Context) bool) {
	if ev.Hop != 0 {
		return nil, nil
	}
	op, ok := AskOperatorFrom(ctx)
	if !ok {
		return nil, nil
	}
	if isJob {
		live, ok := AskSessionLiveFrom(ctx)
		if !ok {
			return nil, nil
		}
		return &op, live
	}
	if ev.Type != "ui.view.ask" && ev.Type != "ui.action.ask" {
		return nil, nil
	}
	return &op, nil
}

// askingOperator is the operator an ai.agent host call acts for, or nil
// with the reason it has none: outside an operator's ui ask, or in a job
// whose session has ended.
func (s *hostState) askingOperator(ctx context.Context) (*agentaction.Operator, string) {
	if s.operator == nil {
		return nil, "outside an operator's ui ask"
	}
	if s.operatorLive != nil && !s.operatorLive(ctx) {
		return nil, "the operator's session has ended"
	}
	return s.operator, ""
}

// hostActionPropose: action_propose(name, args, dst) writes
// {"proposal":"<id>"} per the buffer ABI. A guest that undersized dst and
// calls again with the same name and args gets the same proposal back,
// never a second one.
func hostActionPropose(ctx context.Context, m api.Module, namePtr, nameLen, argsPtr, argsLen, dstPtr, dstCap uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	if nameLen == 0 || nameLen > actionNameMax || argsLen > agentaction.MaxArgsBytes {
		return hostErrInvalid
	}
	nameB, nok := readGuest(m, namePtr, nameLen)
	argsB, aok := readGuest(m, argsPtr, argsLen)
	if !nok || !aok {
		return hostErrInvalid
	}
	key := append(append(bytes.Clone(nameB), 0), argsB...)
	if s.proposeOut != nil && bytes.Equal(s.proposeKey, key) {
		out := s.proposeOut
		if dstCap >= uint32(len(out)) {
			s.proposeKey, s.proposeOut = nil, nil
		}
		return writeGuest(m, dstPtr, dstCap, out)
	}
	out, code := actionPropose(ctx, s, string(nameB), bytes.Clone(argsB))
	if code != 0 {
		return code
	}
	if uint32(len(out)) > dstCap {
		s.proposeKey, s.proposeOut = key, out
	}
	return writeGuest(m, dstPtr, dstCap, out)
}

// actionPropose is the host function's logic. Checks, in order: no ui-ask
// operator (or a job whose session ended) → -2; installed manifest does not declare ai.agent → -2;
// unknown action → -2, like a missing grant, so a plugin without the
// grants can't enumerate action names (ut-docs#4168);
// ai.tools.write:<area> not granted → -2; the
// operator's role lacks the action's role permission → -2; the till's
// propose-time rule refuses the action (catalogue on a till that follows
// a main till, ut-docs#4168) → -2 (every -2 is audited); bad or
// over-limit arguments → -4; a proposal ceiling → -5.
func actionPropose(ctx context.Context, s *hostState, name string, args []byte) ([]byte, int32) {
	deny := func(perm, reason string) ([]byte, int32) {
		if err := auditPermissionDenial(ctx, s.db, s.pluginID, perm, reason); err != nil {
			logging.L().Warnf("failed to audit action_propose denial: %v", err)
		}
		return nil, hostErrDenied
	}
	opp, why := s.askingOperator(ctx)
	if opp == nil {
		return deny(AIAgentPermission, "action_propose "+why)
	}
	if !loadManifestFacts(ctx, s) || !s.aiAgent {
		return deny(AIAgentPermission, "action_propose needs ai.agent declared")
	}
	a, ok := agentaction.Lookup(name)
	if !ok {
		return deny(AIAgentPermission, "action_propose: no such action")
	}
	if err := CheckPermission(ctx, s.db, s.pluginID, AIToolsWritePermissionPrefix+a.Area); err != nil {
		return nil, hostErrDenied
	}
	op := *opp
	can, err := data.NewAuthRepo(s.db).HasPermission(ctx, op.Role, a.RolePermission)
	if err != nil {
		logging.L().Warnf("[wasm:%s] action_propose role check: %v", s.pluginID, err)
		return nil, hostErrInternal
	}
	if !can {
		return deny(a.RolePermission, "operator role "+op.Role+" lacks "+a.RolePermission)
	}
	if reason := agentaction.ProposeRefusal(ctx, a); reason != "" {
		return deny(AIToolsWritePermissionPrefix+a.Area, "action_propose: "+reason)
	}
	env := agentaction.Env{DB: s.db}
	parsed, rows, state, err := a.Prepare(ctx, env, args)
	switch {
	case errors.Is(err, agentaction.ErrInvalid):
		return nil, hostErrInvalid
	case err != nil:
		logging.L().Warnf("[wasm:%s] action_propose %s plan: %v", s.pluginID, name, err)
		return nil, hostErrInternal
	}
	p := agentaction.NewProposal(s.pluginID, op, a, parsed, rows, state)
	id, err := agentaction.Proposals.Put(ctx, s.db, p)
	switch {
	case errors.Is(err, agentaction.ErrQuota):
		deny(AIAgentPermission, "action_propose ceiling reached")
		return nil, hostErrQuota
	case err != nil:
		return nil, hostErrInternal
	}
	if err := agentaction.Audit(ctx, s.db, nil, "proposed", p, "pending"); err != nil {
		logging.L().Warnf("[wasm:%s] audit agent.action.proposed: %v", s.pluginID, err)
	}
	out, err := json.Marshal(map[string]string{"proposal": id})
	if err != nil {
		return nil, hostErrInternal
	}
	return out, 0
}
