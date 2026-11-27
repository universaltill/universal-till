package enroll

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/config"
)

// RemovedTill is a till the account lists as removed (ut-docs#7098): its
// device was removed or its credential retired, so it is no longer a live entry.
// Only ids and the removal time; no names, no secrets.
type RemovedTill struct {
	DeviceID  string `json:"device_id"`
	TillID    string `json:"till_id,omitempty"`
	RemovedAt string `json:"removed_at,omitempty"`
}

// maxRemovedTills bounds what is read from the cloud (it caps its own list).
const maxRemovedTills = 200

// FleetWithRemoved is Fleet that also returns the cloud's removed_tills when it
// sends them. An older cloud sends none: removed is nil and the devices are
// exactly what Fleet returns. Fleet keeps its signature for its other callers.
func FleetWithRemoved(ctx context.Context, cfg *config.Config) ([]Device, []RemovedTill, error) {
	eff := Effective(cfg)
	m := eff.Marketplace
	if !CredentialsComplete(m) {
		return nil, nil, fmt.Errorf("till is not registered")
	}
	endpoint := strings.TrimRight(m.EndpointURL, "/") + "/v1/stores/devices?store_id=" + url.QueryEscape(m.StoreID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+m.MerchantToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("fleet returned %d", resp.StatusCode)
	}
	var envelope struct {
		Data struct {
			Devices      []Device      `json:"devices"`
			RemovedTills []RemovedTill `json:"removed_tills"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&envelope); err != nil {
		return nil, nil, fmt.Errorf("decode fleet: %w", err)
	}
	removed := envelope.Data.RemovedTills
	if len(removed) > maxRemovedTills {
		removed = removed[:maxRemovedTills]
	}
	return envelope.Data.Devices, removed, nil
}

// ErrRemoveNotEnabled: the cloud's till-initiated remove is switched off
// (TILL_DEVICE_REMOVE_ENABLED, default off). Nothing was changed.
var ErrRemoveNotEnabled = errors.New("the cloud has not enabled removing a till from the list")

// removeFromAccountTimeout bounds the one call; offline never blocks the page.
const removeFromAccountTimeout = 5 * time.Second

// RemoveFromAccount asks the cloud to remove deviceID from this shop's list
// (POST /v1/stores/devices/remove) with the main till's own device credential
// (ut-docs#7098, C2). 2xx and 404 (already removed) are success, so pressing the
// button twice is safe; ErrRemoveNotEnabled when the cloud flag is off; any
// other answer or an unreachable cloud is an error and nothing was changed.
// It never queues and never retries: the caller shows the failure and the
// button stays.
func RemoveFromAccount(ctx context.Context, cfg *config.Config, deviceID string) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return errors.New("remove from account: empty device id")
	}
	eff := Effective(cfg)
	m := eff.Marketplace
	if !CredentialsComplete(m) {
		return fmt.Errorf("till is not registered")
	}
	body, _ := json.Marshal(map[string]string{"store_id": m.StoreID, "device_id": deviceID})
	ctx, cancel := context.WithTimeout(ctx, removeFromAccountTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.EndpointURL, "/")+"/v1/stores/devices/remove", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.MerchantToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if bytes.Contains(raw, []byte("not_enabled")) {
			return ErrRemoveNotEnabled
		}
		return nil // till_not_found: already gone
	}
	return fmt.Errorf("remove from account returned %d", resp.StatusCode)
}
