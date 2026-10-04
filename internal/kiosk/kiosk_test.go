package kiosk

import (
	"errors"
	"testing"
)

type fakeBridge struct{ err error }

func (f fakeBridge) ReleaseKiosk() error { return f.err }

func TestKioskPinnedDefaultsFalseAndTracksSetter(t *testing.T) {
	t.Cleanup(func() { SetKioskPinned(false) })
	if KioskPinned() {
		t.Fatal("a till boots unpinned: KioskPinned must default to false")
	}
	SetKioskPinned(true)
	if !KioskPinned() {
		t.Fatal("KioskPinned after SetKioskPinned(true) = false")
	}
	SetKioskPinned(false)
	if KioskPinned() {
		t.Fatal("KioskPinned after SetKioskPinned(false) = true")
	}
}

func TestBridgeRegistration(t *testing.T) {
	t.Cleanup(func() { SetBridge(nil) })
	if RegisteredBridge() != nil {
		t.Fatal("no bridge is registered by default")
	}
	want := errors.New("boom")
	SetBridge(fakeBridge{err: want})
	b := RegisteredBridge()
	if b == nil || !errors.Is(b.ReleaseKiosk(), want) {
		t.Fatalf("RegisteredBridge = %#v", b)
	}
	SetBridge(nil)
	if RegisteredBridge() != nil {
		t.Fatal("SetBridge(nil) must un-register")
	}
}
