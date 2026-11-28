package plugins

import "testing"

// ut-docs#7230: the install path derives synchronous=FULL from keep_years.
func TestRetentionYears_7230(t *testing.T) {
	if retentionYears(nil) != 0 || retentionYears(&Manifest{}) != 0 {
		t.Fatal("no retention must be 0")
	}
	if got := retentionYears(&Manifest{Retention: &ManifestRetention{KeepYears: 10}}); got != 10 {
		t.Fatalf("got %d, want 10", got)
	}
}
