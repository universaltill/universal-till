package plugins

import (
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
)

// ut-docs#7017: CodeRuntimeRefused is the boolean form of RefuseCodeRuntime.
func TestCodeRuntimeRefused_7017(t *testing.T) {
	cases := []struct {
		channel buildinfo.InstallChannel
		runtime string
		want    bool
	}{
		{buildinfo.ChannelAppStore, "wasm", true},
		{buildinfo.ChannelAppStore, "go", true},
		{buildinfo.ChannelAppStore, "none", false},
		{buildinfo.ChannelDirect, "wasm", false},
		{buildinfo.ChannelDirect, "none", false},
	}
	for _, c := range cases {
		restore := buildinfo.OverrideChannelForTest(c.channel)
		got := CodeRuntimeRefused(c.runtime)
		restore()
		if got != c.want {
			t.Errorf("%s/%q = %v, want %v", c.channel, c.runtime, got, c.want)
		}
	}
}
