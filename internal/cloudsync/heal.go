package cloudsync

// The till side of ADR-0217 in this package: every cloud call's 401 envelope
// reaches the heal engine (internal/credheal), refusals are logged once per
// change, and the engine's probe is a check-in GET.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/credheal"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
)

const (
	syncPath    = "/v1/stores/sync"
	checkinPath = "/v1/stores/checkin"
)

// reportRefusal tells the heal engine a caller's call was refused with this
// status and envelope code. Only a 401 with an envelope code counts: a 403 is
// an authorisation answer for a credential the cloud knows, and a 401 with no
// code (a proxy, an older cloud, catalogpull's "http_401") is no evidence.
func reportRefusal(source string, status int, code string) {
	reportRefusalFull(source, status, code, "", "", false)
}

func reportRefusalFull(source string, status int, code, reason, detail string, recover bool) {
	if status != http.StatusUnauthorized {
		return
	}
	switch code {
	case AuthCodeUnauthorized, AuthCodeTokenRetired, AuthCodeDeviceRevoked:
	default:
		return
	}
	credheal.Report(credheal.Signal{Source: source, Code: code, Reason: reason, Detail: detail, Recover: recover})
}

// reportTickRefusal is the tick's own evidence: the check-in GET and the sync
// POST are what the probe would do, so the engine skips the probe for it
// (FromCheckin). A tick that really reached the cloud tells the engine the
// credential works.
func reportTickRefusal(tickErr error, contacted bool) {
	if tickErr == nil {
		if contacted {
			credheal.Working()
		}
		return
	}
	var se *statusError
	if !errors.As(tickErr, &se) || se.StatusCode != http.StatusUnauthorized {
		return
	}
	switch se.Code {
	case AuthCodeUnauthorized, AuthCodeTokenRetired, AuthCodeDeviceRevoked:
		src := "sync"
		if se.Path == checkinPath {
			src = "checkin"
		}
		credheal.Report(credheal.Signal{Source: src, Code: se.Code, Reason: se.Reason, Detail: se.Detail, Recover: se.Recover, FromCheckin: true})
	default:
		logging.L().Infof("cloudsync: %s answered 401 with no error code; not treated as a dead credential", se.Path)
	}
}

// ProbeCredential is the engine's probe: one check-in GET with the current
// credential. OK for 200/304 (and a 402, which is an authenticated answer);
// the 401's code, reason and hint otherwise; Err when it could not tell
// (transport, 429, 503, anything unexpected).
func ProbeCredential(ctx context.Context, cfg *config.Config) credheal.ProbeResult {
	m := enroll.Effective(cfg).Marketplace
	if m.StoreID == "" || m.MerchantToken == "" {
		return credheal.ProbeResult{Err: errors.New("cloudsync: no credential to probe")}
	}
	status, _, err := getCheckin(ctx, m.EndpointURL, m.StoreID, m.MerchantToken, false, 0, nil)
	var se *statusError
	switch {
	case errors.As(err, &se):
		switch se.StatusCode {
		case http.StatusUnauthorized:
			return credheal.ProbeResult{Code: se.Code, Reason: se.Reason, Recover: se.Recover}
		case http.StatusPaymentRequired:
			return credheal.ProbeResult{OK: true}
		}
		return credheal.ProbeResult{Err: err}
	case err != nil:
		return credheal.ProbeResult{Err: err}
	case status == 0, status == http.StatusOK, status == http.StatusNotModified:
		return credheal.ProbeResult{OK: true}
	}
	return credheal.ProbeResult{Err: fmt.Errorf("cloudsync: probe answered %d", status)}
}

// ---- refusal logging (AC17) --------------------------------------------------------

var refusalLog struct {
	sync.Mutex
	last map[string]string
}

func resetRefusalLog() {
	refusalLog.Lock()
	refusalLog.last = nil
	refusalLog.Unlock()
}

// noteRefusal logs a refused till call (path, status, code, reason: never the
// token) once per change of that tuple for the path, so a refusal on every
// tick is one line, and a changed answer is a new one.
func noteRefusal(path string, status int, code, reason string) {
	key := fmt.Sprintf("%d|%s|%s", status, code, reason)
	refusalLog.Lock()
	if refusalLog.last == nil {
		refusalLog.last = map[string]string{}
	}
	same := refusalLog.last[path] == key
	refusalLog.last[path] = key
	refusalLog.Unlock()
	if same {
		return
	}
	line := fmt.Sprintf("cloudsync: %s refused: status %d, code %s", path, status, orNoCode(code))
	if reason != "" {
		line += ", reason " + reason
	}
	logging.L().Warnf("%s", line)
}

func orNoCode(c string) string {
	if c == "" {
		return "(none)"
	}
	return c
}
