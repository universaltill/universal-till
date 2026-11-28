package pages

import (
	"context"
	"time"

	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/netaccess"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settlementfee"
)

// ut-docs#7160 (epic #7061): the day-so-far screen shows the card-fee line
// the cloud imported from the payment provider's settlement data
// (settlementfee). Display only: the till never computes a fee and holds no
// rate. The line is on screen only, never on the printed paper, the JSON
// report or the archived Z. No data, no cloud credentials, offline or any
// failure: no line, no error, and the report is never delayed past
// settlementfee.Timeout.

// settlementFeeFetcher builds the fetcher for this till; nil when it has no
// cloud credentials. A variable so tests can supply fixtures.
var settlementFeeFetcher = func(d *common.Deps) settlementfee.Fetcher {
	if d == nil || d.Cfg == nil {
		return nil
	}
	m := enroll.Effective(d.Cfg).Marketplace
	if !enroll.CredentialsComplete(m) || m.DeviceID == "" {
		return nil
	}
	return settlementfee.NewHTTPFetcher(feeHTTPClient, m.EndpointURL, m.DeviceID, m.MerchantToken, hasEnglishKey)
}

// feeHTTPClient goes through netaccess like every outbound client (ADR-0113).
var feeHTTPClient = netaccess.NewClient(settlementfee.Timeout)

// hasEnglishKey reports whether en.json has the key (T falls back to the key
// itself when it does not).
func hasEnglishKey(key string) bool { return httpx.T("en", key) != key }

// eodFeeLines returns the fee lines to show for the local calendar day of
// `at`, or nil. Best-effort by design: failures are logged and swallowed.
func eodFeeLines(ctx context.Context, d *common.Deps, at time.Time) []settlementfee.Line {
	fetch := settlementFeeFetcher(d)
	if fetch == nil {
		return nil
	}
	lines, err := fetch(ctx, at.Local().Format("2006-01-02"))
	if err != nil {
		logging.L().Debugf("eod card fee line: %v", err)
		return nil
	}
	return lines
}
