package plugins

import (
	"context"
	"strings"
	"testing"
)

// Install-time enforcement of fiscal.sign.ask's `exclusive` marker
// (ADR-0041 Decision B; independent review of ut-docs#675, finding B2).
// The enable-time check in setPluginActiveHandler is not enough on its own:
// PersistManifest activates a plugin unconditionally (is_active = 1 on both
// the INSERT and the ON CONFLICT UPDATE branch of UpsertPluginManifest), so
// installing a second fiscal.sign.ask-declaring plugin — or updating an
// existing plugin to newly declare the hook — silently created two active
// answerers on an exclusive point without ever passing through /enable.
// Same call-site shape as validatePageEntryKeys/validatePageEntryRoutes:
// checked inside PersistManifest's transaction, whole install rolls back on
// refusal.

// fiscalSignManifest builds a minimal manifest declaring the
// fiscal.sign.ask hook. The event name is written as a literal here (not
// the FiscalSignAskEvent const) so this file compiles — and demonstrably
// FAILS — against the pre-fix code too.
func fiscalSignManifest(id string) *Manifest {
	return &Manifest{
		ID:         id,
		Name:       "Signer " + id,
		Version:    "1.0.0",
		Entrypoint: "./main.wasm",
		Hooks: []ManifestHook{
			{Event: "fiscal.sign.ask", Action: "fiscal.sign"},
		},
	}
}

func TestPersistManifest_RejectsSecondFiscalSignAskPlugin(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	if err := PersistManifest(ctx, d.DB, fiscalSignManifest("com.first.signer"), InstallOptions{}); err != nil {
		t.Fatalf("first signing plugin must install cleanly: %v", err)
	}
	err := PersistManifest(ctx, d.DB, fiscalSignManifest("com.second.signer"), InstallOptions{})
	if err == nil {
		t.Fatal("PersistManifest installed a second active fiscal.sign.ask answerer — the point is exclusive (ADR-0041 Decision B)")
	}
	if !strings.Contains(err.Error(), "com.first.signer") || !strings.Contains(err.Error(), "fiscal.sign.ask") {
		t.Fatalf("refusal should name the owning plugin and the point, got: %v", err)
	}
	// The refused plugin must not be half-installed (transaction rollback,
	// same guarantee the page-key collision check gives).
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.second.signer'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rejected plugin left %d plugins row(s), want 0", n)
	}
	// The first plugin still owns the point.
	var owner string
	if err := d.DB.QueryRow(`SELECT plugin_id FROM plugin_hooks WHERE event='fiscal.sign.ask' AND is_active=1`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != "com.first.signer" {
		t.Fatalf("point owner changed: %q", owner)
	}
}

// An UPDATE of an unrelated installed plugin whose new version starts
// declaring fiscal.sign.ask while another plugin holds the point is refused
// the same way — this is the exact silent path the enable-time check never
// sees (the plugin is already active; no /enable ever fires).
func TestPersistManifest_RejectsUpdateNewlyDeclaringFiscalSignAsk(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	// Plugin B: installed first, no fiscal hook.
	plain := &Manifest{ID: "com.other.plugin", Name: "Other", Version: "1.0.0", Entrypoint: "./main.wasm"}
	if err := PersistManifest(ctx, d.DB, plain, InstallOptions{}); err != nil {
		t.Fatalf("install plain plugin: %v", err)
	}
	// Plugin A: the active signing provider.
	if err := PersistManifest(ctx, d.DB, fiscalSignManifest("com.owner.signer"), InstallOptions{}); err != nil {
		t.Fatalf("install owner: %v", err)
	}

	// Plugin B v1.0.1 now declares the hook — must be refused while A holds it.
	upgraded := fiscalSignManifest("com.other.plugin")
	upgraded.Version = "1.0.1"
	err := PersistManifest(ctx, d.DB, upgraded, InstallOptions{})
	if err == nil {
		t.Fatal("an update newly declaring fiscal.sign.ask must be refused while another plugin owns the point")
	}
	if !strings.Contains(err.Error(), "com.owner.signer") {
		t.Fatalf("refusal should name the owning plugin, got: %v", err)
	}
	// The refused update must not have bumped the stored version.
	var version string
	if err := d.DB.QueryRow(`SELECT version FROM plugins WHERE id = 'com.other.plugin'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "1.0.0" {
		t.Fatalf("refused update must roll back, plugin now at %q", version)
	}
}

// A plugin updating/re-installing ITSELF while it already holds the point
// must not conflict with its own prior registration.
func TestPersistManifest_FiscalSignAskSelfUpdateNotAConflict(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	if err := PersistManifest(ctx, d.DB, fiscalSignManifest("com.self.signer"), InstallOptions{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	upgraded := fiscalSignManifest("com.self.signer")
	upgraded.Version = "1.0.1"
	if err := PersistManifest(ctx, d.DB, upgraded, InstallOptions{}); err != nil {
		t.Fatalf("a signing plugin updating itself must not conflict with its own registration: %v", err)
	}
}

// Fail closed (same posture as the enable-time check): a DB error while
// answering "who owns the point?" refuses the persist — it never skips the
// check and installs anyway. plugin_hooks is what both the check and the
// later hook insert read/write; dropping it makes the CHECK error first
// (it runs before any insert), and the error must be the check's own,
// proving the refusal came from the guard rather than a later write.
func TestPersistManifest_FiscalSignAskCheckFailsClosedOnDBError(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	mustExecSQL(t, d, `DROP TABLE plugin_hooks`)
	err := PersistManifest(ctx, d.DB, fiscalSignManifest("com.unlucky.signer"), InstallOptions{})
	if err == nil {
		t.Fatal("a DB error during the exclusivity check must refuse the persist (fail closed)")
	}
	if !strings.Contains(err.Error(), "fiscal signing exclusivity") {
		t.Fatalf("expected the exclusivity check's own error (fail closed at the guard, not a later insert), got: %v", err)
	}
}

// --- ADR-0077 D3 (ut-docs#1520): the three-event exclusivity GROUP ----------
//
// {fiscal.sign.ask, fiscal.sign.start, fiscal.sign.reconcile.ask} is ONE
// exclusivity group: declaring any one of the three while a DIFFERENT active
// plugin holds any one of the three is refused at persist time, exactly as
// the single-event check above already did for fiscal.sign.ask alone. Before
// this, a second plugin declaring only fiscal.sign.start or only
// fiscal.sign.reconcile.ask passed unmodified (ADR-0077 D3 calls this out
// as a real gap found on independent review) — and EventBus.Ask would then
// have handed it real sale data / accepted its "confirmed" answer.

// fiscalHookManifest is fiscalSignManifest generalized to any one event of
// the group — literals, not the constants, for the same reason as above.
func fiscalHookManifest(id, event string) *Manifest {
	return &Manifest{
		ID:         id,
		Name:       "Signer " + id,
		Version:    "1.0.0",
		Entrypoint: "./main.wasm",
		Hooks: []ManifestHook{
			{Event: event, Action: "fiscal.sign"},
		},
	}
}

var fiscalSignGroupEvents = []string{"fiscal.sign.ask", "fiscal.sign.start", "fiscal.sign.reconcile.ask"}

// Every directional pair across the group (held × declared, 9 combinations
// including the three same-event ones): a different plugin is refused, the
// refusal names the owner, and the refused install rolls back entirely.
func TestPersistManifest_FiscalSignGroupRefusesEveryDirectionalPair(t *testing.T) {
	for _, held := range fiscalSignGroupEvents {
		for _, declared := range fiscalSignGroupEvents {
			t.Run(held+" blocks "+declared, func(t *testing.T) {
				d := openRealDB(t)
				ctx := context.Background()
				if err := PersistManifest(ctx, d.DB, fiscalHookManifest("com.owner.signer", held), InstallOptions{}); err != nil {
					t.Fatalf("owner must install cleanly: %v", err)
				}
				err := PersistManifest(ctx, d.DB, fiscalHookManifest("com.second.signer", declared), InstallOptions{})
				if err == nil {
					t.Fatalf("a second plugin declaring %s must be refused while another holds %s (one exclusivity group, ADR-0077 D3)", declared, held)
				}
				if !strings.Contains(err.Error(), "com.owner.signer") || !strings.Contains(err.Error(), declared) {
					t.Fatalf("refusal should name the owning plugin and the declared point, got: %v", err)
				}
				var n int
				if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.second.signer'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 0 {
					t.Fatalf("rejected plugin left %d plugins row(s), want 0", n)
				}
			})
		}
	}
}

// A plugin updating ITSELF never conflicts with its own registration — for
// every event of the group, and for an update that ADDS the other two
// events to a plugin already holding one (the ut-plugin-tax-de upgrade path:
// today's ask-only signer gaining start + reconcile in one release).
func TestPersistManifest_FiscalSignGroupSelfUpdateNotAConflict(t *testing.T) {
	for _, ev := range fiscalSignGroupEvents {
		t.Run(ev, func(t *testing.T) {
			d := openRealDB(t)
			ctx := context.Background()
			if err := PersistManifest(ctx, d.DB, fiscalHookManifest("com.self.signer", ev), InstallOptions{}); err != nil {
				t.Fatalf("install: %v", err)
			}
			upgraded := fiscalHookManifest("com.self.signer", ev)
			upgraded.Version = "1.0.1"
			if err := PersistManifest(ctx, d.DB, upgraded, InstallOptions{}); err != nil {
				t.Fatalf("self-update on %s must not conflict with its own registration: %v", ev, err)
			}
		})
	}
	t.Run("ask-only signer gaining start and reconcile", func(t *testing.T) {
		d := openRealDB(t)
		ctx := context.Background()
		if err := PersistManifest(ctx, d.DB, fiscalHookManifest("com.self.signer", "fiscal.sign.ask"), InstallOptions{}); err != nil {
			t.Fatalf("install: %v", err)
		}
		upgraded := fiscalHookManifest("com.self.signer", "fiscal.sign.ask")
		upgraded.Version = "2.0.0"
		upgraded.Hooks = append(upgraded.Hooks,
			ManifestHook{Event: "fiscal.sign.start", Action: "fiscal.sign"},
			ManifestHook{Event: "fiscal.sign.reconcile.ask", Action: "fiscal.sign"},
		)
		if err := PersistManifest(ctx, d.DB, upgraded, InstallOptions{}); err != nil {
			t.Fatalf("a signer adding the other two group events to its own registration must not conflict with itself: %v", err)
		}
	})
}

// Fail closed holds for the group too: a DB error while checking a
// start-only or reconcile-only manifest refuses the persist with the
// check's own error.
func TestPersistManifest_FiscalSignGroupFailsClosedOnDBError(t *testing.T) {
	for _, ev := range []string{"fiscal.sign.start", "fiscal.sign.reconcile.ask"} {
		t.Run(ev, func(t *testing.T) {
			d := openRealDB(t)
			mustExecSQL(t, d, `DROP TABLE plugin_hooks`)
			err := PersistManifest(context.Background(), d.DB, fiscalHookManifest("com.unlucky.signer", ev), InstallOptions{})
			if err == nil {
				t.Fatal("a DB error during the exclusivity check must refuse the persist (fail closed)")
			}
			if !strings.Contains(err.Error(), "fiscal signing exclusivity") {
				t.Fatalf("expected the exclusivity check's own error, got: %v", err)
			}
		})
	}
}

// The exported group is exactly the three events ADR-0077 D3 names — a
// fourth event silently added here would widen single-ownership enforcement
// without an ADR; a dropped one would silently reopen the gap.
func TestFiscalSignExclusiveEvents_IsExactlyTheADR0077Group(t *testing.T) {
	got := strings.Join(FiscalSignExclusiveEvents, ",")
	want := strings.Join(fiscalSignGroupEvents, ",")
	if got != want {
		t.Fatalf("FiscalSignExclusiveEvents = %q, want %q", got, want)
	}
}
