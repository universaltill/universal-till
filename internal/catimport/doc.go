// Package catimport holds the till-only importers that build on the shared
// catalogue parser module github.com/universaltill/universal-till/catimport
// (ADR-0164 §1, ut-docs#4525): ParseBkp (a .bkp backup — it needs SQLite,
// internal/data and internal/stagedupload, so it can't run in the cloud),
// ParseVoucherBalances (gift-voucher opening balances), and the built-in
// category icons and placeholders (internal/iconid). CSV/XLSX catalogue
// parsing lives only in the module; scripts/ci/guard-catimport-module.sh
// fails if it comes back here. Like the module, this package carries no
// SQL text of its own: ParseBkp's queries live in internal/data.
package catimport
