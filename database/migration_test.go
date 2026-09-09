package database

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"testing"
)

// The concurrent-InitDB test that used to live here was removed: it could not
// reproduce the failure it was named after — that one needs two separate x-ui
// processes, and inside one process the single connection serialises them —
// and it failed intermittently on SQLite lock contention, which is noise rather
// than a signal. The regression is pinned by TestIsAlreadyThere below, which
// does fail without the fix.

// TestIsAlreadyThere covers the exact messages SQLite produces. Matching only
// "already exists" is what let an upgrade abort with "Database initialization
// failed: duplicate column name: public_port" — the panel refused to start over
// a migration step that had, in fact, already succeeded.
func TestIsAlreadyThere(t *testing.T) {
	benign := []string{
		"index idx_enable_traffic_reset already exists",
		"duplicate column name: public_port",
		"table inbounds already exists",
		"SQL logic error: duplicate column name: s3",
	}
	for _, msg := range benign {
		if !isAlreadyThere(errors.New(msg)) {
			t.Errorf("%q should be treated as harmless; failing here takes the panel down", msg)
		}
	}

	real := []string{
		"database is locked",
		"disk I/O error",
		"no such table: inbounds",
		"attempt to write a readonly database",
	}
	for _, msg := range real {
		if isAlreadyThere(errors.New(msg)) {
			t.Errorf("%q is a real failure and must not be swallowed", msg)
		}
	}
	if isAlreadyThere(nil) {
		t.Error("a nil error is not a migration collision")
	}
}

// TestUpgradeAddsPublicPortColumn walks the upgrade path: a database written by
// an older panel has no public_port, and opening it with this one must add it.
func TestUpgradeAddsPublicPortColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x-ui.db")
	if err := InitDB(path); err != nil {
		t.Fatalf("first init: %v", err)
	}
	if err := db.Exec("ALTER TABLE inbounds DROP COLUMN public_port").Error; err != nil {
		t.Fatalf("drop column: %v", err)
	}
	if err := InitDB(path); err != nil {
		t.Fatalf("upgrade failed: %v", err)
	}
	if !columnExists("inbounds", "public_port") {
		t.Error("public_port is missing after the upgrade")
	}
}

// TestAddColumnIsQuietWhenTheColumnArrivedFirst: during an upgrade the panel
// and the installer have the database open together — the update script starts
// the service and then runs `x-ui setting -show` — so both can look at a column
// neither has yet and both go to add it. SQLite tells the loser the column is
// already there, which is the outcome that was wanted; printed on the console
// in the middle of the installer's output, it reads as a failed upgrade.
//
// The race itself needs two processes. The branch it lands in does not: SQLite
// compares column names case-insensitively while pragma_table_info compares
// them case-sensitively, so asking for a column that is already there under
// another case takes exactly the same path — the check says it is missing, the
// statement says it is already there.
func TestAddColumnIsQuietWhenTheColumnArrivedFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x-ui.db")
	if err := InitDB(path); err != nil {
		t.Fatalf("init: %v", err)
	}
	if !columnExists("inbounds", "public_port") {
		t.Fatal("the column this test races for is not there to begin with")
	}

	var said bytes.Buffer
	log.SetOutput(&said)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	addColumn("inbounds", "PUBLIC_PORT", "integer DEFAULT 0")
	if said.Len() > 0 {
		t.Errorf("a column another process had already added was reported as a failure: %s", said.String())
	}

	// Anything that is not that must still be heard.
	said.Reset()
	addColumn("inbounds", "unbuildable", "integer DEFAULT")
	if said.Len() == 0 {
		t.Error("a column that genuinely could not be added went by in silence")
	}
}
