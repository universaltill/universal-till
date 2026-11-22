package config

import "testing"

func mustInit(t *testing.T) *Config {
	t.Helper()
	c, err := Init()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLoad_HostIDFromEnv(t *testing.T) {
	t.Setenv("UT_HOST_ID", "  pin-1 ")
	if got := mustInit(t).HostID; got != "pin-1" {
		t.Fatalf("HostID = %q, want trimmed pin-1", got)
	}
	t.Setenv("UT_HOST_ID", "")
	if got := mustInit(t).HostID; got != "" {
		t.Fatalf("HostID = %q, want empty", got)
	}
}
