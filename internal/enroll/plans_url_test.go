package enroll

import "testing"

// ut-docs#4286: the till's Renew / Choose-a-plan panel shows a QR of the
// cloud's public /plans redirect, built from the same web base the claim
// URL uses (the marketplace endpoint minus a trailing "/api").

func TestWebBase(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://cloud.universaltill.com/api", "https://cloud.universaltill.com"},
		{"https://cloud.universaltill.com/api/", "https://cloud.universaltill.com"},
		{"http://127.0.0.1:8096", "http://127.0.0.1:8096"},
		{"http://127.0.0.1:8096/", "http://127.0.0.1:8096"},
		{"https://on-prem.example/till/api", "https://on-prem.example/till"},
	} {
		if got := WebBase(tc.in); got != tc.want {
			t.Errorf("WebBase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCloudLocale(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"en", "en-gb"},
		{"de", "de-de"},
		{"tr", "tr-tr"},
		{"fa", "fa-ir"},
		{"ar", "ar-sa"},
		{"pt", "pt-pt"},
		{"en-US", "en-us"},
		{"de_DE", "de-de"},
		{"de-AT", "de-de"},
		{"", "en-gb"},
		{"xx", "en-gb"},
		{"../evil", "en-gb"},
		{"de-DE&x=1", "en-gb"},
	} {
		if got := CloudLocale(tc.in); got != tc.want {
			t.Errorf("CloudLocale(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPlansURL(t *testing.T) {
	got := PlansURL("https://cloud.universaltill.com/api/", "store 1&x=2", "renew", "fa")
	want := "https://cloud.universaltill.com/plans?intent=renew&lang=fa-ir&store=store+1%26x%3D2"
	if got != want {
		t.Fatalf("PlansURL = %q, want %q", got, want)
	}
	// Anything but renew is "change" — the cloud does the same.
	if got := PlansURL("http://127.0.0.1:8096", "s1", "steal", "en"); got != "http://127.0.0.1:8096/plans?intent=change&lang=en-gb&store=s1" {
		t.Fatalf("unknown intent = %q", got)
	}
}

func TestPartnerURL(t *testing.T) {
	got := PartnerURL("https://cloud.universaltill.com/api/", "store 1&x=2", "fa")
	want := "https://cloud.universaltill.com/partner?lang=fa-ir&store=store+1%26x%3D2"
	if got != want {
		t.Fatalf("PartnerURL = %q, want %q", got, want)
	}
	if got := PartnerURL("http://127.0.0.1:8096", "s1", "../x"); got != "http://127.0.0.1:8096/partner?lang=en-gb&store=s1" {
		t.Fatalf("bad locale = %q", got)
	}
}
