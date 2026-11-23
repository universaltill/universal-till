package plugins

import (
	"context"
	"crypto/tls"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#6627: GrantDeclaredPermissions is the rule installGrantsPermission
// applies to a marketplace install, exported for the bundled first-party
// loader. First declaration only: an operator's revoke survives a re-run.
func TestGrantDeclaredPermissions_FirstDeclarationOnlyAndRevokeSurvives(t *testing.T) {
	d := hostfnTestDB(t)
	ctx := context.Background()
	m := &Manifest{ID: "com.universaltill.payment-fake", Name: "Fake", Version: "1.0.0", Entrypoint: "./bin/plugin.wasm",
		Runtime: "wasm", CanonicalType: "payment", DeviceArch: "any", MinPOSVersion: "1.0.0",
		Permissions: []string{"events:receive", "net:api.example.com", "storage", AIToolProvidePermission}}
	if err := PersistManifest(ctx, d, m, InstallOptions{TrustLevel: "system", BundledLockVerified: true}); err != nil {
		t.Fatal(err)
	}
	// Fresh install: nothing was declared before.
	GrantDeclaredPermissions(ctx, d, m, map[string]bool{}, nil)
	granted := map[string]bool{}
	perms, _ := data.NewPluginRepo(d).ListPermissions(ctx, m.ID)
	for _, p := range perms {
		granted[p.Permission] = p.Granted
	}
	for _, want := range []string{"events:receive", "net:api.example.com", "storage"} {
		if !granted[want] {
			t.Errorf("%s not granted: %v", want, granted)
		}
	}
	if granted[AIToolProvidePermission] {
		t.Errorf("an ADR-0153 ungranted permission was granted: %v", granted)
	}
	// The operator revokes net; an update (permission already declared) keeps that.
	if err := RevokePermission(ctx, d, m.ID, "net:api.example.com"); err != nil {
		t.Fatal(err)
	}
	GrantDeclaredPermissions(ctx, d, m, map[string]bool{"net:api.example.com": true, "events:receive": true, "storage": true}, nil)
	perms, _ = data.NewPluginRepo(d).ListPermissions(ctx, m.ID)
	for _, p := range perms {
		if p.Permission == "net:api.example.com" && p.Granted {
			t.Fatal("a revoked permission was granted again by a re-run")
		}
	}
	// Unreadable previous set: fail closed, grant nothing.
	d2 := hostfnTestDB(t)
	if err := PersistManifest(ctx, d2, m, InstallOptions{TrustLevel: "system", BundledLockVerified: true}); err != nil {
		t.Fatal(err)
	}
	GrantDeclaredPermissions(ctx, d2, m, nil, context.DeadlineExceeded)
	perms, _ = data.NewPluginRepo(d2).ListPermissions(ctx, m.ID)
	for _, p := range perms {
		if p.Granted {
			t.Fatalf("%s granted although the previous set was unreadable", p.Permission)
		}
	}
}

// With a fake provider host (TLS, resolved by the stubbed dialer): the net:
// call of a bundled plugin goes through only once its declared permission is
// granted, which is what GrantDeclaredPermissions does at install.
func TestGrantDeclaredPermissions_NetCallReachesFakeProviderOnlyWhenGranted(t *testing.T) {
	guest := buildHostfnGuest(t)
	cert, pool := egressTestCert(t)
	var hits atomic.Int32
	srv := httptest.NewUnstartedServer(hitCounter(&hits, "ok"))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()
	env := &egressEnv{dns: map[string][]string{"api.example.com": {"93.184.216.34"}}, route: map[string]string{"93.184.216.34": srv.Listener.Addr().String()}}

	run := func(t *testing.T, id string, grant bool) (map[string]any, int32) {
		d := hostfnTestDB(t)
		ctx := context.Background()
		m := &Manifest{ID: id, Name: "Fake", Version: "1.0.0", Entrypoint: "./plugin.wasm", Runtime: "wasm",
			CanonicalType: "payment", DeviceArch: "any", MinPOSVersion: "1.0.0",
			Permissions: []string{"net:api.example.com", "storage"}}
		if err := PersistManifest(ctx, d, m, InstallOptions{TrustLevel: "system", BundledLockVerified: true}); err != nil {
			t.Fatal(err)
		}
		GrantDeclaredPermissions(ctx, d, m, map[string]bool{}, nil)
		if !grant { // the pre-fix state of a bundled install: net: declared, not granted
			if err := RevokePermission(ctx, d, id, "net:api.example.com"); err != nil {
				t.Fatal(err)
			}
		}
		w := NewWasmRuntime(t.TempDir())
		w.httpClient = env.client(pool)
		if err := w.load(id, "1.0.0", guest); err != nil {
			t.Fatal(err)
		}
		w.hasNet[id] = true
		before := hits.Load()
		return runGuest(t, w, d, id, "https://api.example.com/ok"), hits.Load() - before
	}
	res, n := run(t, "com.universaltill.payment-granted", true)
	if res["http_status"] != float64(200) || n != 1 {
		t.Fatalf("granted: http_status=%v code=%v hits=%d, want 200 and one hit", res["http_status"], res["http_code"], n)
	}
	res, n = run(t, "com.universaltill.payment-ungranted", false)
	if res["http_code"] != float64(hostErrDenied) || n != 0 {
		t.Fatalf("ungranted: http_code=%v status=%v hits=%d, want denied and no hit", res["http_code"], res["http_status"], n)
	}
}

// ADR-0198 3.1 (ut-docs#6458, design D3): a bundled plugin declaring the two
// Tap to Pay permissions gets the rows ungranted on its first install; its
// ordinary permissions are granted as before.
func TestBundledLoader_DoesNotGrantBrokerPermissions(t *testing.T) {
	d := hostfnTestDB(t)
	ctx := context.Background()
	m := &Manifest{ID: "com.universaltill.payment-faketap", Name: "Fake", Version: "1.10.0", Entrypoint: "./bin/plugin.wasm",
		Runtime: "wasm", CanonicalType: "payment", DeviceArch: "any", MinPOSVersion: "1.0.0",
		Permissions: []string{"events:receive", "storage", TapDevicePermission, BrokerNetPermission}}
	if err := PersistManifest(ctx, d, m, InstallOptions{TrustLevel: "system", BundledLockVerified: true}); err != nil {
		t.Fatal(err)
	}
	GrantDeclaredPermissions(ctx, d, m, map[string]bool{}, nil)
	granted := map[string]bool{}
	perms, _ := data.NewPluginRepo(d).ListPermissions(ctx, m.ID)
	for _, p := range perms {
		granted[p.Permission] = p.Granted
	}
	for _, want := range []string{"events:receive", "storage"} {
		if !granted[want] {
			t.Errorf("%s not granted: %v", want, granted)
		}
	}
	for _, perm := range []string{TapDevicePermission, BrokerNetPermission} {
		g, present := granted[perm]
		if !present || g {
			t.Errorf("%s: row present=%v granted=%v, want a row with granted=0", perm, present, g)
		}
	}
}
