package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/devicepay"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paybroker"
)

// Payment broker host functions (ut-docs#6455, ADR-0198 §3.1/§3.2/§3.10).
//
//   - payment_provider_call(provider, op, json): the plugin's route to the
//     till's payment broker. Needs net:payment_broker, checked on EVERY call
//     (no caching: a revoke takes effect on the next call, a denial is
//     audited like any other). Only the ops the matrix gives CallerHostFn
//     (onboarding_status, session_status, session_cancel, refund) exist for
//     it; connection_token, session_create and onboarding_start are never
//     reachable here. Outside a payment dispatch core bound (paybroker.Binding)
//     it refuses with no network request; inside one, every identifier in the
//     outgoing body comes from the binding, and the answer is the neutral
//     envelope without secrets.
//   - tap_to_pay_request(json): starts the phone's tap sheet. Needs
//     device:tap_to_pay and a bound AUTHORIZE dispatch. The plugin's JSON is
//     ignored: amount, currency and key come from core's binding, core writes
//     the attempt row BEFORE the tap, and the deadline is min(120 s, the
//     dispatch's). The answer is the advisory devicepay.Result; core confirms
//     with the broker itself (pos.ConfirmTap).
//
// Both use the buffer ABI (min(len, dstCap) bytes written, full length
// returned). A broker failure is an answer, not a host error:
// {"error":{"code":"..."}} (the cloud's code or a local one,
// paybroker.ErrorCode), so a plugin can tell "could not ask" (pending) from a
// refusal. Negative codes: -2 not permitted or no bound dispatch, -3
// internal, -4 unreadable arguments or guest memory.

// tapCollectMax is the longest a tap sheet stays up (ADR-0125 §3d).
const tapCollectMax = TapGateTimeout

// errorAnswer is the JSON a plugin reads for a broker failure.
func errorAnswer(err error) []byte {
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"code": paybroker.ErrorCode(err)}})
	return b
}

// brokerAllowed checks one permission for this call: 0 to go on, else the
// code to return. A lookup failure is internal, never read as a revoke.
func brokerAllowed(ctx context.Context, s *hostState, function, perm string) int32 {
	granted, err := CheckPermissionGranted(ctx, s.db, s.pluginID, perm)
	if err != nil {
		logging.L().Errorf("[wasm:%s] %s: permission lookup failed (%v)", s.pluginID, function, err)
		return hostErrInternal
	}
	if !granted {
		return hostErrDenied
	}
	return 0
}

func hostPaymentProviderCall(ctx context.Context, m api.Module, pPtr, pLen, oPtr, oLen, rPtr, rLen, dstPtr, dstCap uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return stateErr(ctx)
	}
	if code := brokerAllowed(ctx, s, "payment_provider_call", BrokerNetPermission); code != 0 {
		return code
	}
	provider, ok1 := readGuest(m, pPtr, pLen)
	op, ok2 := readGuest(m, oPtr, oLen)
	body, ok3 := readGuest(m, rPtr, rLen)
	if !ok1 || !ok2 || !ok3 || len(body) > paybroker.MaxRequestBytes {
		return hostErrInvalid
	}
	// The binding is the one HandleEvent validated, never whatever the call
	// context happens to carry (a non-gate event, a hop, a schedule tick).
	callCtx := paybroker.WithBinding(ctx, paybroker.Binding{})
	if s.bound {
		callCtx = paybroker.WithBinding(ctx, s.binding)
	}
	out, err := paybroker.Call(callCtx, paybroker.CallerHostFn, string(provider), string(op), body)
	if err != nil {
		if errors.Is(err, paybroker.ErrNotAllowed) || errors.Is(err, paybroker.ErrNoDispatch) {
			// Refused before any request: the plugin is not entitled to this.
			logging.L().Warnf("[wasm:%s] payment_provider_call %s/%s refused: %s", s.pluginID, provider, op, paybroker.ErrorCode(err))
			return hostErrDenied
		}
		return writeGuest(m, dstPtr, dstCap, errorAnswer(err))
	}
	return writeGuest(m, dstPtr, dstCap, out)
}

func hostTapToPayRequest(ctx context.Context, m api.Module, _, _, dstPtr, dstCap uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return stateErr(ctx)
	}
	if code := brokerAllowed(ctx, s, "tap_to_pay_request", TapDevicePermission); code != 0 {
		return code
	}
	b := s.binding
	if !s.bound || b.Kind != paybroker.BindAuthorize || b.AttemptKey == "" || b.Provider == "" || b.AmountMinor <= 0 {
		return hostErrDenied
	}
	res, err := CollectTap(ctx, s.db, b)
	if err != nil {
		if errors.Is(err, errTapDenied) {
			return hostErrDenied
		}
		logging.L().Errorf("[wasm:%s] tap_to_pay_request: %v", s.pluginID, err)
		return hostErrInternal
	}
	out, err := json.Marshal(res)
	if err != nil {
		return hostErrInternal
	}
	return writeGuest(m, dstPtr, dstCap, out)
}

var errTapDenied = errors.New("tap payment refused")

// CollectTap is what tap_to_pay_request does for a core-bound authorize
// dispatch: write the attempt row BEFORE the tap (so a crash, a restart or a
// lost reply still leaves something the till can settle against the
// provider), then collect on the device, or on the paired phone reader the
// binding names (ut-docs#6538). The answer is the advisory devicepay.Result;
// core confirms with the broker itself afterwards.
func CollectTap(ctx context.Context, db *sql.DB, b paybroker.Binding) (devicepay.Result, error) {
	deadline := time.Now().Add(tapCollectMax)
	if d, has := ctx.Deadline(); has && d.Before(deadline) {
		deadline = d
	}
	_, err := data.NewPaymentAttemptRepo(db).Begin(ctx, data.PaymentAttempt{
		Key: b.AttemptKey, Provider: b.Provider, AmountMinor: b.AmountMinor, Currency: b.Currency, Deadline: deadline,
	})
	if err != nil {
		if errors.Is(err, data.ErrAttemptSettled) || errors.Is(err, data.ErrAttemptMismatch) || errors.Is(err, data.ErrAttemptInvalid) {
			return devicepay.Result{}, errTapDenied
		}
		return devicepay.Result{}, fmt.Errorf("attempt row: %w", err)
	}
	tapCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if b.ReaderRef != "" {
		tapCtx = devicepay.WithReader(tapCtx, b.ReaderRef)
	}
	return devicepay.Collect(tapCtx, devicepay.Request{
		Provider: b.Provider, AmountMinor: b.AmountMinor, Currency: strings.ToLower(b.Currency), IdempotencyKey: b.AttemptKey,
	}), nil
}
