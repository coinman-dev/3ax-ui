package database

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// ax181Data is a 3AX-UI 1.8.1 panel: a VLESS client "alice", an AmneziaWG
// server whose inbound row still carries the inbound modal's stale client
// snapshot, a native WireGuard server, and MTProto with a repeated name.
const ax181Data = `
INSERT INTO users (username, password) VALUES ('admin', 'x');
INSERT INTO history_of_seeders (seeder_name) VALUES ('UserPasswordHash'), ('AwgWgPeerBaseline');
INSERT INTO inbounds (id, user_id, up, down, total, all_time, remark, enable, expiry_time, traffic_reset, listen, port, protocol, settings, stream_settings, tag, sniffing, public_port) VALUES
 (1, 1, 0, 0, 0, 0, 'vless', 1, 0, 'never', '', 443, 'vless', '{"clients":[{"id":"6f1c3c5e-6c0a-4b8e-9d0c-2f3a1b2c3d4e","email":"alice","enable":true,"subId":"alicesub"}],"decryption":"none"}', '{"network":"tcp","security":"none"}', 'inbound-443', '{}', 0),
 (2, 1, 0, 0, 0, 0, 'awg', 1, 0, 'monthly', '', 40000, 'amneziawg', '{"clients":[{"id":"u-bob","email":"bob","enable":true,"_awgId":1}]}', '', 'inbound-40000', '', 0),
 (3, 1, 0, 0, 0, 0, 'wg', 1, 0, 'never', '', 51821, 'nativewg', '{"clients":[]}', '', 'inbound-51821', '', 0),
 (4, 1, 0, 0, 0, 0, 'tg', 1, 0, 'never', '', 8443, 'mtproto', '{"fakeTlsDomain":"www.cloudflare.com","routeThroughXray":false,"antiReplay":{"enabled":true}}', '', 'inbound-8443', '', 0);
INSERT INTO client_traffics (inbound_id, enable, email, up, down, all_time, expiry_time, total, reset, last_online) VALUES
 (1, 1, 'alice', 5, 6, 11, 0, 0, 0, 0),
 (2, 1, 'bob', 999, 999, 0, 0, 0, 0, 0);
INSERT INTO tunnel_servers (kind, id, enable, interface_name, listen_port, mtu, private_key, public_key, ipv4_address, ipv4_pool,
  ipv6_enabled, ipv6_address, ipv6_pool, ipv6_gateway, jc, jmin, jmax, s1, s2, s3, s4, h1, h2, h3, h4, i1, i2, i3, i4, i5,
  header_protection_key, content_padding_addition, rekey_after_time, rekey_timeout, reject_after_time, keepalive_timeout, max_handshake_attempts,
  random_trailers, disable_cookies, dns_ipv4, dns_ipv6, external_interface, ipv6_external_interface, post_up, post_down, endpoint, traffic_reset,
  route_via_xray, xray_inbound_tag, xray_tproxy_port) VALUES
 ('awg', 1, 1, 'awg0', 51820, 1380, 'awgPriv', 'awgPub', '10.66.66.1/24', '10.66.66.0/24',
  1, '2001:db8::1/112', '2001:db8::/112', 'fe80::1', 4, 40, 70, 12, 20, 14, 16, '100000-800000', '900000-1000000', '1100000-1200000', '1300000-1400000', '<r 32>', '', '', '', '',
  '', '', '', '', '', '', '', 0, 0, '1.1.1.1', '2606:4700:4700::1111', 'eth0', '', 'PostUp', 'PostDown', 'vpn.example.com', 'never',
  1, 'awg-tproxy-in', 12345),
 ('wg', 2, 1, 'wg0', 51821, 1420, 'wgPriv', 'wgPub', '10.77.77.1/24', '10.77.77.0/24',
  0, '', '', '', 0, 0, 0, 0, 0, 0, 0, '', '', '', '', '', '', '', '', '',
  '', '', '', '', '', '', '', 0, 0, '1.1.1.1', '', '', '', '', '', '', 'never',
  0, 'wg-tproxy-in', 12346);
INSERT INTO tunnel_clients (server_id, uuid, name, email, enable, comment, private_key, public_key, preshared_key, ipv4_address, ipv6_address,
  allowed_ips, client_allowed_ips, forwarded_ports, persistent_keepalive, upload, download, total_gb, all_time, last_peer_up, last_peer_down,
  expiry_time, reset, limit_ip, tg_id, last_online, last_ip, created_at, updated_at) VALUES
 (1, 'u-bob', 'Bob phone', 'bob', 1, '', 'bobPriv', 'bobPub', 'bobPsk', '10.66.66.2/32', '2001:db8::2/128',
  '10.66.66.2/32,2001:db8::2/128', '0.0.0.0/0,::/0', '80,443', 25, 100, 200, 10737418240, 300, 0, 0,
  1893456000000, 30, 2, 777, 1700000000000, '203.0.113.5', 1690000000000, 1690000000001),
 (1, 'u-alice', 'alice', 'alice', 0, 'second phone', 'alicePriv', 'alicePub', '', '10.66.66.3/32', '',
  '10.66.66.3/32', '0.0.0.0/0,::/0', '', 0, 7, 8, 0, 15, 0, 0,
  0, 0, 0, 0, 0, '', 1690000000000, 1690000000000),
 (2, 'u-carol', 'carol', 'carol', 1, '', 'carolPriv', 'carolPub', '', '10.77.77.2/32', '',
  '10.77.77.2/32', '0.0.0.0/0,::/0', '', 25, 1, 2, 0, 3, 0, 0,
  0, 0, 0, 0, 0, '', 1690000000000, 1690000000000);
INSERT INTO mtproto_clients (inbound_id, uuid, email, enable, secret, comment, sub_id, upload, download, all_time, total_gb, expiry_time, reset, limit_ip, tg_id, last_online, created_at, updated_at) VALUES
 (4, 'm-1', 'dave', 1, 'ee0123456789abcdef0123456789abcdef7777772e636c6f7564666c6172652e636f6d', '', 'davesub', 10, 20, 30, 0, 0, 0, 0, 0, 0, 1690000000000, 1690000000000),
 (4, 'm-2', 'dave', 1, 'eefedcba9876543210fedcba98765432107777772e636c6f7564666c6172652e636f6d', 'tablet', '', 1, 2, 3, 0, 0, 0, 0, 0, 0, 1690000000000, 1690000000000);
INSERT INTO settings (key, value) VALUES ('xrayTemplateConfig', '{"routing":{"rules":[{"type":"field","inboundTag":["awg-tproxy-in","api"],"outboundTag":"warp"}]}}');
`

func buildAx181DB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x-ui.db")
	schema, err := os.ReadFile(filepath.Join("testdata", "ax181_schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, stmt := range strings.Split(string(schema)+ax181Data, ";\n") {
		if strings.TrimSpace(stmt) == "" || strings.HasPrefix(strings.TrimSpace(stmt), "--") && !strings.Contains(stmt, "\n") {
			continue
		}
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("building 1.8.1 database: %v\n%s", err, stmt)
		}
	}
	return path
}

func migratedAx181(t *testing.T) string {
	t.Helper()
	path := buildAx181DB(t)
	if err := InitDB(path); err != nil {
		t.Fatalf("InitDB on a 1.8.1 database: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	return path
}

func inboundByID(t *testing.T, id int) model.Inbound {
	t.Helper()
	var ib model.Inbound
	if err := GetDB().First(&ib, id).Error; err != nil {
		t.Fatalf("inbound %d: %v", id, err)
	}
	return ib
}

func awgSettings(t *testing.T, ib model.Inbound) amneziawg.InboundSettings {
	t.Helper()
	var s amneziawg.InboundSettings
	if err := json.Unmarshal([]byte(ib.Settings), &s); err != nil || s.Server == nil {
		t.Fatalf("inbound %d settings are not upstream AmneziaWG settings: %v\n%s", ib.Id, err, ib.Settings)
	}
	return s
}

func clientByEmail(t *testing.T, clients []model.Client, email string) model.Client {
	t.Helper()
	for _, c := range clients {
		if c.Email == email {
			return c
		}
	}
	t.Fatalf("no client %q among %d", email, len(clients))
	return model.Client{}
}

func trafficByEmail(t *testing.T, email string) xray.ClientTraffic {
	t.Helper()
	var ct xray.ClientTraffic
	if err := GetDB().Where("email = ?", email).First(&ct).Error; err != nil {
		t.Fatalf("client_traffics %q: %v", email, err)
	}
	return ct
}

func TestAx181AmneziaWGServerBecomesKernelModeInbound(t *testing.T) {
	migratedAx181(t)
	ib := inboundByID(t, 2)
	if ib.Protocol != model.AmneziaWG || ib.Port != 51820 || ib.Tag != "inbound-40000" || !ib.Enable || ib.TrafficReset != "monthly" {
		t.Fatalf("inbound 2 = protocol %s port %d tag %s enable %v reset %s", ib.Protocol, ib.Port, ib.Tag, ib.Enable, ib.TrafficReset)
	}
	if ib.ShareAddrStrategy != "custom" || ib.ShareAddr != "vpn.example.com" {
		t.Errorf("endpoint override lost: strategy %q addr %q", ib.ShareAddrStrategy, ib.ShareAddr)
	}
	s := awgSettings(t, ib)
	srv := s.Server
	if !srv.KernelMode || srv.PrivateKey != "awgPriv" || srv.PublicKey != "awgPub" || srv.SubnetIP != "10.66.66.0" || srv.SubnetCIDR != 24 || srv.MTU != 1380 {
		t.Errorf("server identity: %+v", srv)
	}
	if srv.Jc != 4 || srv.Jmin != 40 || srv.Jmax != 70 || srv.S1 != 12 || srv.S4 != 16 || srv.H1 != "100000-800000" || srv.H4 != "1300000-1400000" || srv.I1 != "<r 32>" {
		t.Errorf("obfuscation: %+v", srv)
	}
	if !srv.IPv6Enabled || srv.IPv6Subnet != "2001:db8::/112" || srv.ExternalInterface != "eth0" || !srv.RouteThroughXray {
		t.Errorf("network: %+v", srv)
	}
	if srv.PrimaryDNS != "1.1.1.1" || srv.SecondaryDNS != "2606:4700:4700::1111" {
		t.Errorf("dns %q %q", srv.PrimaryDNS, srv.SecondaryDNS)
	}
	var raw struct {
		Server map[string]any `json:"server"`
	}
	_ = json.Unmarshal([]byte(ib.Settings), &raw)
	if raw.Server["ipv6Gateway"] != "fe80::1" {
		t.Errorf("ipv6Gateway not kept for the NDP port: %v", raw.Server["ipv6Gateway"])
	}
	if len(s.Clients) != 2 {
		t.Fatalf("inbound 2 has %d clients, want bob and the renamed alice: %+v", len(s.Clients), s.Clients)
	}
	// limitIp is not checked: upstream's ResetIpLimitNoFail2ban clears it on a host without fail2ban.
	bob := clientByEmail(t, s.Clients, "bob")
	if !bob.Enable || bob.PrivateKey != "bobPriv" || bob.PublicKey != "bobPub" || bob.PreSharedKey != "bobPsk" ||
		!slices.Equal(bob.AllowedIPs, []string{"10.66.66.2/32", "2001:db8::2/128"}) || bob.ForwardedPortsSpec() != "80,443" ||
		bob.KeepAlive == nil || bob.KeepAlive.Seconds() != 25 || bob.TotalGB != 10737418240 || bob.ExpiryTime != 1893456000000 ||
		bob.Reset != 30 || bob.TgID != 777 || bob.Comment != "Bob phone" || bob.SubID == "" {
		t.Errorf("bob = %+v", bob)
	}
}

func TestAx181TrafficCountersMoveAndStaleRowsGo(t *testing.T) {
	migratedAx181(t)
	bob := trafficByEmail(t, "bob")
	if bob.InboundId != 2 || bob.Up != 100 || bob.Down != 200 || bob.Total != 10737418240 || bob.ExpiryTime != 1893456000000 || bob.Reset != 30 || bob.LastOnline != 1700000000000 {
		t.Errorf("bob traffic = %+v, want tunnel_clients' counters instead of the stale snapshot row", bob)
	}
	alice := trafficByEmail(t, "alice")
	if alice.InboundId != 1 || alice.Up != 5 || alice.Down != 6 {
		t.Errorf("VLESS alice traffic changed: %+v", alice)
	}
}

// Upstream merges same-email clients into one identity; 1.x tunnel and MTProto
// clients were separate people, so a clash is renamed rather than merged.
func TestAx181CollidingNamesAreRenamedNotMerged(t *testing.T) {
	migratedAx181(t)
	s := awgSettings(t, inboundByID(t, 2))
	renamed := clientByEmail(t, s.Clients, "alice-awg")
	if renamed.Enable || renamed.PublicKey != "alicePub" || renamed.Comment != "second phone" {
		t.Errorf("renamed AWG alice = %+v", renamed)
	}
	if ct := trafficByEmail(t, "alice-awg"); ct.InboundId != 2 || ct.Up != 7 || ct.Down != 8 || ct.Enable {
		t.Errorf("alice-awg traffic = %+v", ct)
	}
	var tg map[string]any
	if err := json.Unmarshal([]byte(inboundByID(t, 4).Settings), &tg); err != nil {
		t.Fatal(err)
	}
	var mt struct {
		Clients []model.Client `json:"clients"`
	}
	_ = json.Unmarshal([]byte(inboundByID(t, 4).Settings), &mt)
	d1, d2 := clientByEmail(t, mt.Clients, "dave"), clientByEmail(t, mt.Clients, "dave-2")
	if !strings.HasPrefix(d1.Secret, "ee0123") || !strings.HasPrefix(d2.Secret, "eefedc") || d2.Comment != "tablet" || d1.SubID != "davesub" || d2.SubID == "" {
		t.Errorf("MTProto clients = %+v / %+v", d1, d2)
	}
	if tg["antiReplay"] == nil || tg["fakeTlsDomain"] != "www.cloudflare.com" {
		t.Errorf("MTProto settings lost keys: %v", tg)
	}
	if ct := trafficByEmail(t, "dave-2"); ct.InboundId != 4 || ct.Up != 1 || ct.Down != 2 {
		t.Errorf("dave-2 traffic = %+v", ct)
	}
	var records []model.ClientRecord
	if err := GetDB().Order("email").Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	var emails []string
	for _, r := range records {
		emails = append(emails, r.Email)
	}
	if want := []string{"alice", "alice-awg", "bob", "carol", "dave", "dave-2"}; !slices.Equal(emails, want) {
		t.Errorf("client records = %v, want %v", emails, want)
	}
}

func TestAx181NativeWireGuardBecomesPlainAmneziaWG(t *testing.T) {
	migratedAx181(t)
	ib := inboundByID(t, 3)
	if ib.Protocol != model.AmneziaWG || ib.Port != 51821 {
		t.Fatalf("inbound 3 = %s:%d", ib.Protocol, ib.Port)
	}
	s := awgSettings(t, ib)
	if err := amneziawg.ValidateServerObfuscation(s.Server.Obfuscation()); err != nil {
		t.Errorf("former WireGuard obfuscation rejected: %v", err)
	}
	o := s.Server.Obfuscation()
	if o.Jc != 0 || o.S1 != 0 || o.S2 != 0 || o.H1 != "" || o.HeaderProtectionKey != "" || !s.Server.KernelMode {
		t.Errorf("former WireGuard must stay wire-compatible with WireGuard clients: %+v", s.Server)
	}
	carol := clientByEmail(t, s.Clients, "carol")
	if carol.PublicKey != "carolPub" || !slices.Equal(carol.AllowedIPs, []string{"10.77.77.2/32"}) {
		t.Errorf("carol = %+v", carol)
	}
}

func TestAx181RoutingRulesFollowTheTunnelInbound(t *testing.T) {
	migratedAx181(t)
	var setting model.Setting
	if err := GetDB().Where("key = ?", "xrayTemplateConfig").First(&setting).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(setting.Value, `"inbound-40000"`) || strings.Contains(setting.Value, "awg-tproxy-in") || !strings.Contains(setting.Value, `"api"`) {
		t.Errorf("routing rule still names the old TPROXY tag: %s", setting.Value)
	}
}

func TestAx181RecordsTheTunnelsToRetire(t *testing.T) {
	migratedAx181(t)
	var setting model.Setting
	if err := GetDB().Where("key = ?", AxLegacyHostCleanupKey).First(&setting).Error; err != nil {
		t.Fatalf("no host cleanup recorded: %v", err)
	}
	var got []AxLegacyInterface
	if err := json.Unmarshal([]byte(setting.Value), &got); err != nil {
		t.Fatal(err)
	}
	want := []AxLegacyInterface{{Kind: "awg", Name: "awg0"}, {Kind: "wg", Name: "wg0"}}
	if !slices.Equal(got, want) {
		t.Errorf("interfaces to retire = %v, want %v", got, want)
	}
}

func TestAx181MigrationBacksUpAndRunsOnce(t *testing.T) {
	path := migratedAx181(t)
	backups, _ := filepath.Glob(path + ".3ax-1x-backup-*")
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want exactly one copy of the 1.x database", backups)
	}
	if err := CloseDB(); err != nil {
		t.Fatal(err)
	}
	if err := InitDB(path); err != nil {
		t.Fatalf("second start: %v", err)
	}
	var count int64
	GetDB().Model(&xray.ClientTraffic{}).Count(&count)
	if count != 6 {
		t.Errorf("client_traffics rows after a second start = %d, want 6", count)
	}
	backups, _ = filepath.Glob(path + ".3ax-1x-backup-*")
	if len(backups) != 1 {
		t.Errorf("a second start made another backup: %v", backups)
	}
}

func TestFreshDatabaseSkipsTheAx181Migration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x-ui.db")
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	if backups, _ := filepath.Glob(path + ".3ax-1x-backup-*"); len(backups) != 0 {
		t.Errorf("fresh install made a 1.x backup: %v", backups)
	}
}
