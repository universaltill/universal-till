package pages

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// recentLines returns the Problems ring entries whose message contains substr.
func recentLines(substr string) []logging.Problem {
	var out []logging.Problem
	for _, p := range logging.Recent() {
		if strings.Contains(p.Msg, substr) {
			out = append(out, p)
		}
	}
	return out
}

// ut-docs#2862: a revoked pairing is keyed, so it clears from the heartbeat
// once the main till accepts this till again (primaryContactOK), while the
// line stays in the log history.
func TestPairingRevoked_ClearsWhenTheMainTillAcceptsTheTillAgain(t *testing.T) {
	dp := newMigratedSyncDeps(t, "revoked-key.db")
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)

	linkPairingRevoked(t.Context(), dp)
	if !heartbeatReportsProblem(t, dp, "no longer accepts this till's pairing") {
		t.Fatalf("a revoked pairing is missing from the heartbeat's problems: %+v", collectProblems(t.Context(), dp))
	}

	primaryContactOK(t.Context(), dp)
	if heartbeatReportsProblem(t, dp, "no longer accepts this till's pairing") {
		t.Fatalf("an accepted-again pairing is still reported: %+v", collectProblems(t.Context(), dp))
	}
	lines := recentLines("no longer accepts this till's pairing")
	if len(lines) != 1 || !lines[0].Resolved {
		t.Fatalf("revoked line should be kept as Resolved history: %+v", logging.Recent())
	}
	if len(recentLines("pairing is accepted again")) != 0 {
		t.Fatal("the recovery INFO must not enter the Problems ring")
	}
}

// ut-docs#2862: a plugin-sync failure repeating every tick warns once per
// listing, is reported, and clears when the install finally succeeds.
func TestConvergePluginSet_InstallFailureWarnsOnceAndClearsOnSuccess(t *testing.T) {
	dp := newMigratedSyncDeps(t, "keyed-install.db")
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)
	stubRecompute(t)
	rows := []data.PluginSyncRow{followRow}

	orig := pluginSyncInstall
	t.Cleanup(func() { pluginSyncInstall = orig })
	var failWith error = errors.New("marketplace down")
	pluginSyncInstall = func(context.Context, *common.Deps, string, string) (string, error) { return "", failWith }

	convergePluginSet(t.Context(), dp, rows)
	convergePluginSet(t.Context(), dp, rows)

	var warns int
	for _, p := range recentLines("failed (will retry)") {
		if p.Level == "WARN" {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("%d WARNs for one repeating install failure, want exactly 1: %+v", warns, logging.Recent())
	}
	if !heartbeatReportsProblem(t, dp, "failed (will retry)") {
		t.Fatalf("a failing install is missing from the heartbeat: %+v", collectProblems(t.Context(), dp))
	}

	failWith = nil
	convergePluginSet(t.Context(), dp, rows)
	if heartbeatReportsProblem(t, dp, "failed (will retry)") {
		t.Fatalf("a recovered install is still reported: %+v", collectProblems(t.Context(), dp))
	}
	lines := recentLines("failed (will retry)")
	if len(lines) != 1 || !lines[0].Resolved {
		t.Fatalf("install failure line should be kept as Resolved history: %+v", logging.Recent())
	}
}

// ut-docs#2862: the primary dropping the listing means the install branch can
// never recover on its own — the keyed problem must not stay open forever.
func TestConvergePluginSet_DroppedListingResolvesItsInstallFailure(t *testing.T) {
	dp := newMigratedSyncDeps(t, "keyed-drop.db")
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)
	stubRecompute(t)
	stubPluginSyncInstall(t, errors.New("marketplace down"))

	convergePluginSet(t.Context(), dp, []data.PluginSyncRow{followRow})
	if !heartbeatReportsProblem(t, dp, "failed (will retry)") {
		t.Fatalf("setup: failing install not reported: %+v", collectProblems(t.Context(), dp))
	}

	convergePluginSet(t.Context(), dp, nil) // the primary no longer lists it
	if heartbeatReportsProblem(t, dp, "failed (will retry)") {
		t.Fatalf("an install failure for a dropped listing is still reported: %+v", collectProblems(t.Context(), dp))
	}
}

// ut-docs#2862: an uninstall that keeps failing warns once per listing, is
// reported, and clears when the uninstall succeeds.
func TestConvergePluginSet_UninstallFailureWarnsOnceAndClearsOnSuccess(t *testing.T) {
	dp := newMigratedSyncDeps(t, "keyed-uninstall.db")
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)
	stubRecompute(t)
	if err := plugins.NewInstallStatusStore(dp.Db).Save(t.Context(), plugins.InstallStatusRecord{
		ListingID: "listing-old", PluginID: "com.test.old", PluginName: "Old",
		CurrentVersion: "1.0.0", State: plugins.InstallStateActive,
	}); err != nil {
		t.Fatalf("seed install status: %v", err)
	}
	orig := pluginSyncRemove
	t.Cleanup(func() { pluginSyncRemove = orig })
	var failWith error = errors.New("files locked")
	pluginSyncRemove = func(context.Context, *common.Deps, string) (string, error) { return "", failWith }

	convergePluginSet(t.Context(), dp, nil)
	convergePluginSet(t.Context(), dp, nil)

	var warns int
	for _, p := range recentLines("uninstall com.test.old") {
		if p.Level == "WARN" {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("%d WARNs for one repeating uninstall failure, want exactly 1: %+v", warns, logging.Recent())
	}
	if !heartbeatReportsProblem(t, dp, "uninstall com.test.old") {
		t.Fatalf("a failing uninstall is missing from the heartbeat: %+v", collectProblems(t.Context(), dp))
	}

	failWith = nil
	convergePluginSet(t.Context(), dp, nil)
	if heartbeatReportsProblem(t, dp, "uninstall com.test.old") {
		t.Fatalf("a recovered uninstall is still reported: %+v", collectProblems(t.Context(), dp))
	}
	lines := recentLines("uninstall com.test.old")
	if len(lines) != 1 || !lines[0].Resolved {
		t.Fatalf("uninstall failure line should be kept as Resolved history: %+v", logging.Recent())
	}
}

// ut-docs#2862 review: a keyed uninstall failure whose install record goes
// away (or stops being Active) without this loop's uninstall — a cloud
// directive removed it, or an install saved it Failed — is no longer a
// pending uninstall, so its problem must close rather than stay open
// forever (keyed problems never age out).
func TestConvergePluginSet_UninstallKeyClearsWhenItsRecordGoes(t *testing.T) {
	dp := newMigratedSyncDeps(t, "keyed-uninstall-gone.db")
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)
	stubRecompute(t)
	store := plugins.NewInstallStatusStore(dp.Db)
	if err := store.Save(t.Context(), plugins.InstallStatusRecord{
		ListingID: "listing-old", PluginID: "com.test.old", PluginName: "Old",
		CurrentVersion: "1.0.0", State: plugins.InstallStateActive,
	}); err != nil {
		t.Fatalf("seed install status: %v", err)
	}
	orig := pluginSyncRemove
	t.Cleanup(func() { pluginSyncRemove = orig })
	pluginSyncRemove = func(context.Context, *common.Deps, string) (string, error) {
		return "", errors.New("files locked")
	}

	convergePluginSet(t.Context(), dp, nil)
	if !heartbeatReportsProblem(t, dp, "uninstall com.test.old") {
		t.Fatalf("a failing uninstall is missing from the heartbeat: %+v", collectProblems(t.Context(), dp))
	}

	if err := store.ClearForPlugin(t.Context(), "com.test.old"); err != nil {
		t.Fatalf("clear install status: %v", err)
	}
	convergePluginSet(t.Context(), dp, nil)
	if heartbeatReportsProblem(t, dp, "uninstall com.test.old") {
		t.Fatalf("an uninstall with no pending record is still reported: %+v", collectProblems(t.Context(), dp))
	}
}

// ut-docs#2862 review: promoting a replica stops the pull loop and the link,
// the only paths that resolve the replica's keyed sync problems — so the
// promote itself closes them, or the new main till would show "Attention
// needed" for its old main till forever.
func TestSyncPromote_ResolvesTheReplicasSyncProblems(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	if err := dp.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("seed replica identity: %v", err)
	}
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)
	linkPairingRevoked(t.Context(), dp)
	logging.L().WarnProblemf(discovery.MainTillProblemKey, "sync: main till unreachable (test)")
	warnPluginSyncOnce(pluginSyncInstallKeyPrefix+"listing-x", "plugin sync: install X failed (test)")
	warnPluginSyncOnce(pluginSyncBrokenKeyPrefix+"listing-x", "plugin sync: X is broken locally (test)")
	warnPluginSyncOnce(pluginSyncUninstallKeyPrefix+"listing-y", "plugin sync: uninstall Y failed (test)")
	if got := len(collectProblems(t.Context(), dp)); got != 5 {
		t.Fatalf("want 5 open problems before promote, got %d: %+v", got, collectProblems(t.Context(), dp))
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sync/promote", strings.NewReader("confirm=PROMOTE"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("promote: %d %s", rec.Code, rec.Body.String())
	}
	if open := collectProblems(t.Context(), dp); len(open) != 0 {
		t.Fatalf("a promoted till still reports its replica sync problems: %+v", open)
	}
}
