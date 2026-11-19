package server

import "testing"

func TestDocsShotsNoDailyBackup(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "true": true, " 1 ": true, "": false, "yes": false, "0": false, "false": false} {
		if got := docsShotsNoDailyBackup(v); got != want {
			t.Errorf("docsShotsNoDailyBackup(%q) = %v, want %v", v, got, want)
		}
	}
}
