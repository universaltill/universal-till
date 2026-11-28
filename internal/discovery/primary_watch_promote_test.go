package discovery

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// A promote (ClearReplicaIdentity) that lands while PrimaryWatch is browsing
// or proving must not be undone by the write that follows (ut-docs#7209).

const promotedLine = "was promoted"

// promoteHook promotes f's till when called.
func promoteHook(t *testing.T, f *watchFixture) func() {
	t.Helper()
	return func() {
		if err := f.settings.ClearReplicaIdentity(context.Background()); err != nil {
			t.Errorf("promote: %v", err)
		}
	}
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)
	var buf lockedBuf
	restore := logging.CaptureForTest(&buf)
	t.Cleanup(restore)
	return &buf.Buffer
}

// lockedBuf is a concurrency-safe writer whose Buffer is read after the call.
type lockedBuf struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func requireAbsent(t *testing.T, f *watchFixture, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if got := f.get(t, k); got != "" {
			t.Errorf("%s = %q after the promote; want absent", k, got)
		}
	}
}

func requireNotReplica(t *testing.T, f *watchFixture) {
	t.Helper()
	if ok, err := f.settings.IsReplica(context.Background()); err != nil || ok {
		t.Errorf("IsReplica = %v, %v; the promoted till must stay the primary", ok, err)
	}
}

func requireOnePromotedInfo(t *testing.T, logs *bytes.Buffer) {
	t.Helper()
	if n := strings.Count(logs.String(), promotedLine); n != 1 {
		t.Errorf("%d %q INFO lines, want exactly 1; log:\n%s", n, promotedLine, logs.String())
	}
	for _, p := range logging.Recent() {
		t.Errorf("unexpected WARN/ERROR problem: %s", p.Msg)
	}
}

func TestPrimaryWatch_PromotedDuringRediscoverProofStaysPromoted_7209(t *testing.T) {
	logs := captureLogs(t)
	p := newFakePrimary(t, testPrimaryID, testBearerHash())
	f := newWatchFixture(t, Candidate{TillID: testPrimaryID, BaseURL: p.srv.URL})
	p.beforeReply = promoteHook(t, f)

	out := relink(t, f)
	if out.Relinked || out.Warn {
		t.Errorf("outcome %+v; a promoted till must neither relink nor warn", out)
	}
	requireAbsent(t, f, "sync.primary_url", PrimaryTillIDSettingKey, PrimaryCertPinSettingKey, PrimaryCertNextPinSettingKey)
	requireNotReplica(t, f)
	requireOnePromotedInfo(t, logs)
}

func TestPrimaryWatch_PromotedDuringPinnedRediscoverWritesNoPin_7209(t *testing.T) {
	logs := captureLogs(t)
	p := newTLSFakePrimary(t, testPrimaryID, testBearerHash())
	f := newWatchFixture(t, Candidate{TillID: testPrimaryID, BaseURL: p.base})
	ctx := context.Background()
	for k, v := range map[string]string{
		PrimaryCertPinSettingKey:     strings.Repeat("0", 64),
		PrimaryCertNextPinSettingKey: strings.Repeat("1", 64),
	} {
		if err := f.settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	p.beforeReply = promoteHook(t, f)

	out := relink(t, f)
	if out.Relinked || out.Warn {
		t.Errorf("outcome %+v; want neither relink nor warn", out)
	}
	requireAbsent(t, f, "sync.primary_url", PrimaryTillIDSettingKey, PrimaryCertPinSettingKey, PrimaryCertNextPinSettingKey)
	requireNotReplica(t, f)
	requireOnePromotedInfo(t, logs)
}

func TestPrimaryWatch_PromotedDuringContactOKBackfillStoresNothing_7209(t *testing.T) {
	logs := captureLogs(t)
	p := newTLSFakePrimary(t, testPrimaryID, testBearerHash())
	f := newWatchFixture(t)
	ctx := context.Background()
	if err := f.settings.Set(ctx, "sync.primary_url", p.base); err != nil {
		t.Fatal(err)
	}
	if err := f.settings.Set(ctx, PrimaryTillIDSettingKey, ""); err != nil {
		t.Fatal(err)
	}
	p.beforeReply = promoteHook(t, f)

	f.w.ContactOK(ctx)
	requireAbsent(t, f, "sync.primary_url", PrimaryTillIDSettingKey, PrimaryCertPinSettingKey, PrimaryCertNextPinSettingKey)
	requireNotReplica(t, f)
	requireOnePromotedInfo(t, logs)
}

// promoteOnGet promotes right after the nth read of key, still returning
// what that read saw.
type promoteOnGet struct {
	*data.SettingsRepo
	key     string
	n, seen int
}

func (p *promoteOnGet) Get(ctx context.Context, key string) (string, bool, error) {
	v, ok, err := p.SettingsRepo.Get(ctx, key)
	if key == p.key {
		p.seen++
		if p.seen == p.n {
			if cerr := p.SettingsRepo.ClearReplicaIdentity(ctx); cerr != nil {
				return v, ok, cerr
			}
		}
	}
	return v, ok, err
}

func TestPrimaryWatch_PromotedAfterReproofRecheckAdoptsNothing_7209(t *testing.T) {
	logs := captureLogs(t)
	p := newTLSFakePrimary(t, testPrimaryID, testBearerHash())
	old := strings.Repeat("0", 64)
	f := reproofFixture(t, p.base, old)
	ctx := context.Background()
	if err := f.settings.Set(ctx, PrimaryCertNextPinSettingKey, strings.Repeat("1", 64)); err != nil {
		t.Fatal(err)
	}
	// Reads of the held pin: entry check (1), the challenge (2), post-proof re-check (3).
	// Promote straight after the re-check, before the write.
	f.w = NewPrimaryWatch(&promoteOnGet{SettingsRepo: f.settings, key: PrimaryCertPinSettingKey, n: 3}, nil)
	f.w.now = func() time.Time { return f.now }

	if err := f.w.Reproof(ctx, p.base, old); err != nil {
		t.Fatalf("Reproof = %v; a promote is no refused proof", err)
	}
	requireAbsent(t, f, "sync.primary_url", PrimaryTillIDSettingKey, PrimaryCertPinSettingKey, PrimaryCertNextPinSettingKey)
	requireNotReplica(t, f)
	if !f.w.lastReproofFail.IsZero() {
		t.Error("a promote started the re-proof cooldown")
	}
	requireOnePromotedInfo(t, logs)
}

// Regression (already covered by Reproof's post-proof re-check): a promote
// during the proof itself.
func TestPrimaryWatch_PromotedDuringReproofProof_7209(t *testing.T) {
	p := newTLSFakePrimary(t, testPrimaryID, testBearerHash())
	old := strings.Repeat("0", 64)
	f := reproofFixture(t, p.base, old)
	p.beforeReply = promoteHook(t, f)

	if err := f.w.Reproof(context.Background(), p.base, old); err != nil {
		t.Fatalf("Reproof = %v", err)
	}
	requireAbsent(t, f, "sync.primary_url", PrimaryCertPinSettingKey, PrimaryCertNextPinSettingKey)
	if !f.w.lastReproofFail.IsZero() {
		t.Error("a promote started the re-proof cooldown")
	}
}

// recordingSettings records each SetIfReplica call.
type recordingSettings struct {
	*data.SettingsRepo
	calls []map[string]string
}

func (r *recordingSettings) SetIfReplica(ctx context.Context, kv map[string]string) error {
	cp := make(map[string]string, len(kv))
	for k, v := range kv {
		cp[k] = v
	}
	r.calls = append(r.calls, cp)
	return r.SettingsRepo.SetIfReplica(ctx, kv)
}

// AC6: a relink is one replica-only transaction, not a series of writes.
func TestPrimaryWatch_RelinkIsOneReplicaOnlyWrite_7209(t *testing.T) {
	p := newTLSFakePrimary(t, testPrimaryID, testBearerHash())
	f := newWatchFixture(t, Candidate{TillID: testPrimaryID, BaseURL: p.base})
	ctx := context.Background()
	if err := f.settings.Set(ctx, PrimaryCertPinSettingKey, strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	rec := &recordingSettings{SettingsRepo: f.settings}
	f.w.settings = rec

	if out := relink(t, f); !out.Relinked {
		t.Fatalf("outcome %+v; want a relink", out)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("%d SetIfReplica calls %v; want exactly 1", len(rec.calls), rec.calls)
	}
	want := map[string]string{
		"sync.primary_url":           p.base,
		PrimaryTillIDSettingKey:      testPrimaryID,
		PrimaryCertPinSettingKey:     p.cert.Pin(),
		PrimaryCertNextPinSettingKey: "",
	}
	got := rec.calls[0]
	if len(got) != len(want) {
		t.Fatalf("keys %v; want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q; want %q", k, got[k], v)
		}
	}
	if f.get(t, "sync.primary_url") != p.base || f.get(t, PrimaryCertPinSettingKey) != p.cert.Pin() {
		t.Error("the relink was not applied on a till that stays a replica")
	}
}
