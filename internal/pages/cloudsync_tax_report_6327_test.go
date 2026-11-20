package pages

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#6327: every till reports its default tax rate and price basis.
func TestDeviceExtra_ReportsDefaultTaxRateAndBasis(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	h := buildCloudHooks(dp, nil)

	dp.UpdateState(func(s *common.RuntimeState) { s.TaxRateBP = 810; s.TaxInclusive = false })
	extra := h.DeviceExtra(t.Context())
	if v, ok := extra["tax_rate_bp"].(int); !ok || v != 810 {
		t.Fatalf("tax_rate_bp = %#v, want int 810", extra["tax_rate_bp"])
	}
	if extra["tax_inclusive"] != false {
		t.Fatalf("tax_inclusive = %#v, want false", extra["tax_inclusive"])
	}

	// 0 % is a real rate: it must be sent, never omitted.
	dp.UpdateState(func(s *common.RuntimeState) { s.TaxRateBP = 0; s.TaxInclusive = true })
	extra = h.DeviceExtra(t.Context())
	v, present := extra["tax_rate_bp"]
	if !present || v != 0 {
		t.Fatalf("tax_rate_bp = %#v (present=%v), want 0", v, present)
	}
	if extra["tax_inclusive"] != true {
		t.Fatalf("tax_inclusive = %#v, want true", extra["tax_inclusive"])
	}
}

func TestDeviceExtra_TaxReportMarshalsAsSnakeCaseNumbers(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	dp.UpdateState(func(s *common.RuntimeState) { s.TaxRateBP = 810; s.TaxInclusive = false })
	extra := buildCloudHooks(dp, nil).DeviceExtra(t.Context())
	b, err := json.Marshal(extra)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"tax_rate_bp":810`, `"tax_inclusive":false`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("payload lacks %s: %s", want, b)
		}
	}
}
