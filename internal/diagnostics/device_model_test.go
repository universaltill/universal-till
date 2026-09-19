package diagnostics

import "testing"

// SetDeviceModel/DeviceModel round-trip (ut-docs#2235), mirroring
// internal/bluetooth's SetAndroidBridge/RegisteredAndroidBridge test shape:
// unset reports "", a set value is reported back, and "" un-sets it again.
func TestSetDeviceModel_RoundTrip(t *testing.T) {
	t.Cleanup(func() { SetDeviceModel("") })

	if got := DeviceModel(); got != "" {
		t.Fatalf("DeviceModel() before any Set = %q, want \"\"", got)
	}

	SetDeviceModel("Pixel 8")
	if got := DeviceModel(); got != "Pixel 8" {
		t.Fatalf("DeviceModel() = %q, want %q", got, "Pixel 8")
	}

	SetDeviceModel("")
	if got := DeviceModel(); got != "" {
		t.Fatalf("DeviceModel() after clearing = %q, want \"\"", got)
	}
}
