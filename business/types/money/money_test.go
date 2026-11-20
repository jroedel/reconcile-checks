package money_test

import (
	"errors"
	"testing"

	"github.com/jroedel/reconcile-checks/business/types/money"
)

// The table in docs/04-stock-and-layout.md, "Amount in words", and every teen.
func TestWords(t *testing.T) {
	for cents, want := range map[money.Cents]string{
		5:          "Zero and 05/100",
		100:        "One and 00/100",
		110:        "One and 10/100",
		1100:       "Eleven and 00/100",
		1300:       "Thirteen and 00/100",
		1999:       "Nineteen and 99/100",
		2100:       "Twenty-one and 00/100",
		9999:       "Ninety-nine and 99/100",
		10000:      "One hundred and 00/100",
		10100:      "One hundred one and 00/100",
		11000:      "One hundred ten and 00/100",
		100000:     "One thousand and 00/100",
		100100:     "One thousand one and 00/100",
		110000:     "One thousand one hundred and 00/100",
		1200000:    "Twelve thousand and 00/100",
		9999999:    "Ninety-nine thousand nine hundred ninety-nine and 99/100",
		10000000:   "One hundred thousand and 00/100",
		100000000:  "One million and 00/100",
		123450:     "One thousand two hundred thirty-four and 50/100",
		4000000:    "Forty thousand and 00/100",
		70001500:   "Seven hundred thousand fifteen and 00/100",
		200000000:  "Two million and 00/100",
		0:          "Zero and 00/100",
		money.Max:  "Nine hundred ninety-nine million nine hundred ninety-nine thousand nine hundred ninety-nine and 99/100",
		1001000000: "Ten million ten thousand and 00/100",
	} {
		got, err := cents.Words()
		if err != nil {
			t.Errorf("%d: %v", cents, err)
			continue
		}

		if got != want {
			t.Errorf("%s in words:\n got %q\nwant %q", cents, got, want)
		}
	}
}

func TestEveryTeen(t *testing.T) {
	teens := []string{"Ten", "Eleven", "Twelve", "Thirteen", "Fourteen", "Fifteen", "Sixteen", "Seventeen", "Eighteen", "Nineteen"}

	for i, word := range teens {
		c := money.Cents((10 + i) * 100)

		got, err := c.Words()
		if err != nil {
			t.Fatal(err)
		}

		if want := word + " and 00/100"; got != want {
			t.Errorf("%s: got %q, want %q", c, got, want)
		}
	}
}

func TestWordsRefusesWhatItCantSay(t *testing.T) {
	for _, c := range []money.Cents{-1, money.Max + 1} {
		if _, err := c.Words(); !errors.Is(err, money.ErrTooLarge) {
			t.Errorf("%d: err = %v, want ErrTooLarge", c, err)
		}
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]money.Cents{
		"1234.5":        123450,
		"1,234.50":      123450,
		"$1234.50":      123450,
		"  $ 1,234.50 ": 123450,
		"1234":          123400,
		"0.05":          5,
		".05":           5,
		"5.":            500,
		"0":             0,
		"12,345,678.90": 1234567890,
		"999999999.99":  money.Max,
		"007.10":        710,
	} {
		got, err := money.Parse(in)
		if err != nil {
			t.Errorf("Parse(%q): %v", in, err)
			continue
		}

		if got != want {
			t.Errorf("Parse(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseRefusesRatherThanGuesses(t *testing.T) {
	for _, in := range []string{
		"",
		"$",
		".",
		"12.345",  // which cent?
		"1,23.45", // a slip, not a thousand
		",123",
		"1,,234",
		"1234,567", // commas only between thousands
		"-12.00",   // less than zero
		"12.3a",
		"twelve",
		"1e3",
		"1 234",
		"1000000000.00", // past what the words can say
	} {
		if _, err := money.Parse(in); err == nil {
			t.Errorf("Parse(%q) was accepted", in)
		} else if _, ok := errors.AsType[money.Invalid](err); !ok {
			t.Errorf("Parse(%q): %v is not a money.Invalid", in, err)
		}
	}
}

func TestFiguresAndPadding(t *testing.T) {
	for c, want := range map[money.Cents]string{
		5:         "0.05",
		100:       "1.00",
		123450:    "1,234.50",
		100000:    "1,000.00",
		99999:     "999.99",
		123456789: "1,234,567.89",
		money.Max: "999,999,999.99",
	} {
		if got := c.Figures(); got != want {
			t.Errorf("%d: Figures = %q, want %q", c, got, want)
		}
	}

	if got := money.Cents(123450).Padded(10); got != "**1,234.50" {
		t.Errorf("Padded(10) = %q", got)
	}

	if got := money.Cents(123450).Padded(4); got != "1,234.50" {
		t.Errorf("Padded narrower than the figures = %q, want them unchanged", got)
	}

	if got := money.Cents(123450).String(); got != "$1,234.50" {
		t.Errorf("String = %q", got)
	}
}

// Every amount that parses comes back the same from its figures: nothing is
// lost between what is typed and what is printed.
func TestFiguresParseBackToTheSameAmount(t *testing.T) {
	for _, c := range []money.Cents{0, 1, 5, 99, 100, 101, 123450, 100000000, money.Max} {
		got, err := money.Parse(c.Figures())
		if err != nil || got != c {
			t.Errorf("%d -> %q -> %d, %v", c, c.Figures(), got, err)
		}
	}
}
