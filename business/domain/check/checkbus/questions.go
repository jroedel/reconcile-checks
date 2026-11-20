package checkbus

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Code names a question asked before printing.
type Code string

// The questions. Each is asked again rather than refused (docs/06, "The
// form"): every one of them has a right answer that is yes, now and then, and
// a person who knows it is not stopped.
const (
	// AskOldDate is a date more than a week before today.
	AskOldDate Code = "old-date"

	// AskPostDated is any date after today: a post-dated check.
	AskPostDated Code = "post-dated"

	// AskOverCeiling is an amount over the account's ceiling, asked with the
	// amount in words, so a slipped digit is read before it is printed.
	AskOverCeiling Code = "over-ceiling"

	// AskNewPayee is a payee the register has never paid: it catches a typo
	// before it is on paper, and one plumber becoming three spellings.
	AskNewPayee Code = "new-payee"
)

// Question is one thing asked again, with the sentence that asks it.
type Question struct {
	Code     Code
	Sentence string
}

// Asked is a check that can be printed once the person has answered its
// questions.
type Asked struct{ Questions []Question }

func (e Asked) Error() string {
	var s []string
	for _, q := range e.Questions {
		s = append(s, q.Sentence)
	}

	return strings.Join(s, " ")
}

// week is how far either side of today a date may be before it is asked about.
const week = 7

// Questions is what is still to ask about the check before it prints: every
// question whose answer isn't already in nc.Answered.
func (b *Business) Questions(ctx context.Context, nc NewCheck) ([]Question, error) {
	acct, err := b.accounts.Account(nc.Account)
	if err != nil {
		return nil, err
	}

	var qs []Question
	ask := func(c Code, sentence string) {
		if !slices.Contains(nc.Answered, c) {
			qs = append(qs, Question{Code: c, Sentence: sentence})
		}
	}

	now := b.now()
	today := day(now)
	written := day(nc.WrittenOn)
	date := written.Format("January 2, 2006")

	switch days := int(written.Sub(today).Hours() / 24); {
	case days > 0:
		ask(AskPostDated, fmt.Sprintf("The check is dated %s, after today. Print it post-dated?", date))
	case days < -week:
		ask(AskOldDate, fmt.Sprintf("The check is dated %s, more than a week ago. Print it with that date?", date))
	}

	ceiling := acct.Ceiling
	if ceiling <= 0 {
		ceiling = DefaultCeiling
	}

	if nc.Amount > ceiling {
		words, err := nc.Amount.Words()
		if err != nil {
			words = nc.Amount.String()
		}

		ask(AskOverCeiling, fmt.Sprintf("%s is more than this account's usual most, %s. In words: %s. Print it for that amount?",
			nc.Amount, ceiling, words))
	}

	if payee := strings.TrimSpace(nc.Payee); payee != "" {
		_, known, err := b.Defaults(ctx, payee)
		if err != nil {
			return nil, err
		}

		if !known && !b.knownToBooks(ctx, nc.Account, payee) {
			ask(AskNewPayee, fmt.Sprintf("%s is new. Keep it?", payee))
		}
	}

	return qs, nil
}
