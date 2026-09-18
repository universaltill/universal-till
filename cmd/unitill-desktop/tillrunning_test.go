//go:build desktop

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/recovery"
)

// ut-docs#2397: tillAlreadyRunning's job is not "is the till healthy" — it's
// "is a listener already up at addr that a relaunch should attach to instead
// of spawning a second one", and recovery mode (internal/recovery.Serve)
// answers /healthz 503 by design for the entire time it's serving (same
// contract mobile.waitUntilReady already honours, ut-docs#1437/#1438).
// Before this fix, tillAlreadyRunning only accepted a 200, so relaunching the
// desktop shell while the existing unitill-pos sat on the recovery page spawned
// a second unitill-pos against the same locked data dir, which hit
// db.ErrDataDirLocked and hard-exited.
func TestTillAlreadyRunning_RecoveryModeWithHeaderCountsAsRunning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(recovery.HeaderMode, recovery.ModeRecovery)
		http.Error(w, "recovery mode", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if !tillAlreadyRunning(strings.TrimPrefix(srv.URL, "http://")) {
		t.Fatal("tillAlreadyRunning() = false, want true against a 503 recovery-mode response")
	}
}

// A bare 503 (no recovery-mode header — a foreign process squatting on the
// port, or some other unhealthy state) must NOT count as "already running":
// there is no Universal Till server there to attach to.
func TestTillAlreadyRunning_BareUnhealthy503IsNotRunning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unhealthy", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if tillAlreadyRunning(strings.TrimPrefix(srv.URL, "http://")) {
		t.Fatal("tillAlreadyRunning() = true, want false against a bare 503 with no recovery-mode header")
	}
}

// The existing, unchanged case: a healthy 200 counts as running regardless of
// any header.
func TestTillAlreadyRunning_Healthy200IsRunning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if !tillAlreadyRunning(strings.TrimPrefix(srv.URL, "http://")) {
		t.Fatal("tillAlreadyRunning() = false, want true against a healthy 200")
	}
}
