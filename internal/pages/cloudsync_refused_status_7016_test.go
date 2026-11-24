package pages

import (
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#7016: a code plugin whose listing carries a stale retryable failure
// record (left by an earlier build) must show the refused reason, not
// "Install failed. You can retry.", once this build refuses the install.
func TestCloudInstall_AppStoreRefusalOverwritesStaleRetryableStatus(t *testing.T) {
	_, d, _ := codeGateEnv(t, buildinfo.ChannelAppStore)
	store := plugins.NewInstallStatusStore(d.Db)
	if err := store.Save(t.Context(), plugins.InstallStatusRecord{
		ListingID: "lst-pay", State: plugins.InstallStateFailed,
		MessageKey: "plugins.install.error.retryable", Retryable: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cloudInstallPluginDirective(t.Context(), d, "lst-pay"); err == nil {
		t.Fatal("want a refusal")
	}
	rec, ok, err := store.Get(t.Context(), "lst-pay")
	if err != nil || !ok {
		t.Fatalf("status record: ok=%v err=%v", ok, err)
	}
	if rec.MessageKey != plugins.CodeInstallRefusedMsgID || rec.Retryable || rec.State != plugins.InstallStateFailed {
		t.Fatalf("status = %+v, want failed, key %q, not retryable", rec, plugins.CodeInstallRefusedMsgID)
	}
}
