package database

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/coinman-dev/3ax-ui/v2/database/model"
)

func awgFixture() (*model.AwgServer, []model.AwgClient) {
	return &model.AwgServer{
			Enable: true, InterfaceName: "awg0", ListenPort: 55200, MTU: 1420,
			PrivateKey: "AWG_PRIV", PublicKey: "AWG_PUB",
			IPv4Address: "10.66.66.1/24", IPv4Pool: "10.66.66.0/24",
			IPv6Enabled: true, IPv6Address: "2a00::1/112", IPv6Pool: "2a00::/112",
			Jc: 4, Jmin: 50, Jmax: 1000, H1: "1", H2: "2", H3: "3", H4: "4",
			RouteViaXray: true, XrayInboundTag: "awg-tproxy-in", XrayTproxyPort: 12345,
		}, []model.AwgClient{
			{UUID: "a-1", Name: "alice", Email: "alice", Enable: true, PublicKey: "A1",
				IPv4Address: "10.66.66.2/32", Upload: 111, Download: 222, AllTime: 333},
			{UUID: "a-2", Name: "bob", Email: "bob", Enable: false, PublicKey: "A2",
				IPv4Address: "10.66.66.3/32", ForwardedPorts: "37015"},
		}
}

func wgFixture() (*model.WgServer, []model.WgClient) {
	return &model.WgServer{
			Enable: false, InterfaceName: "wg0", ListenPort: 51820, MTU: 1420,
			PrivateKey: "WG_PRIV", PublicKey: "WG_PUB",
			IPv4Address: "10.77.77.1/24", IPv4Pool: "10.77.77.0/24",
			XrayInboundTag: "wg-tproxy-in", XrayTproxyPort: 12346,
		}, []model.WgClient{
			// Same label as an AWG client on purpose: emails are unique per
			// server in the merged table, not globally.
			{UUID: "w-1", Name: "alice", Email: "alice", Enable: true, PublicKey: "W1",
				IPv4Address: "10.77.77.2/32"},
		}
}

func initFixtureDB(t *testing.T) {
	t.Helper()
	if err := InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	awgSrv, awgCl := awgFixture()
	wgSrv, wgCl := wgFixture()
	if err := db.Create(awgSrv).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(wgSrv).Error; err != nil {
		t.Fatal(err)
	}
	for i := range awgCl {
		awgCl[i].ServerId = awgSrv.Id
		// The legacy model carries gorm:"default:true" on Enable, so Create
		// turns an explicit false into true — and then reads the default back
		// into the struct, which is why the wanted value is captured first.
		// This fixture needs a genuinely disabled client, and that quirk is
		// exactly why the merged model drops the default.
		wantEnabled := awgCl[i].Enable
		if err := db.Create(&awgCl[i]).Error; err != nil {
			t.Fatal(err)
		}
		if !wantEnabled {
			if err := db.Model(&model.AwgClient{}).Where("uuid = ?", awgCl[i].UUID).
				Update("enable", false).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := range wgCl {
		wgCl[i].ServerId = wgSrv.Id
		if err := db.Create(&wgCl[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// TestTunnelSyncCopiesBothFlavours is the core of the schema merge: the four
// legacy tables must land in two, with the flavour on the server, the clients
// attached to the right one, and every value carried over unchanged.
func TestTunnelSyncCopiesBothFlavours(t *testing.T) {
	initFixtureDB(t)
	syncTunnelTablesFromLegacy(db)

	var servers []model.TunnelServer
	if err := db.Order("kind").Find(&servers).Error; err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 {
		t.Fatalf("tunnel_servers has %d rows, want 2", len(servers))
	}
	awg, wg := servers[0], servers[1]
	if awg.Kind != model.TunnelKindAwg || wg.Kind != model.TunnelKindWg {
		t.Fatalf("kinds are wrong: %q / %q", awg.Kind, wg.Kind)
	}

	// Values carried over, including the obfuscation set that only AWG has.
	if awg.PrivateKey != "AWG_PRIV" || awg.ListenPort != 55200 || !awg.RouteViaXray {
		t.Errorf("AWG server fields not carried over: %+v", awg)
	}
	if awg.Jc != 4 || awg.Jmax != 1000 || awg.H4 != "4" {
		t.Errorf("obfuscation not carried over: Jc=%d Jmax=%d H4=%q", awg.Jc, awg.Jmax, awg.H4)
	}
	if wg.Jc != 0 || wg.H1 != "" {
		t.Errorf("WireGuard row must leave obfuscation empty, got Jc=%d H1=%q", wg.Jc, wg.H1)
	}
	if wg.PrivateKey != "WG_PRIV" || wg.XrayTproxyPort != 12346 {
		t.Errorf("WG server fields not carried over: %+v", wg)
	}

	var awgClients, wgClients []model.TunnelClient
	db.Where("server_id = ?", awg.Id).Order("uuid").Find(&awgClients)
	db.Where("server_id = ?", wg.Id).Find(&wgClients)
	if len(awgClients) != 2 || len(wgClients) != 1 {
		t.Fatalf("clients split wrongly: awg=%d wg=%d", len(awgClients), len(wgClients))
	}
	if awgClients[0].UUID != "a-1" || awgClients[0].Upload != 111 || awgClients[0].AllTime != 333 {
		t.Errorf("client traffic counters not carried over: %+v", awgClients[0])
	}
	if awgClients[1].Enable || awgClients[1].ForwardedPorts != "37015" {
		t.Errorf("disabled client / port forwarding not carried over: %+v", awgClients[1])
	}
	// The merged model must be able to store a disabled client through a plain
	// Create — that is what the missing gorm default buys us.
	fresh := model.TunnelClient{ServerId: awg.Id, UUID: "fresh", Email: "fresh", Enable: false}
	if err := db.Create(&fresh).Error; err != nil {
		t.Fatal(err)
	}
	var reread model.TunnelClient
	if err := db.Where("uuid = ?", "fresh").First(&reread).Error; err != nil {
		t.Fatal(err)
	}
	if reread.Enable {
		t.Error("merged model still flips an explicitly disabled client to enabled on Create")
	}
	db.Where("uuid = ?", "fresh").Delete(&model.TunnelClient{})
	// The same email exists under both servers — allowed after the merge.
	if wgClients[0].Email != "alice" {
		t.Errorf("WG client email = %q, want alice", wgClients[0].Email)
	}
}

// TestTunnelSyncIsIdempotentAndStable: the sync runs on every start, so it must
// not duplicate rows or reshuffle the ids the API exposes.
func TestTunnelSyncIsIdempotentAndStable(t *testing.T) {
	initFixtureDB(t)
	syncTunnelTablesFromLegacy(db)

	var firstServers []model.TunnelServer
	var firstClients []model.TunnelClient
	db.Order("kind").Find(&firstServers)
	db.Order("uuid").Find(&firstClients)

	for range 3 {
		syncTunnelTablesFromLegacy(db)
	}

	var servers []model.TunnelServer
	var clients []model.TunnelClient
	db.Order("kind").Find(&servers)
	db.Order("uuid").Find(&clients)

	if len(servers) != len(firstServers) || len(clients) != len(firstClients) {
		t.Fatalf("rows multiplied: servers %d→%d, clients %d→%d",
			len(firstServers), len(servers), len(firstClients), len(clients))
	}
	for i := range servers {
		if servers[i].Id != firstServers[i].Id {
			t.Errorf("server id changed for kind %s: %d → %d", servers[i].Kind, firstServers[i].Id, servers[i].Id)
		}
	}
	for i := range clients {
		if clients[i].Id != firstClients[i].Id {
			t.Errorf("client id changed for %s: %d → %d", clients[i].UUID, firstClients[i].Id, clients[i].Id)
		}
	}
}

// TestTunnelSyncTracksLegacyChanges: while the legacy tables stay
// authoritative, edits and deletions there must reach the merged tables.
func TestTunnelSyncTracksLegacyChanges(t *testing.T) {
	initFixtureDB(t)
	syncTunnelTablesFromLegacy(db)

	if err := db.Model(&model.AwgClient{}).Where("uuid = ?", "a-1").
		Updates(map[string]any{"email": "alice2", "upload": 999}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("uuid = ?", "a-2").Delete(&model.AwgClient{}).Error; err != nil {
		t.Fatal(err)
	}
	syncTunnelTablesFromLegacy(db)

	var c model.TunnelClient
	if err := db.Where("uuid = ?", "a-1").First(&c).Error; err != nil {
		t.Fatal(err)
	}
	if c.Email != "alice2" || c.Upload != 999 {
		t.Errorf("edit not propagated: %+v", c)
	}
	var gone int64
	db.Model(&model.TunnelClient{}).Where("uuid = ?", "a-2").Count(&gone)
	if gone != 0 {
		t.Error("client deleted from the legacy table is still in tunnel_clients")
	}

	// Removing the whole WireGuard server takes its clients with it.
	if err := db.Where("1 = 1").Delete(&model.WgServer{}).Error; err != nil {
		t.Fatal(err)
	}
	syncTunnelTablesFromLegacy(db)
	var wgRows int64
	db.Model(&model.TunnelServer{}).Where("kind = ?", model.TunnelKindWg).Count(&wgRows)
	if wgRows != 0 {
		t.Error("wg server removed from the legacy table is still in tunnel_servers")
	}
	var orphans int64
	db.Model(&model.TunnelClient{}).Where("uuid = ?", "w-1").Count(&orphans)
	if orphans != 0 {
		t.Error("clients of the removed server were left behind")
	}
}

// TestMergedModelCoversLegacyFields fails if a field is ever added to a legacy
// model without adding it to the merged one — the copier would silently skip it.
func TestMergedModelCoversLegacyFields(t *testing.T) {
	check := func(legacy, merged any, name string) {
		l, m := reflect.TypeOf(legacy), reflect.TypeOf(merged)
		for i := range l.NumField() {
			f := l.Field(i)
			mf, ok := m.FieldByName(f.Name)
			if !ok {
				t.Errorf("%s: merged model has no field %s", name, f.Name)
				continue
			}
			if mf.Type != f.Type {
				t.Errorf("%s: field %s is %s in the legacy model and %s in the merged one",
					name, f.Name, f.Type, mf.Type)
			}
		}
	}
	check(model.AwgServer{}, model.TunnelServer{}, "AwgServer")
	check(model.WgServer{}, model.TunnelServer{}, "WgServer")
	check(model.AwgClient{}, model.TunnelClient{}, "AwgClient")
	check(model.WgClient{}, model.TunnelClient{}, "WgClient")
}

// TestTunnelSyncSurvivesLegacyColumnDrift models the production database, whose
// awg_servers still carries a legacy `dns` column the current model dropped.
// The sync must ignore unknown columns rather than fail or lose the row.
func TestTunnelSyncSurvivesLegacyColumnDrift(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	if err := db.Exec(`ALTER TABLE awg_servers ADD COLUMN dns text DEFAULT "1.1.1.1"`).Error; err != nil {
		t.Fatalf("add legacy column: %v", err)
	}
	if err := db.Exec(`INSERT INTO awg_servers
		(enable, interface_name, listen_port, mtu, private_key, public_key,
		 ipv4_address, ipv4_pool, ipv6_enabled, jc, jmin, jmax, h1, h2, h3, h4, dns)
		VALUES (1, 'awg0', 55200, 1420, 'P', 'U', '10.66.66.1/24', '10.66.66.0/24', 0,
		        4, 50, 1000, '1', '2', '3', '4', '9.9.9.9')`).Error; err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	var serverId int
	if err := db.Raw(`SELECT id FROM awg_servers LIMIT 1`).Scan(&serverId).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO awg_clients (server_id, uuid, name, email, enable, public_key, ipv4_address)
		VALUES (?, 'drift-1', 'alice', 'alice', 1, 'K', '10.66.66.2/32')`, serverId).Error; err != nil {
		t.Fatal(err)
	}

	syncTunnelTablesFromLegacy(db)

	var srv model.TunnelServer
	if err := db.Where("kind = ?", model.TunnelKindAwg).First(&srv).Error; err != nil {
		t.Fatalf("server not migrated from a drifted table: %v", err)
	}
	if srv.PrivateKey != "P" || srv.ListenPort != 55200 || srv.Jc != 4 {
		t.Errorf("values lost on a drifted table: %+v", srv)
	}
	var clients int64
	db.Model(&model.TunnelClient{}).Where("server_id = ?", srv.Id).Count(&clients)
	if clients != 1 {
		t.Errorf("clients migrated: %d, want 1", clients)
	}
}

// TestMigrationRunsOnlyOnce is the guard that protects live data: after the
// services switch to the merged tables, a second pass over the frozen legacy
// tables would silently overwrite everything written since the upgrade.
func TestMigrationRunsOnlyOnce(t *testing.T) {
	initFixtureDB(t)

	migrateTunnelTablesFromLegacy(db)
	var srv model.TunnelServer
	if err := db.Where("kind = ?", model.TunnelKindAwg).First(&srv).Error; err != nil {
		t.Fatalf("first migration did not run: %v", err)
	}

	// Simulate life after the switch: a client added and a setting changed in
	// the merged tables only.
	fresh := model.TunnelClient{ServerId: srv.Id, UUID: "post-upgrade", Email: "new", Enable: true}
	if err := db.Create(&fresh).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.TunnelServer{}).Where("id = ?", srv.Id).
		Update("listen_port", 44444).Error; err != nil {
		t.Fatal(err)
	}

	migrateTunnelTablesFromLegacy(db)

	var stillThere int64
	db.Model(&model.TunnelClient{}).Where("uuid = ?", "post-upgrade").Count(&stillThere)
	if stillThere != 1 {
		t.Error("a client added after the upgrade was wiped by a second migration pass")
	}
	var after model.TunnelServer
	db.Where("kind = ?", model.TunnelKindAwg).First(&after)
	if after.ListenPort != 44444 {
		t.Errorf("server edit made after the upgrade was overwritten: listen_port = %d", after.ListenPort)
	}
}

// TestInitDBSurvivesStrayIndexName reproduces the upgrade failure seen on a
// production panel: SQLite keeps index names global to the database, gorm looks
// for an index only on the table it is migrating, and the name had ended up on
// another table — from the table rebuild the driver does when adding a column,
// or from a second x-ui process migrating at the same time. AutoMigrate then
// failed, InitDB returned an error and the panel started with its schema
// half-applied.
func TestInitDBSurvivesStrayIndexName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x-ui.db")
	if err := InitDB(path); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	// Put one of our index names on the wrong table.
	if err := db.Exec("DROP INDEX IF EXISTS idx_awg_enable_last_online").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE INDEX idx_awg_enable_last_online ON client_traffics(enable, last_online)").Error; err != nil {
		t.Fatal(err)
	}

	if err := InitDB(path); err != nil {
		t.Fatalf("InitDB must survive a stray index name, got: %v", err)
	}

	// The index is back where it belongs, so the query it serves stays indexed.
	var onRightTable int64
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_awg_enable_last_online' AND tbl_name = 'awg_clients'`).
		Scan(&onRightTable).Error; err != nil {
		t.Fatal(err)
	}
	if onRightTable != 1 {
		t.Error("the index was not recreated on awg_clients")
	}

	// And the schema is complete: the merged tunnel tables must exist, which is
	// what the failed run on the live panel could have skipped.
	for _, table := range []string{"tunnel_servers", "tunnel_clients"} {
		var n int64
		db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n)
		if n != 1 {
			t.Errorf("table %s missing after recovery", table)
		}
	}
}
