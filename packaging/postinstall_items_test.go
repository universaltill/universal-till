package packaging

// Retirement guard for the legacy items symlink (ut-docs#4401).
//
// postinstall.sh used to replace /opt/unitill/web/public/assets/items with a
// symlink onto /var/lib/unitill/items, and paths.migrateLegacyUploadedAssets
// copied pre-2026-07-29 uploads into the data dir. Premise: no supported
// install predates 2026-07-29, so both were retired together. Retirement is
// safe on upgrade too: dpkg keeps an existing symlink and writes the package's
// files through it, so uploaded photos on already-upgraded boxes survive
// (verified with dpkg 1.22.6). This test fails if either bridge comes back.

import (
	"strings"
	"testing"
)

func TestPostinstallLegacyItemsSymlinkRetired(t *testing.T) {
	post := readScript(t, "packaging/scripts/postinstall.sh")

	if anyLineContains(codeLines(post), "ln -sfn /var/lib/unitill/items") {
		t.Error("postinstall.sh recreates the legacy items symlink; it was retired in ut-docs#4401")
	}
	if strings.Contains(post, "migrateLegacyUploadedAssets") {
		t.Error("postinstall.sh references the retired migrateLegacyUploadedAssets (ut-docs#4401)")
	}
}
