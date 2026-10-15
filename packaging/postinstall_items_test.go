package packaging

// Pins WHY postinstall.sh still keeps /opt/unitill/web/public/assets/items
// on /var/lib/unitill/items behind a symlink (ut-docs#256).
//
// The dance looks vestigial — item photo uploads now go to the stable data
// dir (paths.Data("public","assets","items",...)) — but it is NOT: boxes
// installed before 2026-07-29 hold the shop's uploaded photos behind that
// symlink, and paths.migrateLegacyUploadedAssets reads web/public/assets/items
// on the first start after an upgrade to copy them into the data dir. Removing
// the block before that migration could run would strand those photos. This
// test keeps the behaviour AND the explanation from drifting apart: a future
// "cleanup" has to read the reason (and retire the migration first) instead
// of trusting a stale comment.

import (
	"strings"
	"testing"
)

func TestPostinstallItemsSymlinkIsKeptAndExplained(t *testing.T) {
	post := readScript(t, "packaging/scripts/postinstall.sh")

	// Behaviour: the symlink step is still there (non-comment lines only).
	code := codeLines(post)
	if !anyLineContains(code, "ln -sfn /var/lib/unitill/items", "$ITEMS") {
		t.Error("postinstall.sh no longer symlinks web/public/assets/items onto /var/lib/unitill/items; " +
			"retire paths.migrateLegacyUploadedAssets and the legacy-upload bridge first (ut-docs#256)")
	}

	// Explanation: the comment names the real reason, not the stale one.
	if strings.Contains(post, "item photos INTO") {
		t.Error("postinstall.sh still carries the stale claim that shops upload item photos into web/public/assets/items; " +
			"uploads go to the data dir since 2026-07-29 (ut-docs#256)")
	}
	for _, want := range []string{"migrateLegacyUploadedAssets", "paths.Data"} {
		if !strings.Contains(post, want) {
			t.Errorf("postinstall.sh comment must explain the symlink by naming %q (ut-docs#256)", want)
		}
	}
}
