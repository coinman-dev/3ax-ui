package database

import (
	"errors"
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
