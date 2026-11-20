package checkdb_test

import (
	"path/filepath"
	"testing"

	"github.com/jroedel/reconcile-checks/business/domain/check/stores/checkdb"
	"github.com/jroedel/reconcile-checks/foundation/sqldb"
)

// Init runs at every start, on a register that already holds checks.
func TestInitRunsAgainOnARegisterWithChecks(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "register.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := checkdb.Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(t.Context(), `
INSERT INTO checks (account, number, written_on, payee, amount, state, created_at)
VALUES ('checking-a', 1043, '2026-11-20', 'Payee A Plumbing', 123450, 'printed', 1)`); err != nil {
		t.Fatal(err)
	}

	if err := checkdb.Init(t.Context(), db); err != nil {
		t.Fatalf("a second Init: %v", err)
	}

	if err := sqldb.CheckSchema(t.Context(), db, checkdb.Expected); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM checks`).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d checks after a second Init (%v); want the one", n, err)
	}
}

// What is true of every check there will ever be is refused by the table
// itself, whatever writes to it.
func TestTheTableRefusesANumberOrAmountBelowOne(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "register.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := checkdb.Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	for _, row := range [][2]int{{0, 100}, {-1, 100}, {1043, 0}, {1043, -100}} {
		_, err := db.ExecContext(t.Context(), `
INSERT INTO checks (account, number, written_on, payee, amount, state, created_at)
VALUES ('checking-a', ?, '2026-11-20', 'Payee A', ?, 'printed', 1)`, row[0], row[1])
		if err == nil {
			t.Errorf("number %d, amount %d was accepted", row[0], row[1])
		}
	}
}
