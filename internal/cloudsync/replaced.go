package cloudsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
)

// ReplacedSignal is a replaced till's 401 data (ut-docs#5058, design C6):
// By is the successor's fleet-row name ("" until it has checked in), At the
// RFC 3339 instant of the replacement. Both are cloud text, clamped when
// recorded (enroll.MarkReplaced).
type ReplacedSignal struct {
	By, At string
}

// RevokeSignal is a 401 device_revoked's data (ut-docs#5058, ADR-0217 §2.3):
// the machine-readable Reason (replaced, moved, removed, store_deleted,
// pair_abandoned; "" from an older cloud), Recover when the cloud says this
// revocation is healable, and Detail, the display text of replaced_by or
// moved_to ("" until the successor has checked in). At is replaced_at.
type RevokeSignal struct {
	Reason  string
	Recover bool
	Detail  string
	At      string
}

// parseRevokeSignal reads {error:{code:"device_revoked"}, data:{reason,
// recover, replaced_at, replaced_by, moved_to}} from an already-read 401
// body. Any other code is nil. The body is cut at cloudErrorMaxBytes first,
// like parseCloudErrorEnvelope.
func parseRevokeSignal(body []byte) *RevokeSignal {
	if len(body) > cloudErrorMaxBytes {
		body = body[:cloudErrorMaxBytes]
	}
	var env struct {
		Data struct {
			Reason     string `json:"reason"`
			Recover    bool   `json:"recover"`
			ReplacedAt string `json:"replaced_at"`
			ReplacedBy string `json:"replaced_by"`
			MovedTo    string `json:"moved_to"`
		} `json:"data"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil || env.Error.Code != AuthCodeDeviceRevoked {
		return nil
	}
	sig := &RevokeSignal{Reason: env.Data.Reason, Recover: env.Data.Recover, At: env.Data.ReplacedAt}
	switch env.Data.Reason {
	case "replaced":
		sig.Detail = env.Data.ReplacedBy
	case "moved":
		sig.Detail = env.Data.MovedTo
	}
	return sig
}

// parseReplacedSignal is the replaced case of parseRevokeSignal: nil for any
// other code or reason (a plain removal).
func parseReplacedSignal(body []byte) *ReplacedSignal {
	sig := parseRevokeSignal(body)
	if sig == nil || sig.Reason != "replaced" {
		return nil
	}
	return &ReplacedSignal{By: sig.Detail, At: sig.At}
}

// applyBody fills a 401's statusError from its body.
func (e *statusError) applyBody(body []byte) {
	e.Code, _ = parseCloudErrorEnvelope(body)
	e.Replaced = parseReplacedSignal(body)
	if sig := parseRevokeSignal(body); sig != nil {
		e.Reason, e.Recover, e.Detail = sig.Reason, sig.Recover, sig.Detail
	}
}

// recordReplaced persists the replaced state when a tick's error carries the
// signal. On the first such answer, not after ADR-0116 D6's three: the
// replacement is the owner's decision, final, and this till must stop acting
// as main till on the shop network at once (design T2, ADR-0117 amendment).
func recordReplaced(ctx context.Context, db *sql.DB, tickErr error) {
	var se *statusError
	if db == nil || !errors.As(tickErr, &se) || se.StatusCode != http.StatusUnauthorized || se.Replaced == nil {
		return
	}
	// Not cancelled with a shutting-down tick: the first answer sticks.
	enroll.MarkReplaced(context.WithoutCancel(ctx), data.NewSettingsRepo(db), se.Replaced.By, se.Replaced.At)
}
