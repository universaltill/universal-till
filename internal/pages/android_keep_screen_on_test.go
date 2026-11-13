package pages

import (
	"os"
	"strings"
	"testing"
)

// ut-docs#5620: the Android till screen slept after a few seconds because the
// shell never asked Android to keep the display on. These pin the pieces that
// fix it, from the Go side where CI always runs (the Kotlin logic has its own
// JVM test, ScreenAwakeLogicTest).
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(rel)
	if err != nil {
		b, err = os.ReadFile("../../" + rel)
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAndroidShellKeepsTheScreenOnWhileInFront(t *testing.T) {
	src := readRepoFile(t, "android/app/src/main/java/com/universaltill/pos/MainActivity.kt")
	for _, want := range []string{"FLAG_KEEP_SCREEN_ON", "startKeepAwake()", "stopKeepAwake()", "fun setScreenAwakeMinutes", "fun getScreenAwakeMinutes"} {
		if !strings.Contains(src, want) {
			t.Errorf("MainActivity.kt lacks %q: the till screen would follow the OS display timeout (ut-docs#5620)", want)
		}
	}
}

func TestSettingsOffersTheScreenAwakeChoiceOnlyThroughTheBridge(t *testing.T) {
	html := readRepoFile(t, "web/ui/pages/settings.html")
	if !strings.Contains(html, `id="screen-awake-form"`) || !strings.Contains(html, "screen-awake.js") {
		t.Fatal("settings.html lacks the keep-screen-on control")
	}
	if !strings.Contains(html, `id="screen-awake-form" class="set-gap" hidden`) {
		t.Error("the control must be hidden until the Android bridge is present")
	}
	js := readRepoFile(t, "web/public/screen-awake.js")
	if !strings.Contains(js, "getScreenAwakeMinutes") || !strings.Contains(js, "setScreenAwakeMinutes") {
		t.Error("screen-awake.js must drive the bridge")
	}
}
