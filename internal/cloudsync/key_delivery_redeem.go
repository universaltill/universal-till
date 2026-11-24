package cloudsync

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
)

// The till's side of the one-time key delivery code (ut-docs#6960,
// ADR-0213 Decision 9): the shop owner shows a code on my.; the main till
// posts it with its device credential and the cloud then queues the sealed
// key as a directive. The code carries no key and the answer never does.

// RedeemOutcome is the one answer the UI distinguishes. A bad, expired,
// used, other-store or not-the-main-till code is always RedeemRefused: the
// till must not hint which.
type RedeemOutcome int

const (
	RedeemAccepted RedeemOutcome = iota + 1
	RedeemRefused
	RedeemRateLimited
	RedeemOffline
	RedeemUnavailable
)

const (
	keyCodeLen      = 26
	keyCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ" // Crockford base32
)

// NormalizeKeyDeliveryCode turns a typed or scanned code into the 26
// canonical characters: case, hyphens and spaces ignored, I/L read as 1 and
// O as 0 (Crockford). ok is false for any other shape.
func NormalizeKeyDeliveryCode(in string) (string, bool) {
	var b strings.Builder
	for _, r := range strings.ToUpper(in) {
		switch {
		case r == '-' || r == ' ':
			continue
		case r == 'I' || r == 'L':
			r = '1'
		case r == 'O':
			r = '0'
		}
		if r > 127 || !strings.ContainsRune(keyCodeAlphabet, r) {
			return "", false
		}
		b.WriteRune(r)
	}
	if b.Len() != keyCodeLen {
		return "", false
	}
	return b.String(), true
}

// RedeemKeyDeliveryCode posts the code to the cloud (main till only; the
// cloud also checks). The error is for logging and never carries the code.
func RedeemKeyDeliveryCode(ctx context.Context, cfg *config.Config, code string) (RedeemOutcome, error) {
	norm, ok := NormalizeKeyDeliveryCode(code)
	if !ok {
		return RedeemRefused, nil
	}
	m := enroll.Effective(cfg).Marketplace
	body, _ := json.Marshal(map[string]string{"code": norm})
	url := strings.TrimRight(m.EndpointURL, "/") + "/v1/stores/key-delivery/redeem"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return RedeemUnavailable, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.MerchantToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		return RedeemOffline, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, cloudErrorMaxBytes))
	switch {
	case resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK:
		return RedeemAccepted, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return RedeemRateLimited, nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return RedeemRefused, nil
	}
	return RedeemUnavailable, nil
}
