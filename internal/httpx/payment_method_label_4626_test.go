package httpx

import "testing"

// ut-docs#4626: a built-in payment-method id ("cash", "gift", ...) must show
// its translated name wherever the id is rendered for a person, while a
// plugin's own method id stays as-is.
func TestPaymentMethodKey(t *testing.T) {
	cases := []struct {
		in, key string
		ok      bool
	}{
		{"cash", "tender.cash", true},
		{"card", "tender.card", true},
		{"voucher", "tender.voucher", true},
		{"gift", "tender.gift_card", true},
		{" Cash ", "tender.cash", true},
		{"GIFT", "tender.gift_card", true},
		{"sumup", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		key, ok := PaymentMethodKey(c.in)
		if key != c.key || ok != c.ok {
			t.Errorf("PaymentMethodKey(%q) = %q,%v; want %q,%v", c.in, key, ok, c.key, c.ok)
		}
	}
}

func TestTenderLabelTranslatesBuiltInMethodIDs(t *testing.T) {
	InitI18n(realI18n(t), "en")
	fa := FuncsFor("fa")["tenderLabel"].(func(string) string)
	for id, want := range map[string]string{
		"cash": "نقدی", "card": "کارت", "gift": "کارت هدیه", "voucher": "کوپن",
	} {
		if got := fa(id); got != want {
			t.Errorf("tenderLabel(%q) in fa = %q, want %q", id, got, want)
		}
	}
	if got := fa("sumup"); got != "sumup" {
		t.Errorf("plugin method id must pass through unchanged, got %q", got)
	}
	if got := fa("split"); got == "split" || got == "" {
		t.Errorf("sentinel split must still translate, got %q", got)
	}
}
