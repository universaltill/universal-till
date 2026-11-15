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

// parseReplacedSignal reads {error:{code:"device_revoked"}, data:{reason:
// "replaced", replaced_at, replaced_by}} from an already-read 401 body. Any
// other code or reason (a plain removal) is nil. The body is cut at
// cloudErrorMaxBytes first, like parseCloudErrorEnvelope.
func parseReplacedSignal(body []byte) *ReplacedSignal {
	if len(body) > cloudErrorMaxBytes {
		body = body[:cloudErrorMaxBytes]
	}
	var env struct {
		Data struct {
			Reason     string `json:"reason"`
			ReplacedAt string `json:"replaced_at"`
			ReplacedBy string `json:"replaced_by"`
		} `json:"data"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil || env.Error.Code != AuthCodeDeviceRevoked || env.Data.Reason != "replaced" {
		return nil
	}
	return &ReplacedSignal{By: env.Data.ReplacedBy, At: env.Data.ReplacedAt}
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
