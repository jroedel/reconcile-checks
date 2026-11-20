package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// The checks routes (docs/09-reconcile-api.md, "The calls"). An account is
// always Reconcile's id; a number is a string of digits, and an amount an
// unsigned decimal string, both ways.

// Amount is an amount as Reconcile writes it: "1234.50". Read from a string
// or, should a version of Reconcile write one, a number.
type Amount string

func (a *Amount) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}

		*a = Amount(s)

		return nil
	}

	if string(b) == "null" {
		*a = ""
		return nil
	}

	*a = Amount(b)

	return nil
}

// Check is a check Reconcile holds.
type Check struct {
	ID         string `json:"id"`
	Number     string `json:"number"`
	Amount     Amount `json:"amount"`
	Payee      string `json:"payee"`
	WrittenOn  string `json:"written_on"` // YYYY-MM-DD
	Memo       string `json:"memo"`
	State      string `json:"state"` // outstanding, attached or void
	AttachedTo []struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	} `json:"attached_to"`
	URL string `json:"url"`
}

// The states a number can be in (docs/09, "Asking about the number before
// printing").
const (
	NumberFree        = "free"
	NumberOutstanding = "outstanding"
	NumberAttached    = "attached"
	NumberVoid        = "void"
	NumberCleared     = "cleared"
)

// Paid is a payment of a number the bank made with no check here: a check
// written by hand from the same book.
type Paid struct {
	Amount Amount `json:"amount"`
	Date   string `json:"date"` // YYYY-MM-DD
}

// Number is what Reconcile says about a number on an account.
type Number struct {
	Number  string `json:"number"`
	State   string `json:"state"`
	Check   *Check `json:"check"`   // outstanding, attached, void
	Cleared []Paid `json:"cleared"` // cleared
}

// Number asks about one number on the account, before it is printed on.
// Asking sees every person's checks, so it sees the other computer's.
func (c *Client) Number(ctx context.Context, key, account string, number int) (Number, error) {
	var n Number

	err := c.get(ctx, key, fmt.Sprintf("/api/v1/accounts/%s/checks/%d", url.PathEscape(account), number), &n)

	return n, err
}

// Payees is up to twenty of the account's payee names containing q, as
// Reconcile spells them.
func (c *Client) Payees(ctx context.Context, key, account, q string) ([]string, error) {
	var out struct {
		Payees []struct {
			Name string `json:"name"`
		} `json:"payees"`
	}

	path := fmt.Sprintf("/api/v1/accounts/%s/payees?%s", url.PathEscape(account), url.Values{"q": {strings.TrimSpace(q)}}.Encode())
	if err := c.get(ctx, key, path, &out); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(out.Payees))
	for _, p := range out.Payees {
		names = append(names, p.Name)
	}

	return names, nil
}
