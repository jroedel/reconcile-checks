// Package checkdb is the register: one row per check printed on this computer
// (docs/06-writing-a-check.md, "The register").
//
// Every column is here from the first version, those the Reconcile client will
// fill included, because the register on both computers will already hold real
// checks when it arrives, and a register that has to be rewritten then is one
// that can lose a check.
//
// # What the schema refuses, and what it leaves to checkbus
//
// The primary key (account, number) is the rule that matters most: a number is
// taken by the INSERT itself, so two presses of Print can't both be told they
// were first (Claim). The two CHECKs say what is true of every check that
// will ever exist: a number and an amount above zero.
//
// The state is not constrained here, on purpose. SQLite has no ALTER that
// drops a CHECK, so a state added later (CLAUDE.md, "When there is a
// database") would mean rebuilding the one table that must never lose a row.
// checkbus is what writes it, and only through the statements below.
package checkdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jroedel/reconcile-checks/business/domain/check/checkbus"
	"github.com/jroedel/reconcile-checks/business/types/money"
	"github.com/jroedel/reconcile-checks/foundation/sqldb"
)

// Store is the SQLite implementation of checkbus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore makes one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ checkbus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"checks": {
		"account", "number", "written_on", "payee", "amount", "memo", "stub_note",
		"state", "created_at", "printed_at", "voided_at", "void_reason", "reprint_of",
		"sent_at", "void_sent_at", "reconcile_id", "reconcile_url",
		"send_attempts", "next_try", "refusal", "conflict",
	},
}

// Init creates the table. Idempotent, run at every startup.
//
// The moments are Unix milliseconds, NULL until they happen. written_on is
// text, YYYY-MM-DD: it is the date on the paper, a day and not a moment, and a
// day stored as a moment shifts by one in some time zone sooner or later.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS checks (
    -- This computer's key for the account (checkbus.ValidKey), never
    -- Reconcile's id, which is mapped to it in the config once signed in.
    account        TEXT    NOT NULL,
    number         INTEGER NOT NULL CHECK (number > 0),

    written_on     TEXT    NOT NULL,
    payee          TEXT    NOT NULL,
    amount         INTEGER NOT NULL CHECK (amount > 0),
    memo           TEXT    NOT NULL DEFAULT '',
    stub_note      TEXT    NOT NULL DEFAULT '',

    -- One of checkbus's States: printing, printed, void.
    state          TEXT    NOT NULL,
    created_at     INTEGER NOT NULL,
    printed_at     INTEGER,
    voided_at      INTEGER,
    void_reason    TEXT    NOT NULL DEFAULT '',
    reprint_of     INTEGER,

    -- Reconcile's side (docs/09-reconcile-api.md), empty until it answers.
    sent_at        INTEGER,
    void_sent_at   INTEGER,
    reconcile_id   TEXT    NOT NULL DEFAULT '',
    reconcile_url  TEXT    NOT NULL DEFAULT '',
    send_attempts  INTEGER NOT NULL DEFAULT 0,
    next_try       INTEGER,
    refusal        TEXT    NOT NULL DEFAULT '',
    conflict       TEXT    NOT NULL DEFAULT '',

    PRIMARY KEY (account, number)
) STRICT;

-- The form's defaults, the payees' completion and the queue all read the
-- most recent first.
CREATE INDEX IF NOT EXISTS checks_created ON checks (created_at);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the register's checks table: %w", err)
	}

	return nil
}

const columns = `account, number, written_on, payee, amount, memo, stub_note,
    state, created_at, printed_at, voided_at, void_reason, reprint_of,
    sent_at, void_sent_at, reconcile_id, reconcile_url, send_attempts, next_try, refusal, conflict`

// Claim inserts the check, and is the one statement that takes its number.
func (s *Store) Claim(ctx context.Context, c checkbus.Check) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO checks (account, number, written_on, payee, amount, memo, stub_note, state, created_at, reprint_of)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Account, c.Number, c.WrittenOn.Format(time.DateOnly), c.Payee, int64(c.Amount), c.Memo, c.StubNote,
		string(c.State), c.CreatedAt.UnixMilli(), nullInt(c.ReprintOf))

	if sqldb.IsUniqueViolation(err) {
		return s.used(ctx, c.Account, c.Number)
	}

	if err != nil {
		return fmt.Errorf("taking check %d: %w", c.Number, err)
	}

	return nil
}

// used is NumberUsed for the check that has the number.
func (s *Store) used(ctx context.Context, account string, number int) error {
	have, err := s.Get(ctx, account, number)
	if err != nil {
		return err
	}

	return checkbus.NumberUsed{Number: number, State: have.State}
}

// Unclaim removes a check that is still printing.
func (s *Store) Unclaim(ctx context.Context, account string, number int) error {
	return s.change(ctx, "taking back", number, `
DELETE FROM checks WHERE account = ? AND number = ? AND state = 'printing'`, account, number)
}

// Printed marks a printing check printed.
func (s *Store) Printed(ctx context.Context, account string, number int, at time.Time) error {
	return s.change(ctx, "marking printed", number, `
UPDATE checks SET state = 'printed', printed_at = ?
WHERE account = ? AND number = ? AND state = 'printing'`, at.UnixMilli(), account, number)
}

// Void marks a printing or printed check void.
func (s *Store) Void(ctx context.Context, account string, number int, reason string, at time.Time) error {
	return s.change(ctx, "voiding", number, `
UPDATE checks SET state = 'void', voided_at = ?, void_reason = ?
WHERE account = ? AND number = ? AND state IN ('printing', 'printed')`, at.UnixMilli(), reason, account, number)
}

// change runs one conditional write, and answers ErrNotFound when it matched
// no row.
func (s *Store) change(ctx context.Context, what string, number int, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("%s check %d: %w", what, number, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s check %d: %w", what, number, err)
	}

	if n == 0 {
		return checkbus.ErrNotFound
	}

	return nil
}

// Renumber moves a check not yet sent to another number, with the checks that
// name it as the one they replaced, in one transaction. A corrected number
// starts its sending afresh: Reconcile's refusal was about the old one.
func (s *Store) Renumber(ctx context.Context, account string, from, to int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("correcting check %d: %w", from, err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
UPDATE checks SET number = ?, refusal = '', conflict = '', send_attempts = 0, next_try = NULL
WHERE account = ? AND number = ? AND sent_at IS NULL`, to, account, from)

	if sqldb.IsUniqueViolation(err) {
		tx.Rollback()
		return s.used(ctx, account, to)
	}

	if err != nil {
		return fmt.Errorf("correcting check %d to %d: %w", from, to, err)
	}

	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("correcting check %d to %d: %w", from, to, err)
	} else if n == 0 {
		return checkbus.ErrNotFound
	}

	if _, err := tx.ExecContext(ctx, `UPDATE checks SET reprint_of = ? WHERE account = ? AND reprint_of = ?`, to, account, from); err != nil {
		return fmt.Errorf("correcting the reprint of check %d: %w", from, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("correcting check %d to %d: %w", from, to, err)
	}

	return nil
}

// Get is one check.
func (s *Store) Get(ctx context.Context, account string, number int) (checkbus.Check, error) {
	cs, err := s.list(ctx, `WHERE account = ? AND number = ?`, account, number)
	if err != nil {
		return checkbus.Check{}, err
	}

	if len(cs) == 0 {
		return checkbus.Check{}, checkbus.ErrNotFound
	}

	return cs[0], nil
}

// Highest is the account's highest number, or 0.
func (s *Store) Highest(ctx context.Context, account string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(number), 0) FROM checks WHERE account = ?`, account).Scan(&n); err != nil {
		return 0, fmt.Errorf("reading the highest check number: %w", err)
	}

	return n, nil
}

// Recent is the account's last n checks, the most recently written first.
func (s *Store) Recent(ctx context.Context, account string, n int) ([]checkbus.Check, error) {
	return s.list(ctx, `WHERE account = ? ORDER BY created_at DESC, number DESC LIMIT ?`, account, n)
}

// LastTo is the most recent check to the payee, matched without regard to
// case: "payee a plumbing" is the same plumber.
func (s *Store) LastTo(ctx context.Context, payee string) (checkbus.Check, error) {
	cs, err := s.list(ctx, `WHERE payee = ? COLLATE NOCASE ORDER BY created_at DESC, number DESC LIMIT 1`, payee)
	if err != nil {
		return checkbus.Check{}, err
	}

	if len(cs) == 0 {
		return checkbus.Check{}, checkbus.ErrNotFound
	}

	return cs[0], nil
}

// likeEscape makes q match itself in a LIKE: a % or _ in a name is a letter.
var likeEscape = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// Payees is up to n payees whose name contains q, the most recently paid
// first, each spelled as it was last printed.
//
// SQLite's LIKE ignores case only for ASCII letters, so "peña" finds "Peña"
// but "PEÑA" doesn't. Good enough for completion, which is a convenience;
// LastTo's NOCASE has the same limit.
func (s *Store) Payees(ctx context.Context, q string, n int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT payee FROM checks
WHERE payee LIKE ? ESCAPE '\'
GROUP BY payee COLLATE NOCASE
ORDER BY max(created_at) DESC
LIMIT ?`, "%"+likeEscape.Replace(q)+"%", n)
	if err != nil {
		return nil, fmt.Errorf("reading payees: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("reading a payee: %w", err)
		}
		out = append(out, p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading payees: %w", err)
	}

	return out, nil
}

// LastAccount is the account of the most recent check, or "".
func (s *Store) LastAccount(ctx context.Context) (string, error) {
	var a string

	err := s.db.QueryRowContext(ctx, `SELECT account FROM checks ORDER BY created_at DESC LIMIT 1`).Scan(&a)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("reading the last account used: %w", err)
	}

	return a, nil
}

// InState is every check in the state, oldest first.
func (s *Store) InState(ctx context.Context, st checkbus.State) ([]checkbus.Check, error) {
	return s.list(ctx, `WHERE state = ? ORDER BY created_at, number`, string(st))
}

// Unsent is every check Reconcile doesn't have yet, oldest first. A check
// still printing waits for its answer: until then it is neither a check nor a
// void.
func (s *Store) Unsent(ctx context.Context) ([]checkbus.Check, error) {
	return s.list(ctx, `
WHERE (state IN ('printed', 'void') AND sent_at IS NULL)
   OR (state = 'void' AND void_sent_at IS NULL)
ORDER BY created_at, number`)
}

func (s *Store) list(ctx context.Context, where string, args ...any) ([]checkbus.Check, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM checks `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("reading the register: %w", err)
	}
	defer rows.Close()

	var out []checkbus.Check

	for rows.Next() {
		var (
			c                                                 checkbus.Check
			written, state                                    string
			amount, created                                   int64
			printed, voided, reprint, sent, voidSent, nextTry sql.NullInt64
		)

		if err := rows.Scan(&c.Account, &c.Number, &written, &c.Payee, &amount, &c.Memo, &c.StubNote,
			&state, &created, &printed, &voided, &c.VoidReason, &reprint,
			&sent, &voidSent, &c.Send.ID, &c.Send.URL, &c.Send.Attempts, &nextTry, &c.Send.Refusal, &c.Send.Conflict); err != nil {
			return nil, fmt.Errorf("reading a check: %w", err)
		}

		day, err := time.Parse(time.DateOnly, written)
		if err != nil {
			return nil, fmt.Errorf("check %d has the date %q, which is not a date: %w", c.Number, written, err)
		}

		c.WrittenOn = day
		c.Amount = money.Cents(amount)
		c.State = checkbus.State(state)
		c.CreatedAt = time.UnixMilli(created)
		c.PrintedAt = moment(printed)
		c.VoidedAt = moment(voided)
		c.ReprintOf = int(reprint.Int64)
		c.Send.SentAt = moment(sent)
		c.Send.VoidSentAt = moment(voidSent)
		c.Send.NextTry = moment(nextTry)

		out = append(out, c)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the register: %w", err)
	}

	return out, nil
}

// moment is a nullable Unix-milliseconds column as a time, zero when NULL.
func moment(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}

	return time.UnixMilli(v.Int64)
}

// nullInt is 0 as NULL.
func nullInt(n int) any {
	if n == 0 {
		return nil
	}

	return n
}
