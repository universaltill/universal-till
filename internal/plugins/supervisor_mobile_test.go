package plugins

import (
	"context"
	"strings"
	"testing"
)

// ADR-0121 §2 (ut-docs#3154): Android and iOS cannot exec a downloaded
// binary (W^X, Play policy, App Review 2.5.2), so the supervisor refuses a
// process plugin there before it tries.
func TestSupervisorRefusesProcessPluginsOnMobile(t *testing.T) {
	for _, goos := range []string{"android", "ios"} {
		prev := supervisorGOOS
		t.Cleanup(func() { supervisorGOOS = prev })
		supervisorGOOS = goos
		s := NewSupervisor(nil)
		err := s.StartPlugin(context.Background(), "com.test.native", "/bin/true", nil, RestartPolicy{})
		if err == nil || !strings.Contains(err.Error(), goos) {
			t.Fatalf("%s: StartPlugin err = %v, want a refusal naming the platform", goos, err)
		}
		if len(s.processes) != 0 {
			t.Fatalf("%s: a process was registered despite the refusal", goos)
		}
	}
}

func TestProcessPluginsSupported(t *testing.T) {
	for goos, want := range map[string]bool{"linux": true, "windows": true, "darwin": true, "android": false, "ios": false} {
		if got := processPluginsSupported(goos); got != want {
			t.Errorf("processPluginsSupported(%q) = %v, want %v", goos, got, want)
		}
	}
}
