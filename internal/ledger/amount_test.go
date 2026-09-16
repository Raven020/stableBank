package ledger

import "testing"

func TestParseAmountAndDisplay(t *testing.T) {
	cases := []struct {
		code    string
		decimal string
		minor   int64
		display string
	}{
		{"USD", "100.00", 10000, "100.00 USD"},
		{"USD", "0.01", 1, "0.01 USD"},
		{"USD", "5", 500, "5.00 USD"},
		{"USDC", "800.000000", 800000000, "800.000000 USDC"},
		{"USDC", "0.998500", 998500, "0.998500 USDC"},
		{"USDC", "1", 1000000, "1.000000 USDC"},
	}
	for _, c := range cases {
		a, err := ParseAmount(c.code, c.decimal)
		if err != nil {
			t.Fatalf("ParseAmount(%q, %q): unexpected error: %v", c.code, c.decimal, err)
		}
		if a.Minor != c.minor {
			t.Errorf("ParseAmount(%q, %q).Minor = %d, want %d", c.code, c.decimal, a.Minor, c.minor)
		}
		if got := a.Display(); got != c.display {
			t.Errorf("Display() = %q, want %q", got, c.display)
		}
	}
}

func TestParseAmountRejectsTooManyDecimals(t *testing.T) {
	if _, err := ParseAmount("USD", "1.005"); err == nil {
		t.Fatal("expected error for USD amount with 3 decimal places")
	}
	if _, err := ParseAmount("USDC", "1.0000001"); err == nil {
		t.Fatal("expected error for USDC amount with 7 decimal places")
	}
}

func TestParseAmountRejectsInvalidInput(t *testing.T) {
	invalid := []string{"", "abc", "1.2.3", "1a", "-", "."}
	for _, s := range invalid {
		if _, err := ParseAmount("USD", s); err == nil {
			t.Errorf("expected error for input %q", s)
		}
	}
	if _, err := ParseAmount("EUR", "1.00"); err == nil {
		t.Error("expected error for unknown currency")
	}
}

func TestParseAmountNegative(t *testing.T) {
	a, err := ParseAmount("USD", "-12.34")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Minor != -1234 {
		t.Fatalf("Minor = %d, want -1234", a.Minor)
	}
	if got := a.Display(); got != "-12.34 USD" {
		t.Fatalf("Display() = %q, want -12.34 USD", got)
	}
}
