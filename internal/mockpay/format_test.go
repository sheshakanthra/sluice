package mockpay

import (
	"testing"

	"github.com/sheshakanthra/sluice/internal/mockpay/mpdb"
)

func TestReturnURL(t *testing.T) {
	in := mpdb.Intent{ID: "pi_1", MerchantRef: "ord-1", Status: "AUTHORIZED"}
	const q = "intent=pi_1&o=ord-1&order=ord-1&status=AUTHORIZED"
	tests := []struct{ base, want string }{
		{"http://app.test/order?o=ord-1", "http://app.test/order?" + q},
		{"https://app.test/order?o=ord-1", "https://app.test/order?" + q},
		// Same-origin path (live demo behind a tunnel, ADR 0016).
		{"/order?o=ord-1", "/order?" + q},
		// Refused: would leave the origin or is not a navigable URL.
		{"//evil.test/order", ""},
		{"/\\evil.test/order", ""},
		{"javascript:alert(1)", ""},
		{"order?o=1", ""},
		{"https:///order", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := returnURL(tc.base, in); got != tc.want {
			t.Errorf("returnURL(%q) = %q, want %q", tc.base, got, tc.want)
		}
	}
}

func TestFormatINR(t *testing.T) {
	tests := []struct {
		paise int64
		want  string
	}{
		{0, "₹0.00"},
		{5, "₹0.05"},
		{99_900, "₹999.00"},
		{100_000, "₹1,000.00"},
		{150_050, "₹1,500.50"},
		{10_000_000, "₹1,00,000.00"},
		{1_234_567_89, "₹12,34,567.89"},
		{100_000_000_00, "₹10,00,00,000.00"},
		{-150_000, "-₹1,500.00"},
	}
	for _, tc := range tests {
		if got := formatINR(tc.paise); got != tc.want {
			t.Errorf("formatINR(%d) = %s, want %s", tc.paise, got, tc.want)
		}
	}
}
