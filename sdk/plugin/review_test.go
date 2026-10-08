//go:build !wasip1

package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// Review findings (ut-docs#3951, Fable): each is a place the fake host was
// more lenient than the till, so a plugin test passed natively and failed on
// a device.

func TestFakeTCPPeerCloseIsInternalErrorNotEOF(t *testing.T) {
	h := NewFakeHost()
	h.TCP = func(string, int) (io.ReadWriteCloser, error) {
		a, b := net.Pipe()
		_ = b.Close()
		return a, nil
	}
	UseFakeHost(t, h)
	c, err := TCPOpen("10.0.0.5", 20007, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(make([]byte, 8)); !errors.Is(err, ErrInternal) {
		t.Fatalf("read after peer close: err=%v, want ErrInternal (the till never reports EOF on tcp_read)", err)
	}
}

func TestFakeEventPublishOwnNamespaceOnly(t *testing.T) {
	h := NewFakeHost()
	h.PluginID = "com.example.shop"
	UseFakeHost(t, h)
	for _, typ := range []string{"sale.completed", "done", "com.other.x", "Com.Example.Shop.x", "com.example.shopx.y"} {
		if err := EventPublish(typ, nil); !errors.Is(err, ErrDenied) {
			t.Errorf("EventPublish(%q) err=%v, want ErrDenied", typ, err)
		}
	}
	if err := EventPublish("com.example.shop.done", nil); err != nil {
		t.Fatalf("own namespace: %v", err)
	}
}

func TestFakeSecretSetOnlyDeclaredSecretKeys(t *testing.T) {
	h := NewFakeHost()
	h.SecretSettings["api_token"] = true
	UseFakeHost(t, h)
	if err := SecretSet("never_declared", "x"); !errors.Is(err, ErrDenied) {
		t.Fatalf("undeclared key: err=%v, want ErrDenied", err)
	}
	if err := SecretSet("api_token", "\xff"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-UTF-8 value: err=%v, want ErrInvalid", err)
	}
	if err := SecretSet("api_token", "ok"); err != nil {
		t.Fatal(err)
	}
}

func TestFakeHookMayCallTheSDK(t *testing.T) {
	h := NewFakeHost()
	h.HTTP = func(r HTTPRequest) (HTTPResponse, error) {
		Log("hook saw " + r.URL) // used to deadlock: hooks ran under the host lock
		return HTTPResponse{Status: 204}, nil
	}
	UseFakeHost(t, h)
	done := make(chan error, 1)
	go func() { _, err := HTTP(HTTPRequest{URL: "https://a.example"}); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP hook calling the SDK deadlocked")
	}
}

func TestFakeSizeCaps(t *testing.T) {
	h := NewFakeHost()
	h.Views = func(string, json.RawMessage) (json.RawMessage, error) { return json.RawMessage("[]"), nil }
	h.HTTP = func(HTTPRequest) (HTTPResponse, error) {
		return HTTPResponse{Status: 200, Body: bytes.Repeat([]byte("b"), 600<<10)}, nil
	}
	UseFakeHost(t, h)
	if err := StorageSet("k", make([]byte, 64<<10+1)); !errors.Is(err, ErrInvalid) {
		t.Errorf("65 KiB value: err=%v, want ErrInvalid", err)
	}
	if err := StorageSet(strings.Repeat("k", 129), nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("129-byte key: err=%v, want ErrInvalid", err)
	}
	if _, err := UploadOpen(strings.Repeat("Z", 32)); !errors.Is(err, ErrInvalid) {
		t.Errorf("non-hex token: err=%v, want ErrInvalid", err)
	}
	if _, err := ViewQuery(strings.Repeat("v", 129), nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("129-byte view name: err=%v, want ErrInvalid", err)
	}
	s, err := HTTPOpen(HTTPRequest{URL: "https://a.example"})
	if err != nil {
		t.Fatal(err)
	}
	n, _ := s.Read(make([]byte, 1<<20))
	if n != 256<<10 {
		t.Errorf("one http_read returned %d bytes, want the host's 256 KiB chunk cap", n)
	}
	for i := 0; i < 70; i++ {
		_, err = ViewQuery("items.top.v1", nil)
	}
	if !errors.Is(err, ErrQuota) {
		t.Errorf("view call 70: err=%v, want ErrQuota (64 per event)", err)
	}
}

func TestFakeStreamCallsHonourDeny(t *testing.T) {
	h := NewFakeHost()
	h.HTTP = func(HTTPRequest) (HTTPResponse, error) { return HTTPResponse{Status: 200}, nil }
	UseFakeHost(t, h)
	s, err := HTTPOpen(HTTPRequest{URL: "https://a.example"})
	if err != nil {
		t.Fatal(err)
	}
	h.Deny["http_read"] = true // a grant revoked mid-stream
	if _, err := s.Read(make([]byte, 8)); !errors.Is(err, ErrDenied) {
		t.Fatalf("err=%v, want ErrDenied", err)
	}
}

func TestExitCodeZeroIsStillAFailure(t *testing.T) {
	if got := exitStatus(ExitCode(0)); got != 1 {
		t.Fatalf("ExitCode(0) exits %d, want 1: a handler error is never a success", got)
	}
	if got := exitStatus(ExitCode(3)); got != 3 {
		t.Fatalf("ExitCode(3) exits %d", got)
	}
	if got := exitStatus(errors.New("x")); got != 1 {
		t.Fatalf("plain error exits %d", got)
	}
}

func TestSubMillisecondTimeoutRoundsUp(t *testing.T) {
	if got := millis(500 * time.Microsecond); got != 1 {
		t.Fatalf("millis(500µs) = %d, want 1", got)
	}
	if got := millis(0); got != 5000 {
		t.Fatalf("millis(0) = %d, want the 5 s default", got)
	}
}

// device_* host functions (ADR-0140, universal-till#1764) landed while this
// SDK was in review; the guard made the wrappers mandatory.
func TestDeviceInfo(t *testing.T) {
	h := NewFakeHost()
	UseFakeHost(t, h)
	if _, err := DeviceID(); !errors.Is(err, ErrDenied) {
		t.Fatalf("no device-info configured: err=%v, want ErrDenied", err)
	}
	h.DeviceID, h.DeviceIPs, h.DeviceTimezone = "0b7f6c1e-9d2a-4c1b-8e3f-5a6b7c8d9e0f", []string{"192.168.1.20"}, "UTC+01:00"
	id, err := DeviceID()
	if err != nil || id != h.DeviceID {
		t.Fatalf("id=%q err=%v", id, err)
	}
	ips, err := DeviceLocalIPs()
	if err != nil || len(ips) != 1 || ips[0] != "192.168.1.20" {
		t.Fatalf("ips=%v err=%v", ips, err)
	}
	if tz, err := DeviceTimezone(); err != nil || tz != "UTC+01:00" {
		t.Fatalf("tz=%q err=%v", tz, err)
	}
	h.DeviceIPs = []string{}
	if ips, err := DeviceLocalIPs(); err != nil || ips == nil || len(ips) != 0 {
		t.Fatalf("no interfaces: ips=%#v err=%v, want an empty non-nil list", ips, err)
	}
}
