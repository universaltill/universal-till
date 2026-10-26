package data

// sales_mirror_repo.go (with sales_mirror_repo_copy.go, its additional-till
// side) is the ONE place that reads or writes the sales copy
// (mirror_*), its local state (mirror_state) and the main till's change log
// (sales_mirror_log, sales_mirror_acks) — ut-docs#5074, ADR-0011 and
// ADR-0114 "Amendment 2026-10-26", wire contract ut-docs
// reference/contracts/pos-lan-sales-mirror.md.
//
// The copy is never the sales table, and nothing here returns a type any
// report, Z report, stock, voucher, receipt-number, cloud-upload or journal
// function accepts: MirrorSale is its own type. That is how "a copied sale
// never counts" holds by construction. scripts/ci/guard-sales-mirror-isolation.sh
// fails CI when any other non-test file names one of these tables.

// Settings keys (design §3.3). sales_mirror.* are shop-wide (the admin
// bundle carries them from the main till); sync.sales_mirror_off is per till
// (the "sync." family of PerTillSettingPrefixes).
const (
	SalesMirrorEnabledKey    = "sales_mirror.enabled"
	SalesMirrorEpochKey      = "sales_mirror.epoch"
	SalesMirrorWindowDaysKey = "sales_mirror.window_days"
	SalesMirrorMaxMBKey      = "sales_mirror.max_mb"
	SalesMirrorLogMaxDaysKey = "sales_mirror.log_max_days"
	SalesMirrorLogMaxRowsKey = "sales_mirror.log_max_rows"
	SalesMirrorTillOffKey    = "sync.sales_mirror_off"
)

// Defaults and bounds (design §6, D1, D6).
const (
	SalesMirrorDefaultWindowDays = 400
	SalesMirrorMinWindowDays     = 31
	SalesMirrorMaxWindowDays     = 3660
	SalesMirrorDefaultMaxMB      = 1024
	SalesMirrorDefaultLogMaxDays = 30
	SalesMirrorDefaultLogMaxRows = 250000
)
