package cloudsync

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
)

// Upload of the main till's neutral delivery signals (ut-docs#6583, S7a, till
// side; migration 121 delivery_signals). Each queued availability.set /
// service.set goes to
//
//	POST <endpoint>/v1/stores/channel/delivery-signals
//	{"store_id": "...", "signals": [{"signal_id","kind","payload","created_at"}]}
//	-> 200 {"data": {"accepted": [signal_id...], "reassert": false}, "error": null}
//
// with the till's own bearer credential, as the channel order actions do. The
// endpoint is a PROPOSED contract (the cloud side is not built in this branch):
// a 404/405/501 holds the upload for an hour and keeps every row; any other
// failure backs off for five minutes. `reassert: true` means the cloud saw a
// channel report a different availability than the till's (a provider-side
// unsnooze): the till queues its whole state again, so the till wins
// (ADR-0215 decision 2, like stock in ADR-0169 §8).
//
// Nothing on the sale path calls this; failures are logged and never returned.

const (
	deliverySignalsPath = "/v1/stores/channel/delivery-signals"
	deliverySignalsMax  = 50
	// bodies are small (a signal is a few hundred bytes); the answer is a list of ids.
	deliverySignalsMaxAnswer = 256 << 10
)

var (
	deliverySignalsMu        sync.Mutex
	deliverySignalsHoldUntil time.Time
)

func deliverySignalsHold(until time.Time) {
	deliverySignalsMu.Lock()
	deliverySignalsHoldUntil = until
	deliverySignalsMu.Unlock()
}

func deliverySignalsHeld(now time.Time) bool {
	deliverySignalsMu.Lock()
	defer deliverySignalsMu.Unlock()
	return now.Before(deliverySignalsHoldUntil)
}

type deliverySignalWire struct {
	SignalID  string          `json:"signal_id"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

// uploadDeliverySignals sends the queued signals when the gate is open: the
// main till (no sync.primary_url), a complete enrolment, something pending, no
// hold. It never returns an error.
func uploadDeliverySignals(ctx context.Context, cfg *config.Config, db *sql.DB, now time.Time) {
	m := enroll.Effective(cfg).Marketplace
	if !enroll.CredentialsComplete(m) || deliverySignalsHeld(now) {
		return
	}
	if v, _, _ := data.NewSettingsRepo(db).Get(ctx, "sync.primary_url"); strings.TrimSpace(v) != "" {
		return
	}
	repo := data.NewDeliveryAvailabilityRepo(db)
	pending, err := repo.PendingSignals(ctx, deliverySignalsMax)
	if err != nil {
		logging.L().Warnf("cloudsync: delivery signals: %v", err)
		return
	}
	if len(pending) == 0 {
		return
	}
	wire := make([]deliverySignalWire, 0, len(pending))
	for _, s := range pending {
		wire = append(wire, deliverySignalWire{SignalID: s.SignalID, Kind: s.Kind, Payload: json.RawMessage(s.PayloadJSON), CreatedAt: s.CreatedAt})
	}
	body, err := json.Marshal(struct {
		StoreID string               `json:"store_id"`
		Signals []deliverySignalWire `json:"signals"`
	}{m.StoreID, wire})
	if err != nil {
		logging.L().Warnf("cloudsync: delivery signals: encode: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.EndpointURL, "/")+deliverySignalsPath, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+m.MerchantToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		// Never log the URL or header: only that the call failed.
		logging.L().Infof("cloudsync: delivery signals not sent (kept, retried later)")
		deliverySignalsHold(now.Add(5 * time.Minute))
		return
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented:
		logging.L().Infof("cloudsync: the cloud does not take delivery signals yet (%d); %d kept", resp.StatusCode, len(pending))
		deliverySignalsHold(now.Add(time.Hour))
		return
	case resp.StatusCode != http.StatusOK:
		logging.L().Infof("cloudsync: delivery signals answered %d (kept, retried later)", resp.StatusCode)
		deliverySignalsHold(now.Add(5 * time.Minute))
		return
	}
	var env struct {
		Data *struct {
			Accepted []string `json:"accepted"`
			Reassert bool     `json:"reassert"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, deliverySignalsMaxAnswer)).Decode(&env); err != nil || env.Data == nil {
		logging.L().Infof("cloudsync: delivery signals answered without a usable body (kept, retried later)")
		deliverySignalsHold(now.Add(5 * time.Minute))
		return
	}
	// Only ids this call sent are marked: a hostile or buggy answer cannot touch other rows.
	sent := map[string]bool{}
	for _, s := range pending {
		sent[s.SignalID] = true
	}
	var ok []string
	for _, id := range env.Data.Accepted {
		if sent[id] {
			ok = append(ok, id)
		}
	}
	if err := repo.MarkSent(ctx, ok, now); err != nil {
		logging.L().Warnf("cloudsync: delivery signals: %v", err)
		return
	}
	if env.Data.Reassert {
		if err := repo.Reassert(ctx, now, ""); err != nil && !errors.Is(err, context.Canceled) {
			logging.L().Warnf("cloudsync: delivery signals: re-asserting the till's availability failed: %v", err)
		}
	}
}
