package mockpay

import "testing"

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
