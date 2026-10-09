package fiscal

import "testing"

func TestRequiresPerSaleDeviceReceipt(t *testing.T) {
	for _, tc := range []struct {
		country string
		want    bool
	}{
		{"TR", true}, {"DE", false}, {"GB", false}, {"tr", false}, {"", false},
	} {
		if got := RequiresPerSaleDeviceReceipt(tc.country); got != tc.want {
			t.Errorf("RequiresPerSaleDeviceReceipt(%q) = %v, want %v", tc.country, got, tc.want)
		}
	}
}

func TestRequiresTaxRateSwitch(t *testing.T) {
	for _, tc := range []struct {
		country string
		want    bool
	}{
		{"DE", true}, {"de", true}, {" DE", false}, {"TR", false}, {"GB", false}, {"", false},
	} {
		if got := RequiresTaxRateSwitch(tc.country); got != tc.want {
			t.Errorf("RequiresTaxRateSwitch(%q) = %v, want %v", tc.country, got, tc.want)
		}
	}
}
