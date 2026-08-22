package database

import (
	"path/filepath"
	"testing"
)

// TestOnlineLookupIndexes guards the composite indexes backing the "seen
// recently" query (enable + last_online) that the traffic job runs every 10
// seconds for every protocol, plus the inbound_id lookup on client_traffics.
// Without them SQLite scans the whole table on each tick.
func TestOnlineLookupIndexes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x-ui.db")
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	want := map[string]string{
		"idx_ct_enable_last_online":      "client_traffics",
		"idx_awg_enable_last_online":     "awg_clients",
		"idx_wg_enable_last_online":      "wg_clients",
		"idx_mtproto_enable_last_online": "mtproto_clients",
	}
	for index, table := range want {
		var n int64
		err := db.Raw(
			"SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ? AND tbl_name = ?",
			index, table,
		).Scan(&n).Error
		if err != nil {
			t.Fatalf("query index %s: %v", index, err)
		}
		if n != 1 {
			t.Errorf("index %s on %s: found %d, want 1", index, table, n)
		}
	}

	// inbound_id is queried on its own (per-inbound client lists); gorm names
	// single-column index tags idx_<table>_<column>.
	var n int64
	if err := db.Raw(
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = 'client_traffics' AND sql LIKE '%inbound_id%'",
	).Scan(&n).Error; err != nil {
		t.Fatalf("query inbound_id index: %v", err)
	}
	if n == 0 {
		t.Error("client_traffics has no index covering inbound_id")
	}
}
