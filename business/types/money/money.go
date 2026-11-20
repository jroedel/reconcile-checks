// Package money is an amount of a check: whole cents, how a person types it,
// and how it is written on the paper, in figures and in words
// (docs/04-stock-and-layout.md, "Amount in words").
//
// A float never touches an amount. A check for $0.30 that is 0.29999999 in a
// float64 is a check written for the wrong amount on the line that matters
// most; whole cents in an int64 can't be.
package money

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Cents is an amount in whole cents.
type Cents int64

// Max is the largest amount this program writes: $999,999,999.99, the most
// whose words it can say. A check's own ceiling is far lower and is asked
// about separately (docs/06-writing-a-check.md); this is the limit of the
// arithmetic, not a business rule.
const Max Cents = 999_999_999_99

// Invalid is an amount a person typed that can't be read. Its message says
// what to type instead.
type Invalid struct{ Reason string }

func (e Invalid) Error() string { return e.Reason }

// Parse reads an amount as a person types it: "1234.5", "1,234.50" and
// "$1234.50" are all $1,234.50. Spaces around it, and between the dollar sign
// and the figures, are ignored.
//
// It refuses rather than guesses: more than two decimals ("12.345" -- which
// cent?), commas in the wrong places ("1,23.45" -- a slip, not a thousand), a
// minus sign, letters, or more than Max. Zero is an amount; whether a check
// may be written for it is the check's rule, not this one's.
func Parse(s string) (Cents, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSpace(strings.TrimPrefix(s, "$"))

	if s == "" {
		return 0, Invalid{"Type the amount, for example 1234.50."}
	}

	if strings.HasPrefix(s, "-") {
		return 0, Invalid{"An amount can't be less than zero."}
	}

	whole, frac, hasPoint := strings.Cut(s, ".")

	if hasPoint && len(frac) > 2 {
		return 0, Invalid{"An amount has at most two figures after the point: dollars and cents."}
	}

	if hasPoint && frac == "" && whole == "" {
		return 0, Invalid{"Type the amount, for example 1234.50."}
	}

	if !digitsWithCommas(whole) || !digits(frac) {
		return 0, Invalid{fmt.Sprintf("%q is not an amount. Use figures, a point before the cents, and commas only between thousands.", s)}
	}

	dollars := strings.ReplaceAll(whole, ",", "")
	if dollars == "" {
		dollars = "0"
	}

	if len(dollars) > 9 {
		return 0, Invalid{"That is more than this program can write on a check."}
	}

	d, err := strconv.ParseInt(dollars, 10, 64)
	if err != nil {
		return 0, Invalid{fmt.Sprintf("%q is not an amount.", s)}
	}

	c := int64(0)
	if frac != "" {
		frac += strings.Repeat("0", 2-len(frac)) // "5" is fifty cents
		c, _ = strconv.ParseInt(frac, 10, 64)
	}

	return Cents(d*100 + c), nil
}

// digits reports whether s is empty or all ASCII digits.
func digits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}

// digitsWithCommas reports whether s is digits, with commas, if any, exactly
// between groups of three: "1234", "1,234" and "12,345,678", but not "1,23" or
// ",123" or "1,,234".
func digitsWithCommas(s string) bool {
	if !strings.Contains(s, ",") {
		return digits(s)
	}

	groups := strings.Split(s, ",")
	for i, g := range groups {
		if !digits(g) || g == "" {
			return false
		}

		if i == 0 && len(g) > 3 {
			return false
		}

		if i > 0 && len(g) != 3 {
			return false
		}
	}

	return true
}

// Dollars and CentsPart split an amount.
func (c Cents) Dollars() int64   { return int64(c) / 100 }
func (c Cents) CentsPart() int64 { return int64(c) % 100 }

// Figures is the amount in figures, with commas between thousands and no
// dollar sign: "1,234.50". The dollar sign is printed on the stock.
func (c Cents) Figures() string {
	d := strconv.FormatInt(c.Dollars(), 10)

	var b strings.Builder
	for i, r := range d {
		if i > 0 && (len(d)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}

	return fmt.Sprintf("%s.%02d", b.String(), c.CentsPart())
}

// String is the amount as a sentence says it: "$1,234.50".
func (c Cents) String() string { return "$" + c.Figures() }

// Padded is the amount in figures, filled on the left with * to width
// characters, so that no figure can be written in front of it: "**1,234.50".
// An amount already as wide as width, or wider, is returned as it is; whether
// it fits the box is the layout's question.
func (c Cents) Padded(width int) string {
	f := c.Figures()
	if len(f) >= width {
		return f
	}

	return strings.Repeat("*", width-len(f)) + f
}

// ErrTooLarge is returned by Words for an amount past Max.
var ErrTooLarge = errors.New("money: more than this program can write in words")

// Words is the amount in words, as written on a check's dollars line:
// "One thousand two hundred thirty-four and 50/100".
//
//   - The cents are a fraction, always two figures: "and 05/100", "and 00/100".
//   - Under a dollar, the dollars are "Zero".
//   - Compound numbers are hyphenated: "thirty-four".
//   - Hundreds stay hundreds: 1,100 is "One thousand one hundred", never
//     "Eleven hundred".
//   - No "and" inside the dollars: on a check "and" introduces the cents and
//     nothing else, so "One hundred one", not "One hundred and one".
//   - The first word capitalized, the rest not.
//
// Filling the rest of the line with * is the layout's job, because only the
// layout knows how long the line is.
func (c Cents) Words() (string, error) {
	if c < 0 || c > Max {
		return "", ErrTooLarge
	}

	d := c.Dollars()

	var dollars string
	if d == 0 {
		dollars = "zero"
	} else {
		dollars = spell(d)
	}

	w := fmt.Sprintf("%s and %02d/100", dollars, c.CentsPart())

	return strings.ToUpper(w[:1]) + w[1:], nil
}

var (
	ones = [...]string{"", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine",
		"ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen", "eighteen", "nineteen"}
	tens = [...]string{"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety"}
)

// spell says 1 to 999,999,999 in words, lower case.
func spell(n int64) string {
	var parts []string

	for _, scale := range []struct {
		size int64
		name string
	}{
		{1_000_000, "million"},
		{1_000, "thousand"},
	} {
		if n >= scale.size {
			parts = append(parts, below1000(n/scale.size)+" "+scale.name)
			n %= scale.size
		}
	}

	if n > 0 {
		parts = append(parts, below1000(n))
	}

	return strings.Join(parts, " ")
}

// below1000 says 1 to 999.
func below1000(n int64) string {
	var parts []string

	if n >= 100 {
		parts = append(parts, ones[n/100]+" hundred")
		n %= 100
	}

	switch {
	case n == 0:
	case n < 20:
		parts = append(parts, ones[n])
	case n%10 == 0:
		parts = append(parts, tens[n/10])
	default:
		parts = append(parts, tens[n/10]+"-"+ones[n%10])
	}

	return strings.Join(parts, " ")
}
