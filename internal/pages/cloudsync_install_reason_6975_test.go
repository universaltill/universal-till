package pages

import (
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#6975: a refused cloud install reports a message KEY and a safe
// message, never the raw error text.
func TestCloudInstallPluginDirective_RefusalCarriesKeyAndSafeMessage(t *testing.T) {
	_, d, _ := codeGateEnv(t, buildinfo.ChannelAppStore)
	_, err := cloudInstallPluginDirective(t.Context(), d, "lst-pay")
	var re *cloudsync.ReasonError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want a ReasonError", err)
	}
	if re.Key != plugins.CodeInstallRefusedMsgID {
		t.Fatalf("key = %q, want %q", re.Key, plugins.CodeInstallRefusedMsgID)
	}
	if re.Error() != "This build of the app cannot install this plugin." {
		t.Fatalf("message must be the classifier's fixed text, got %q", re.Error())
	}
	if !errors.Is(err, plugins.ErrCodePluginRefused) {
		t.Fatal("the cause must stay reachable for errors.Is")
	}
}

// The install_plugin hook the till really wires reports a ReasonError (reverting
// the wiring to cloudInstallPlugin fails this).
func TestBuildCloudHooks_InstallPluginReportsReason(t *testing.T) {
	_, d, _ := codeGateEnv(t, buildinfo.ChannelAppStore)
	hooks := buildCloudHooks(d, nil)
	_, err := hooks.InstallPlugin(t.Context(), "lst-pay")
	var re *cloudsync.ReasonError
	if !errors.As(err, &re) || re.Key == "" {
		t.Fatalf("err = %v, want a ReasonError with a key", err)
	}
}

// A generic failure whose text holds a file path reports the classifier's
// fixed line, never the path: the message of a ReasonError is its Message.
func TestReasonError_MessageNeverCarriesCauseText(t *testing.T) {
	f := plugins.ClassifyInstallError(errors.New("open /Users/x/plugins/tmp/a.part: no such file"), false)
	re := &cloudsync.ReasonError{Key: f.MessageKey, Message: f.Message, Cause: errors.New("open /Users/x/plugins/tmp/a.part")}
	if re.Error() != f.Message || f.MessageKey == "" || f.Message == "" {
		t.Fatalf("reason = %q key = %q", re.Error(), f.MessageKey)
	}
}
