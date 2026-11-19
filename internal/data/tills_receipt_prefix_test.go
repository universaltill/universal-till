package data

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"testing"
)

// ut-docs#5058 UT-5: the main till records the receipt prefix it grants and
// knows every prefix in use — granted, or carried by a sale it holds.
func TestTillsRepo_ReceiptPrefixesInUse(t *testing.T) {
	d := openMigratedDB(t, "tills-prefix.db")
	ctx := context.Background()
	repo := NewTillsRepo(d.DB)
	base := syncAdminGeneration(t, d)
	if _, _, err := enrolTill(repo, ctx, "Till 4", "hash-4", TillRoleAdditional, "T4-"); err != nil {
		t.Fatal(err)
	}
	if syncAdminGeneration(t, d) == base {
		t.Fatal("a granted prefix did not bump the admin generation: replicas would keep a stale roster")
	}
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at) VALUES ('s1', 'T7-000000001', 'completed', 'sale', 'GBP', 1, 1, '2026-10-26T09:00:00Z')`)
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at) VALUES ('s2', '000000005', 'completed', 'sale', 'GBP', 1, 1, '2026-10-26T09:00:00Z')`)
	got, err := receiptPrefixesInUse(ctx, repo.db)
	if err != nil {
		t.Fatal(err)
	}
	if !got["T4-"] || !got["T7-"] || len(got) != 2 {
		t.Fatalf("prefixes in use = %v, want T4- (granted) and T7- (a sale), not the main till's own unprefixed numbers", got)
	}
}

func settingValue(t *testing.T, ctx context.Context, repo *TillsRepo, key string) string {
	t.Helper()
	var v string
	_ = repo.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	return v
}

// ut-docs#5074 S0: the record of granted numbers is raised in the same
// transaction as the till row, and travels (a settings write bumps the
// admin generation, so every till — and a later promoted main till — gets it).
func TestEnrolTill_RecordsTheHighWaterAndRevokeDoesNotLowerIt(t *testing.T) {
	d := openMigratedDB(t, "tills-hw.db")
	ctx := context.Background()
	repo := NewTillsRepo(d.DB)
	a, n, err := enrolTill(repo, ctx, "Till 2", "h2", TillRoleAdditional, "")
	if err != nil || n != 2 {
		t.Fatalf("first till = %d, %v; want 2", n, err)
	}
	if v := settingValue(t, ctx, repo, TillNoHighWaterSettingsKey); v != "2" {
		t.Fatalf("%s = %q, want 2", TillNoHighWaterSettingsKey, v)
	}
	if err := repo.DeleteTill(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, n, err = enrolTill(repo, ctx, "Till 3", "h3", TillRoleSatellite, ""); err != nil || n != 3 {
		t.Fatalf("after revoke = %d, %v; want 3", n, err)
	}
	// A re-joining till's own number below the record is kept (it is its
	// own) and does not lower the record.
	if _, n, err = enrolTill(repo, ctx, "Till 2 again", "h2b", TillRoleAdditional, "T2-"); err != nil || n != 2 {
		t.Fatalf("re-join with T2- = %d, %v; want 2", n, err)
	}
	if v := settingValue(t, ctx, repo, TillNoHighWaterSettingsKey); v != "3" {
		t.Fatalf("%s = %q, want 3", TillNoHighWaterSettingsKey, v)
	}
	// An invalid or main-till prefix is ignored.
	for _, bad := range []string{"T1-", "T0-", "X2-", "T10000-", "T03-"} {
		if _, n, err = enrolTill(repo, ctx, "Till "+bad, "h-"+bad, TillRoleAdditional, bad); err != nil || n <= 3 {
			t.Fatalf("requested %q = %d, %v; want a fresh number", bad, n, err)
		}
	}
}

// Tills enrolling at the same time never share a number: the allocation
// and the record are one write transaction.
func TestEnrolTill_ConcurrentEnrolmentsGetDistinctNumbers(t *testing.T) {
	d := openMigratedDB(t, "tills-concurrent.db")
	ctx := context.Background()
	repo := NewTillsRepo(d.DB)
	const tills = 12
	var wg sync.WaitGroup
	nums := make(chan int, tills)
	errs := make(chan error, tills)
	for i := 0; i < tills; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, n, err := enrolTill(repo, ctx, fmt.Sprintf("Till %d", i), fmt.Sprintf("h%d", i), TillRoleAdditional, "")
			if err != nil {
				errs <- err
				return
			}
			nums <- n
		}(i)
	}
	wg.Wait()
	close(nums)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for n := range nums {
		if seen[n] {
			t.Fatalf("number %d granted twice", n)
		}
		seen[n] = true
	}
	if len(seen) != tills {
		t.Fatalf("granted %d numbers, want %d", len(seen), tills)
	}
}

// An existing shop (tills enrolled before migration 092, so no recorded
// prefix, and sales under T2- and T3-) upgrades: the next till numbers past
// all of it, and no existing receipt number changes.
func TestEnrolTill_OnAnExistingShopKeepsEveryReceiptAndNumbersPastThem(t *testing.T) {
	d := openMigratedDB(t, "tills-legacy.db")
	ctx := context.Background()
	repo := NewTillsRepo(d.DB)
	mustExec(t, d, `INSERT INTO tills (id, name, bearer_hash, role) VALUES ('old2', 'Till 2', 'x', 'additional'), ('old3', 'Till 3', 'y', 'additional')`)
	for i, rn := range []string{"000000001", "000000002", "T2-000000001", "T2-000000002", "T3-000000001"} {
		mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at) VALUES (?, ?, 'completed', 'sale', 'GBP', 1, 1, '2026-10-26T09:00:00Z')`, fmt.Sprintf("s%d", i), rn)
	}
	before := allReceiptNos(t, repo)
	_, n, err := enrolTill(repo, ctx, "Till 4", "h4", TillRoleAdditional, "")
	if err != nil || n != 4 {
		t.Fatalf("next till on an existing shop = %d, %v; want 4", n, err)
	}
	// A re-joining legacy till asking for T2- (its sales are here) is not
	// given it again; it gets a fresh number.
	if _, n, err = enrolTill(repo, ctx, "Till 2 again", "h2", TillRoleAdditional, "T2-"); err != nil || n != 5 {
		t.Fatalf("legacy T2- re-join = %d, %v; want 5", n, err)
	}
	if after := allReceiptNos(t, repo); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("receipt numbers changed: %v -> %v", before, after)
	}
	var old2, old3 string
	_ = repo.db.QueryRow(`SELECT receipt_prefix FROM tills WHERE id = 'old2'`).Scan(&old2)
	_ = repo.db.QueryRow(`SELECT receipt_prefix FROM tills WHERE id = 'old3'`).Scan(&old3)
	if old2 != "" || old3 != "" {
		t.Fatalf("existing tills' rows were rewritten: %q %q", old2, old3)
	}
}

func allReceiptNos(t *testing.T, repo *TillsRepo) []string {
	t.Helper()
	rows, err := repo.db.Query(`SELECT id || '=' || receipt_no FROM sales ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// The record travels to every till in the admin bundle, so a till promoted
// to main till later continues the shop's numbers (ut-docs#5074 S0).
func TestEnrolTill_HighWaterTravelsInTheAdminBundle(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "hw-primary.db")
	replica := openReplicaDB(t, "hw-replica.db")
	if _, _, err := enrolTill(NewTillsRepo(primary.DB), ctx, "Till 2", "h2", TillRoleAdditional, ""); err != nil {
		t.Fatal(err)
	}
	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatal(err)
	}
	if v := settingValue(t, ctx, NewTillsRepo(replica.DB), TillNoHighWaterSettingsKey); v != "2" {
		t.Fatalf("replica's %s = %q after the admin pull, want 2", TillNoHighWaterSettingsKey, v)
	}
}

// ut-docs#5074 design §3.6: a prefix seen only in this till's read-only sales
// copy (another till's sales, mirror_sales) is taken too — a promoted till
// that holds the copy must never hand that prefix out again.
func TestTillsRepo_ReceiptPrefixesInUseIncludesTheSalesCopy(t *testing.T) {
	d := openMigratedDB(t, "tills-prefix-copy.db")
	ctx := context.Background()
	mustExec(t, d, `INSERT INTO mirror_sales (id, src_seq, epoch, origin_till_id, receipt_no, status, sale_type, tender_type,
  currency, subtotal, discount_total, tax_total, total, rounding, service_charge_amount, service_charge_tax_basis_bp,
  voucher_issue_total, offline, created_at, local_date, received_at)
  VALUES ('m1', 1, 'e', 't9', 'T9-000000003', 'completed', 'sale', 'cash', 'GBP', 1, 0, 0, 1, 0, 0, 0, 0, 0, 'x', '2026-10-26', 'x'),
         ('m2', 2, 'e', 'main', '000000004', 'completed', 'sale', 'cash', 'GBP', 1, 0, 0, 1, 0, 0, 0, 0, 0, 'x', '2026-10-26', 'x')`)
	got, err := receiptPrefixesInUse(ctx, d.DB)
	if err != nil {
		t.Fatal(err)
	}
	if !got["T9-"] || len(got) != 1 {
		t.Fatalf("prefixes in use = %v, want T9- from the sales copy only", got)
	}
	// The enrolment that follows numbers past it.
	_, no, err := enrolTill(NewTillsRepo(d.DB), ctx, "Till X", "hash-x", TillRoleAdditional, "")
	if err != nil {
		t.Fatal(err)
	}
	if no <= 9 {
		t.Fatalf("new till number %d reuses or precedes T9- seen only in the copy", no)
	}
}

// ut-docs#5162: a restored copy of a joined till asks to keep a provisional
// R<4 hex>- prefix. It is no T<n>-, so the main till grants a fresh number
// instead, and two copies of one till can never share one.
func TestEnrolTill_ProvisionalRestorePrefixGetsANewTillNo(t *testing.T) {
	d := openMigratedDB(t, "tills-restored.db")
	ctx := context.Background()
	repo := NewTillsRepo(d.DB)
	if _, n, err := enrolTill(repo, ctx, "Till 2", "h2", TillRoleAdditional, ""); err != nil || n != 2 {
		t.Fatalf("first till = %d, %v; want 2", n, err)
	}
	for _, p := range []string{"R1a2b-", "Rffff-"} {
		id, n, err := enrolTill(repo, ctx, "Restored copy "+p, "h-"+p, TillRoleAdditional, p)
		if err != nil || n < 3 {
			t.Fatalf("requested %q = %d, %v; want a fresh T<n>- (n >= 3)", p, n, err)
		}
		_ = id
	}
}

// ut-docs#5058 T4: a replacement main till must not reuse the dead till's
// receipt (and invoice) numbers.
func TestReplacementMainTillPrefix_NeverEqualsTheReplacedTill(t *testing.T) {
	shape := regexp.MustCompile(`^M[0-9a-f]{8}-$`)
	restored := regexp.MustCompile(`^R[0-9a-f]{4}-$`)
	for _, tc := range []struct{ newID, oldID string }{
		{"till-3f2a9c1e-0000-4000-8000-000000000001", "till-aaaaaaaa-0000-4000-8000-000000000001"},
		{"till-3f2a9c1e-0000-4000-8000-000000000001", "till-3f2a0000-1111-4000-8000-000000000002"}, // same first 4 hex
		{"till-3f2a9c1e-0000-4000-8000-000000000001", "till-3f2a9c1e-0000-4000-8000-000000000002"}, // same first 8 hex
		{"till-3f2a9c1e-0000-4000-8000-000000000001", "main-till"},                                 // legacy / non-uuid old id
		{"till-3f2a9c1e-0000-4000-8000-000000000001", ""},
	} {
		got := ReplacementMainTillPrefix(tc.newID, tc.oldID)
		if !shape.MatchString(got) {
			t.Errorf("(%q,%q) = %q, want M<8 hex>-", tc.newID, tc.oldID, got)
		}
		if got == "" || restored.MatchString(got) || tillNoOf(got) != 0 {
			t.Errorf("(%q,%q) = %q collides with the main/restored/T<n> prefixes", tc.newID, tc.oldID, got)
		}
		// The old till's own prefix, if it was itself a replacement.
		if got == ReplacementMainTillPrefix(tc.oldID, "") {
			t.Errorf("(%q,%q) = %q equals the old till's own prefix", tc.newID, tc.oldID, got)
		}
		if again := ReplacementMainTillPrefix(tc.newID, tc.oldID); again != got {
			t.Errorf("not deterministic: %q vs %q", got, again)
		}
	}
}

// ut-docs#5058 review F2: always 8 hex, and the matcher agrees.
func TestReplacementMainTillPrefix_AlwaysEightHex(t *testing.T) {
	got := ReplacementMainTillPrefix("till-3f2a9c1e-0000-4000-8000-000000000001", "till-aaaaaaaa-0000-4000-8000-000000000001")
	if got != "M3f2a9c1e-" {
		t.Errorf("got %q, want M3f2a9c1e-", got)
	}
	// Same first 8 hex as the replaced till: next 8 hex chars.
	got = ReplacementMainTillPrefix("till-3f2a9c1e-0000-4000-8000-000000000001", "till-3f2a9c1e-0000-4000-8000-000000000002")
	if got != "M00004000-" {
		t.Errorf("fallback got %q, want M00004000-", got)
	}
	for _, p := range []string{got, "M3f2a9c1e-"} {
		if !IsReplacementMainTillPrefix(p) {
			t.Errorf("IsReplacementMainTillPrefix(%q) = false", p)
		}
	}
	for _, p := range []string{"", "T2-", "R1a2b-", "M3f2a-", "M3F2A9C1E-", "M3f2a9c1e", "M3f2a9c1e-1", "Mzzzzzzzz-"} {
		if IsReplacementMainTillPrefix(p) {
			t.Errorf("IsReplacementMainTillPrefix(%q) = true", p)
		}
	}
}

// Documents the collision T4 fixes: a fresh install has no sync.* rows, so
// its first receipt is the same 000000001 a replaced original main till had.
func TestNextReceiptNo_FreshInstallHasNoPrefix(t *testing.T) {
	d := openMigratedDB(t, "fresh-receipt.db")
	got, err := NewPOSRepo(d.DB).NextReceiptNo(context.Background(), nil)
	if err != nil || got != "000000001" {
		t.Fatalf("NextReceiptNo = %q, %v; want 000000001", got, err)
	}
}

// ut-docs#6663: the main till grants plain <n>- prefixes (2-, 3-, ...) in
// the SAME never-reused number space as the legacy T<n>-.
func TestEnrolTillPrefix_GrantsPlainNumbers(t *testing.T) {
	d := openMigratedDB(t, "tills-plain.db")
	ctx := context.Background()
	repo := NewTillsRepo(d.DB)
	for want := 2; want <= 4; want++ {
		_, n, prefix, err := repo.EnrolTillPrefix(ctx, fmt.Sprintf("Till %d", want), fmt.Sprintf("h%d", want), TillRoleAdditional, "")
		if err != nil || n != want || prefix != fmt.Sprintf("%d-", want) {
			t.Fatalf("till %d = %d, %q, %v; want %d-", want, n, prefix, err, want)
		}
		var stored string
		_ = repo.db.QueryRow(`SELECT receipt_prefix FROM tills WHERE name = ?`, fmt.Sprintf("Till %d", want)).Scan(&stored)
		if stored != prefix {
			t.Fatalf("stored prefix %q, granted %q", stored, prefix)
		}
	}
}

func TestEnrolTillPrefix_ShopWithLegacyT2GrantsPlain3(t *testing.T) {
	d := openMigratedDB(t, "tills-legacy-t2.db")
	ctx := context.Background()
	repo := NewTillsRepo(d.DB)
	mustExec(t, d, `INSERT INTO tills (id, name, bearer_hash, role, receipt_prefix) VALUES ('old2', 'Till 2', 'x', 'additional', 'T2-')`)
	_, n, prefix, err := repo.EnrolTillPrefix(ctx, "Till 3", "h3", TillRoleAdditional, "")
	if err != nil || n != 3 || prefix != "3-" {
		t.Fatalf("got %d, %q, %v; want 3, 3-", n, prefix, err)
	}
}

func TestEnrolTillPrefix_RevokedNumberNeverRegrantedAcrossForms(t *testing.T) {
	d := openMigratedDB(t, "tills-revoked-forms.db")
	ctx := context.Background()
	repo := NewTillsRepo(d.DB)
	id, n, _, err := repo.EnrolTillPrefix(ctx, "Till 2", "h2", TillRoleAdditional, "")
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if err := repo.DeleteTill(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, n, prefix, err := repo.EnrolTillPrefix(ctx, "Till x", "hx", TillRoleAdditional, ""); err != nil || n != 3 || prefix != "3-" {
		t.Fatalf("after revoke = %d, %q, %v; want 3-", n, prefix, err)
	}
	// A sale under the revoked 2- keeps the number taken for re-joiners too.
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at) VALUES ('s2', '2-000000001', 'completed', 'sale', 'GBP', 1, 1, '2026-10-26T09:00:00Z')`)
	if _, n, _, err := repo.EnrolTillPrefix(ctx, "Other", "ho", TillRoleAdditional, "2-"); err != nil || n == 2 {
		t.Fatalf("a till asking for 2- (in use by a sale) got %d, %v", n, err)
	}
}

func TestEnrolTillPrefix_RejoinKeepsItsOwnPrefixForm(t *testing.T) {
	d := openMigratedDB(t, "tills-rejoin-form.db")
	ctx := context.Background()
	repo := NewTillsRepo(d.DB)
	for _, want := range []string{"T5-", "6-"} {
		_, _, prefix, err := repo.EnrolTillPrefix(ctx, "re"+want, "h"+want, TillRoleAdditional, want)
		if err != nil || prefix != want {
			t.Fatalf("re-join with %q = %q, %v", want, prefix, err)
		}
	}
	// 1- is the main till's: never granted to a joiner.
	if _, n, _, err := repo.EnrolTillPrefix(ctx, "one", "h1", TillRoleAdditional, "1-"); err != nil || n == 1 {
		t.Fatalf("1- granted to a joiner: %d, %v", n, err)
	}
}

func TestTillNumberOfPrefix(t *testing.T) {
	for p, want := range map[string]int{"": 1, "1-": 1, "2-": 2, "T2-": 2, "12-": 12, "T10-": 10, "A-": 1, "AB-": 1, "M3f2a9c1e-": 1, "R1a2b-": 1, "T1-": 1, "0-": 1, "garbage": 1} {
		if got := TillNumberOfPrefix(p); got != want {
			t.Errorf("TillNumberOfPrefix(%q) = %d, want %d", p, got, want)
		}
	}
}

func TestIsCloudSeriesPrefix(t *testing.T) {
	for p, want := range map[string]bool{"A-": true, "Z-": true, "AA-": true, "ZZZ-": true, "AAAA-": false, "": false, "I-": false, "O-": false,
		"M-": false, "R-": false, "T-": false, "a-": false, "1-": false, "A": false, "A-1": false, "A--": false} {
		if got := IsCloudSeriesPrefix(p); got != want {
			t.Errorf("IsCloudSeriesPrefix(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestReceiptPrefixesInUse_CountsPlainNumberPrefixes(t *testing.T) {
	d := openMigratedDB(t, "tills-prefix-plain.db")
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at) VALUES ('s1', '7-000000001', 'completed', 'sale', 'GBP', 1, 1, '2026-10-26T09:00:00Z')`)
	got, err := receiptPrefixesInUse(context.Background(), d.DB)
	if err != nil || !got["7-"] {
		t.Fatalf("in use = %v, %v; want 7-", got, err)
	}
}

// enrolTill is EnrolTillPrefix without the granted prefix, for tests that
// only need the id and number.
func enrolTill(r *TillsRepo, ctx context.Context, name, bearerHash, role, requestedPrefix string) (string, int, error) {
	id, n, _, err := r.EnrolTillPrefix(ctx, name, bearerHash, role, requestedPrefix)
	return id, n, err
}
