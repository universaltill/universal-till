package pages

import (
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// voucherTypeCardValues derives the Settings page's gift-voucher-type card
// values (ADR-0105, ut-docs#1037) from the raw settings map: the effective
// type (absent or unrecognised == multi_purpose, exactly what
// pos.ResolveVoucherIssuePolicy resolves at issue time) and the
// single-purpose rate as a decimal-percent string for the input (absent or
// unparsable == the shop's standard rate, the same fallback issuance
// applies — so what the card shows is what a sale would actually do).
func voucherTypeCardValues(all map[string]string, standardRatePct int) (voucherType, ratePct string) {
	voucherType = data.VoucherTypeMultiPurpose
	if strings.TrimSpace(all[common.KeyVoucherDefaultType]) == data.VoucherTypeSinglePurpose {
		voucherType = data.VoucherTypeSinglePurpose
	}
	rateBP := standardRatePct * 100
	if bp, err := strconv.Atoi(strings.TrimSpace(all[common.KeyVoucherSinglePurposeTaxRateBP])); err == nil && bp >= 0 {
		rateBP = bp
	}
	return voucherType, common.FormatServiceChargeRatePercent(rateBP)
}
