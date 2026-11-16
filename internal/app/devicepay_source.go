package app

import (
	"context"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/devicepay"
	"github.com/universaltill/universal-till/internal/enroll"
)

// devicepaySource gives internal/devicepay the live enrolment
// (#3048). The bearer is read per call so a rotated credential is honoured.
type devicepaySource struct {
	cfg *config.Config
}

var _ devicepay.Source = devicepaySource{}

func (s devicepaySource) Target(context.Context) (string, string, bool) {
	m := enroll.Effective(s.cfg).Marketplace
	if !enroll.CredentialsComplete(m) {
		return "", "", false
	}
	return m.EndpointURL, m.MerchantToken, true
}
