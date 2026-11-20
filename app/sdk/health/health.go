// Package health answers /healthz: whether this program is up, and whether
// the register's schema is the one this version expects.
//
// It re-checks the schema on every call rather than pinging the database. An
// update restart watches this answer to decide whether the new version came up
// (docs/10-install-update-and-releases.md), and a version that can't read the
// register it was given has not come up, however well it answers otherwise.
package health

import (
	"database/sql"
	"net/http"

	"github.com/jroedel/reconcile-checks/foundation/sqldb"
)

// Handler answers 200 with the version, or 503 with what is wrong.
func Handler(version string, db *sql.DB, want sqldb.Expected) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")

		if err := sqldb.CheckSchema(r.Context(), db, want); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("unhealthy " + version + ": " + err.Error() + "\n"))

			return
		}

		w.Write([]byte("ok " + version + "\n"))
	}
}
