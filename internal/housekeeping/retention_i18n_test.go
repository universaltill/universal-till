package housekeeping

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func enLocale(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("../../web/locales/en.json")
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// verbRe matches a printf verb but not the literal "%%".
var verbRe = regexp.MustCompile(`%[^%a-zA-Z]*[a-zA-Z]`)

func verbCount(s string) int { return len(verbRe.FindAllString(strings.ReplaceAll(s, "%%", ""), -1)) }

// A new retention Kind can't ship untranslated or with a policy whose
// placeholders don't match the numbers the rule supplies (ut-docs#3122 R4).
func TestRetentionRowsAreTranslated(t *testing.T) {
	en := enLocale(t)
	for _, r := range RetentionFor(Params{BackupDays: 21, ArchiveMinDays: 3650}) {
		label, policy := en["settings.storage.kind."+r.Kind], en["settings.storage.policy."+r.Kind]
		if label == "" {
			t.Errorf("kind %q: settings.storage.kind.%s missing or empty in en.json", r.Kind, r.Kind)
		}
		if policy == "" {
			t.Errorf("kind %q: settings.storage.policy.%s missing or empty in en.json", r.Kind, r.Kind)
			continue
		}
		if n := verbCount(policy); n != len(r.Args) {
			t.Errorf("kind %q: policy has %d verbs but the rule supplies %d Args", r.Kind, n, len(r.Args))
		}
	}
}

func TestRetentionFor_UsesThisTillsNumbers(t *testing.T) {
	rules := RetentionFor(Params{BackupDays: 21, ArchiveMinDays: 3650})
	by := map[string]Rule{}
	for _, r := range rules {
		by[r.Kind] = r
	}
	if got := by[KindBackups].Args[0]; got != 21 {
		t.Errorf("backups Args[0] = %d, want 21", got)
	}
	if got := by[KindPreRestore].Args[0]; got != 3650 {
		t.Errorf("pre-restore Args[0] = %d, want 3650", got)
	}
	if !strings.Contains(by[KindBackups].Policy, "21 days") {
		t.Errorf("developer policy should print this till's days: %q", by[KindBackups].Policy)
	}
}

func TestRetention_IsRetentionForDefaults(t *testing.T) {
	a, b := Retention(), RetentionFor(Params{BackupDays: DefaultBackupRetentionDays})
	if len(a) != len(b) {
		t.Fatalf("len %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Kind != b[i].Kind {
			t.Errorf("row %d: %s vs %s", i, a[i].Kind, b[i].Kind)
		}
	}
	if got := a[0].Args[0]; got != DefaultBackupRetentionDays {
		t.Errorf("backups Args[0] = %d, want default %d", got, DefaultBackupRetentionDays)
	}
}

func TestRule_InDatabase(t *testing.T) {
	var got []string
	for _, r := range Retention() {
		if r.InDatabase() {
			got = append(got, r.Kind)
		}
	}
	want := []string{KindSyncAggregateLedger, KindSyncHeldTombstones, KindSyncAssetLedger, KindSyncAdminVersion, KindSyncQuarantine}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("InDatabase kinds = %v, want %v", got, want)
	}
}
