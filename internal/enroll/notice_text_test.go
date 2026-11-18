package enroll

import (
	"encoding/json"
	"os"
	"testing"
)

// ut-docs#6617: the notice a shop sees is the owner-approved text, word for
// word (legal-reviews/2026-11-17-auto-registration-6559.md section 5), and
// the version id stored as evidence ("notice <id> shown at T") names exactly
// that text. If you change a string here you change the notice: Legal and
// the owner approve the new text, and NoticeVersion changes with it.
var approvedNotice = []struct{ key, text string }{
	{"registration.notice.title", "What this till tells us"},
	{"registration.notice.body", "Each time this till starts, it tells Universal Till (Task Runner Technology Ltd) its version, system type, release channel, country, language, a random till code and when it last started. When the till updates itself, or you add, update or remove a plugin, it also tells us which version or plugin changed and when."},
	{"registration.notice.fields", "We never send your sales, customers, staff, items or anything you type."},
	{"registration.notice.purpose", "We use this to run and support Universal Till, to see which versions are in use and fix problems, to keep the service secure, and to count how many tills use it. We do this because we have a legitimate interest in running and improving our own service, and we have weighed it against your privacy."},
	{"registration.notice.retention", "We keep a till's latest details while it is in use and for 12 months after it last started. We keep its change history for 12 months. After that we delete them. We keep the address a till connects from for 90 days."},
	{"registration.notice.availability", "Your till works whether or not it can reach us, and this never delays a sale."},
	{"registration.notice.stop_hint", "You can ask us to stop at any time: in Settings > Privacy, switch off \"Send usage facts\" (the till then sends nothing of this kind; cloud features you use still need to connect), or email support@universaltill.com. You can also ask to see, correct or delete what we hold, and object to it. You can complain to your data protection authority; in the UK that is the Information Commissioner's Office (ico.org.uk)."},
	{"registration.notice.controller", "Task Runner Technology Ltd, England and Wales, company number 11442274, is responsible for this data. More: universaltill.com/privacy"},
}

const approvedNoticeVersion = "checkin-notice-v2"

func TestShippedNoticeEqualsApprovedText(t *testing.T) {
	raw, err := os.ReadFile("../../web/locales/en.json")
	if err != nil {
		t.Fatal(err)
	}
	var en map[string]string
	if err := json.Unmarshal(raw, &en); err != nil {
		t.Fatal(err)
	}
	for _, a := range approvedNotice {
		if got := en[a.key]; got != a.text {
			t.Errorf("%s differs from the approved text:\n got: %q\nwant: %q", a.key, got, a.text)
		}
	}
	if NoticeVersion != approvedNoticeVersion {
		t.Errorf("NoticeVersion = %q, want %q (the id of the approved text; draft checkin-notice-v1 was a different text)", NoticeVersion, approvedNoticeVersion)
	}
}
