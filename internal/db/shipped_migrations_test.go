package db

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// shippedMigrationChecksums pins the migrationChecksum of every migration
// file that has merged to main, as LITERALS — deliberately not computed
// from the files at test time, which would be a tautology.
//
// Why (ut-docs#2395, ADR-0100): on 2026-09-17 ut-docs#2312 added a
// permission by editing the already-applied baseline 001_init.sql. It
// followed the written rule of the day ("001 may still be edited freely"),
// passed review and CI, shipped in v0.19.0–v0.19.2 — and bricked every
// upgrading till at boot, because the runtime ledger guard
// (verifyAppliedMigrations, ADR-0074 Decision 3) correctly refused a file
// whose statements no longer matched what the ledger recorded. The runtime
// guard only fires on the device; THIS test fires in CI, on the PR that
// makes the edit.
//
// Every file under internal/db/migrations/ is frozen the moment it merges.
// Adding a migration is therefore two edits: the new NNN_*.sql, and its
// checksum line here (compute it with migrationChecksum — e.g. run this
// test and copy the "got" value from the failure). Comment-only edits do
// not change migrationChecksum and stay allowed
// (TestMigrationChecksum_IgnoresCommentsNotStatements below proves it).
//
// Version 1's value is the checksum every till that installed
// v0.1.0…v0.18.0 carries in its schema_migrations ledger — the one real
// device logs showed when v0.19.0 refused to boot. It must never move.
var shippedMigrationChecksums = map[int]string{
	1:  "ee6f0a910e4259cea503aeddb845c169e82e0d34182ef637f191c9ae1ae71b21",
	2:  "ddce349901fcf378887c14585cfef0701c81d11e14e63db7070abf5c7d07dd5c",
	3:  "7d5ef26d514a0a8572486779656df5baa1c37e45e623c2243bb75c8b5e76c842",
	4:  "7dc582689c2700049073371e8110acd168782116e621f284eeb5214394b38796",
	5:  "5f6e803e5c0dc5fa5d9261bafa1df6d9adc2b52d48b495ae37882a45eac4b404",
	6:  "055dd0d6710c859d92a3288f00a6edd117c51bb7271d9da5c7f9b834c2f99389",
	7:  "468481c7add4395d53052e9c57ed27daf8e1dfc95bc2cc198825c7aa807cc6c1",
	8:  "1c635f68a58c3fa292788728f4bcd7baf853d50cc5a785531a55d4ddfba74ceb",
	9:  "597f4eca9c3848f6fcd205607ffa8eaecf1b25421e1020a848552ad3ddc67717",
	10: "52a9509b7b702f1cb2a53f4adee021e8b5c63a89889a7af204e4346fb3048408",
	11: "3a96499192e3f297bdcc616574372a8f92a7fbaae5d3fb154cce63dd8f0d2403",
	12: "82ccb62a05031222df5cbb0d8d1ddf27cb899321666036730962107671666664",
	13: "21f4a46c9362fbfacc6df2d7aa188c793500e7c00250fe7c8d66384b71444de1",
	14: "dde687b9200dab2fa39d99077acc1268f6d0a79698c96fcc0bcd99977ceb5992",
	15: "e25173663ca4793fac695bf8ff82cfe5222b8c2a3abeda394d8c1dbf1714dc2d",
	16: "a8cd627f462dc92c00d2e95f6206665cf60fe61604565109ee7270e7daf35c92",
	17: "8e0d71ff554ea857c1c9bfcef4bfe557b54a4b970826e5dbb5e862032b928950",
	18: "5dd4ca2db835841a25fe863665bcd5758e6fb5901322e92f355cbbaac7a4e3b6",
	19: "2c628ae375577bfe3487f9976c374c9a05fb39fe813cb485ad5bff50c3f70ced",
	20: "da5ae1d5d4f46ae70eca368354633f99945f25ce9783a73990b1e1f2b9e54446",
	21: "3e2dad7c12dc2e30a20ad57cd8a0deddf87a8c8a323b85b7f9a0fd0fc2686bcc",
	22: "d5f43944936e135585d55ff659757f2908256a571212c0857cc9a2fb3b5832ac",
	23: "3b958e5766162c85df7941ae502b4346a50c9ef3017cb418194f8d0d6db17937",
	24: "3b3dcb892c2e1de2a9b156a0cd8ba7b63b2fff61bdc54f81a6e2e2c2bed61015",
	25: "b0da897be1e98672d0d09b884da11186435b2388dcb1aade8507f0e391651559",
	26: "03486ede0357637b89b229b00dd7c8a4e14c7344dcfa2b371389cac46ad17aa5",
	27: "84705f03eabe60bb8cc8356a602a7d5702ed6e81fc83572c9e6b03820bb18b31",
	28: "e2d545d270cdc5fc680c7a9a33ebfcb34277222355f4f0fbb0bab25f097e846d",
	29: "acee2be60372d2e20eb93f4bf82c977b1e2779b332ea53c7b24915676ec7ed4e",
	30: "7ada3743d98a62c5811cc039d29e0decb50bec6679c4309045958b98b29de7aa",
	31: "869d133d431dd8cb688e7493ba3eac9e544ef80655322c29ba812a4e1092ecae",
	32: "b2a2e82f8ca907b689b13a712bfad32bdb8722dda5c6b0c85ab87e7c215f6bb5",
	33: "8c6aacad9dcfc60b5479304218a3bd872676861d0c76c8fe3b1621bbaa320f38",
	34: "ff09fa3c8368cf53c1376c9f569817964ef48894065e19a2ecdb475eec363175",
	35: "884684e8b251f92672c21a0b9147c356b97421323967c16325da2628c6446e2b",
	36: "e625b0a27efd1ba610786d9e99478fde2aac4e6c9dfd38b5427cec74e5d1d050",
	37: "817db319e0499188e3f7551111c7c82303158996b5b3480e2a8eb3e048e4e947",
	38: "cec3d0aa7b44b70e8840be3792612ae7f43782d41e550b401c3d9df90c8eddf3",
	39: "f90e23151b94431a246b6e850ae63d0bd2f77fc8566ce46b3468b45355e27a6e",
	40: "72a6da2022f6f94bf6d15e871c2ff4e954f85c8b809caad0ac37ddb6d6ff8fa6",
	41: "176f984dcbd5182772c65c41bf420c69c38fb962c412e5a1d7d81c3cab522931",
	42: "bc8b670189b3721ecad77a320764be17a2f489952a9b95c59293edc58f7bd1f3",
	43: "d2146fa54d30638cecf4af8c6668fa4245bf8f9b1c8c84b11ef17ff0a97e9fff",
	44: "69e0cef2bc64d267c000a6145fe965da7b443889b23e6f6caf9a57ffff12bbd7",
}

// shippedMigrationProblems is the check behind TestShippedMigrationsUnchanged,
// split out so its three failure branches can be exercised against
// synthetic input (the real embedded set can't have a file removed at test
// time). It returns one message per problem, in on-disk version order and
// then pinned-but-missing order, or nil when everything matches.
func shippedMigrationProblems(migs []migration, pinned map[int]string) []string {
	var problems []string
	onDisk := make(map[int]bool, len(migs))
	for _, m := range migs {
		onDisk[m.Version] = true
		got := migrationChecksum(m.SQL)
		want, ok := pinned[m.Version]
		if !ok {
			problems = append(problems, fmt.Sprintf("migration %s is not pinned: add its migrationChecksum to shippedMigrationChecksums in shipped_migrations_test.go — it is frozen from the moment it merges (ADR-0100); got %s", m.Name, got))
			continue
		}
		if got != want {
			problems = append(problems, fmt.Sprintf("migration %s has changed since it merged: migration files are frozen the moment they land on main (ADR-0100) — revert it and append a new NNN_*.sql instead (pinned %s, got %s)", m.Name, want, got))
		}
	}
	versions := make([]int, 0, len(pinned))
	for v := range pinned {
		versions = append(versions, v)
	}
	sort.Ints(versions)
	for _, v := range versions {
		if !onDisk[v] {
			problems = append(problems, fmt.Sprintf("migration %d is pinned in shipped_migrations_test.go but is no longer on disk — files are never deleted or renumbered (ADR-0100)", v))
		}
	}
	return problems
}

// TestShippedMigrationsUnchanged fails CI on any statement-level change,
// deletion or renumbering of a merged migration file, and on a new file
// that was not pinned (ADR-0100).
func TestShippedMigrationsUnchanged(t *testing.T) {
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range shippedMigrationProblems(migs, shippedMigrationChecksums) {
		t.Error(p)
	}
}

// TestShippedMigrationsGuard_Branches proves each failure branch of the
// guard fires on synthetic input, and that an intact set is clean — so a
// regression in the guard itself can't pass silently.
func TestShippedMigrationsGuard_Branches(t *testing.T) {
	a := migration{Version: 9101, Name: "9101_a.sql", SQL: "CREATE TABLE a (n INTEGER);\n"}
	b := migration{Version: 9102, Name: "9102_b.sql", SQL: "CREATE TABLE b (n INTEGER);\n"}
	pinned := map[int]string{9101: migrationChecksum(a.SQL), 9102: migrationChecksum(b.SQL)}

	if got := shippedMigrationProblems([]migration{a, b}, pinned); got != nil {
		t.Fatalf("intact set must be clean, got %v", got)
	}

	commented := a
	commented.SQL = "-- explanatory note added later\n" + a.SQL
	if got := shippedMigrationProblems([]migration{commented, b}, pinned); got != nil {
		t.Fatalf("comment-only edit must be clean, got %v", got)
	}

	edited := a
	edited.SQL = a.SQL + "CREATE TABLE a2 (n INTEGER);\n"
	got := shippedMigrationProblems([]migration{edited, b}, pinned)
	if len(got) != 1 || !strings.Contains(got[0], "migration 9101_a.sql has changed since it merged") || !strings.Contains(got[0], "append a new NNN_*.sql") {
		t.Fatalf("statement edit: got %v", got)
	}

	c := migration{Version: 9103, Name: "9103_unpinned.sql", SQL: "CREATE TABLE c (n INTEGER);\n"}
	got = shippedMigrationProblems([]migration{a, b, c}, pinned)
	if len(got) != 1 || !strings.Contains(got[0], "migration 9103_unpinned.sql is not pinned") || !strings.Contains(got[0], migrationChecksum(c.SQL)) {
		t.Fatalf("unpinned file: got %v", got)
	}

	got = shippedMigrationProblems([]migration{a}, pinned)
	if len(got) != 1 || !strings.Contains(got[0], "migration 9102 is pinned in shipped_migrations_test.go but is no longer on disk") {
		t.Fatalf("deleted/renumbered file: got %v", got)
	}

	renumbered := b
	renumbered.Version, renumbered.Name = 9104, "9104_b.sql"
	got = shippedMigrationProblems([]migration{a, renumbered}, pinned)
	if len(got) != 2 || !strings.Contains(got[0], "9104_b.sql is not pinned") || !strings.Contains(got[1], "migration 9102 is pinned") {
		t.Fatalf("renumbered file must report both sides: got %v", got)
	}
}

// TestMigrationChecksum_IgnoresCommentsNotStatements pins the property the
// pin above relies on: a comment-only edit to a merged file is NOT a
// change (so explanatory comments can still be added or corrected after
// the fact), while a statement change is.
func TestMigrationChecksum_IgnoresCommentsNotStatements(t *testing.T) {
	base := "INSERT OR IGNORE INTO permission_actions (action) VALUES ('x');\n"
	t.Run("comment-only edit keeps the checksum", func(t *testing.T) {
		commented := "-- added after the fact (ut-docs#0000)\n" + base + "  -- trailing note\n"
		if migrationChecksum(commented) != migrationChecksum(base) {
			t.Fatalf("a comment-only edit must not change migrationChecksum")
		}
	})
	t.Run("statement edit changes the checksum", func(t *testing.T) {
		edited := base + "INSERT OR IGNORE INTO permission_actions (action) VALUES ('y');\n"
		if migrationChecksum(edited) == migrationChecksum(base) {
			t.Fatalf("a statement edit must change migrationChecksum")
		}
	})
}
