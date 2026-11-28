package offsitebackup

import "testing"

// ut-docs#2630: a purged shop is final, so the off-site key is released.
func TestFinalRevokeReasonsIncludesStorePurged(t *testing.T) {
	if !finalRevokeReasons["store_purged"] || finalRevokeReasons["store_deleted"] {
		t.Fatalf("finalRevokeReasons = %v", finalRevokeReasons)
	}
}
