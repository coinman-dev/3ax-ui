package service

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/tunnel"
)

// TestTunnelFlavoursStayIsolated is the regression test for the schema merge:
// AmneziaWG and native WireGuard now share tunnel_servers/tunnel_clients, and
// every query in either service must stay inside its own flavour. Before the
// merge that isolation came free from having separate tables — now it is a
// property of the code, so it needs a test.
func TestTunnelFlavoursStayIsolated(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	db := database.GetDB()
	awg, wg := &AwgService{}, &WgService{}

	awgServer, err := awg.GetServer()
	if err != nil {
		t.Fatalf("AWG GetServer: %v", err)
	}
	wgServer, err := wg.GetServer()
	if err != nil {
		t.Fatalf("WG GetServer: %v", err)
	}

	// Two distinct rows, correctly tagged.
	if awgServer.Id == wgServer.Id {
		t.Fatalf("both services got the same server row (id %d)", awgServer.Id)
	}
	if awgServer.Kind != model.TunnelKindAwg || wgServer.Kind != model.TunnelKindWg {
		t.Fatalf("kinds are wrong: awg=%q wg=%q", awgServer.Kind, wgServer.Kind)
	}
	// GetServer is called on every request; it must not keep creating rows.
	if _, err := awg.GetServer(); err != nil {
		t.Fatal(err)
	}
	var servers int64
	db.Model(&model.TunnelServer{}).Count(&servers)
	if servers != 2 {
		t.Fatalf("tunnel_servers has %d rows, want 2", servers)
	}

	// Clients of one flavour must be invisible to the other, including the
	// lookups by id and uuid, which used to be scoped by the table itself.
	awgClient := model.TunnelClient{ServerId: awgServer.Id, UUID: "aaaaaaaa-0000-0000-0000-000000000001",
		Name: "alice", Email: "alice", Enable: true, IPv4Address: "10.66.66.2/32"}
	wgClient := model.TunnelClient{ServerId: wgServer.Id, UUID: "bbbbbbbb-0000-0000-0000-000000000002",
		Name: "bob", Email: "bob", Enable: true, IPv4Address: "10.77.77.2/32"}
	for _, c := range []*model.TunnelClient{&awgClient, &wgClient} {
		if err := db.Create(c).Error; err != nil {
			t.Fatal(err)
		}
	}

	awgClients, err := awg.GetClients()
	if err != nil {
		t.Fatal(err)
	}
	if len(awgClients) != 1 || awgClients[0].UUID != awgClient.UUID {
		t.Fatalf("AWG GetClients returned %d rows, want only its own: %+v", len(awgClients), awgClients)
	}
	wgClients, err := wg.GetClients()
	if err != nil {
		t.Fatal(err)
	}
	if len(wgClients) != 1 || wgClients[0].UUID != wgClient.UUID {
		t.Fatalf("WG GetClients returned %d rows, want only its own: %+v", len(wgClients), wgClients)
	}

	if _, err := awg.GetClient(wgClient.Id); err == nil {
		t.Error("AWG service fetched a WireGuard client by id")
	}
	if _, err := awg.GetClientByUUID(wgClient.UUID); err == nil {
		t.Error("AWG service fetched a WireGuard client by uuid")
	}
	if _, err := wg.GetClientByUUID(awgClient.UUID); err == nil {
		t.Error("WG service fetched an AmneziaWG client by uuid")
	}

	// Online lists are keyed by uuid and feed the inbounds page — they must not
	// bleed across flavours either.
	db.Model(&model.TunnelClient{}).Where("id = ?", wgClient.Id).
		Update("last_online", nowMilli())
	for _, uuid := range awg.GetOnlineClients() {
		if uuid == wgClient.UUID {
			t.Error("a WireGuard client showed up in the AmneziaWG online list")
		}
	}
}

func nowMilli() int64 {
	return 9_999_999_999_999
}

// TestRouteViaXrayReadsMergedTables guards the wiring that turns RouteViaXray
// into something that actually works: with the flag on, the panel must inject a
// dokodemo-door inbound for the tunnel and offer its tag for routing rules.
// Both used to be read from the legacy tables, which the services stopped
// writing after the schema merge — the tunnel would then have had its traffic
// redirected to a port nothing was listening on.
func TestRouteViaXrayReadsMergedTables(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	db := database.GetDB()

	awg := &AwgService{}
	server, err := awg.GetServer()
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if err := db.Model(&model.TunnelServer{}).Where("id = ?", server.Id).
		Updates(map[string]any{"enable": true, "route_via_xray": true}).Error; err != nil {
		t.Fatal(err)
	}

	inbounds := tunnelTproxyInbounds()
	if len(inbounds) != 1 {
		t.Fatalf("expected one TPROXY inbound for the enabled tunnel, got %d", len(inbounds))
	}
	if inbounds[0].Tag != server.XrayInboundTag {
		t.Errorf("inbound tag = %q, want %q", inbounds[0].Tag, server.XrayInboundTag)
	}
	if inbounds[0].Port != server.XrayTproxyPort {
		t.Errorf("inbound port = %d, want %d", inbounds[0].Port, server.XrayTproxyPort)
	}

	tags, err := (&InboundService{}).GetInboundTags()
	if err != nil {
		t.Fatalf("GetInboundTags: %v", err)
	}
	if !strings.Contains(tags, server.XrayInboundTag) {
		t.Errorf("routing tag list %s does not offer %q", tags, server.XrayInboundTag)
	}

	// Turning it off withdraws both.
	if err := db.Model(&model.TunnelServer{}).Where("id = ?", server.Id).
		Update("route_via_xray", false).Error; err != nil {
		t.Fatal(err)
	}
	if got := tunnelTproxyInbounds(); len(got) != 0 {
		t.Errorf("TPROXY inbound survived turning RouteViaXray off: %+v", got)
	}
}

// TestFreshServersGetTheirOwnDefaults: with both flavours in one table the
// schema can no longer say "10.66.66.0/24, but 10.77.77.0/24 for the other
// one", so the service fills those in. A WireGuard server inheriting the
// AmneziaWG subnet would collide with a live tunnel on the same host.
func TestFreshServersGetTheirOwnDefaults(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	awgServer, err := (&AwgService{}).GetServer()
	if err != nil {
		t.Fatal(err)
	}
	wgServer, err := (&WgService{}).GetServer()
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name   string
		got    *model.TunnelServer
		iface  string
		pool   string
		tag    string
		tproxy int
	}{
		{"awg", awgServer, "awg0", "10.66.66.0/24", "awg-tproxy-in", 12345},
		{"wg", wgServer, "wg0", "10.77.77.0/24", "wg-tproxy-in", 12346},
	} {
		if c.got.InterfaceName != c.iface {
			t.Errorf("%s: interface = %q, want %q", c.name, c.got.InterfaceName, c.iface)
		}
		if c.got.IPv4Pool != c.pool {
			t.Errorf("%s: pool = %q, want %q", c.name, c.got.IPv4Pool, c.pool)
		}
		if c.got.XrayInboundTag != c.tag {
			t.Errorf("%s: tproxy tag = %q, want %q", c.name, c.got.XrayInboundTag, c.tag)
		}
		if c.got.XrayTproxyPort != c.tproxy {
			t.Errorf("%s: tproxy port = %d, want %d", c.name, c.got.XrayTproxyPort, c.tproxy)
		}
	}
	if awgServer.ListenPort == wgServer.ListenPort {
		t.Errorf("both tunnels picked the same listen port %d", awgServer.ListenPort)
	}
}

// TestFreshAwgServerIsBornObfuscated: a fresh install used to serve the 1.x
// defaults — no junk packets at all — until someone found the Generate button,
// by which time changing the set disconnects the clients already on it. The
// record is now seeded at creation, and only at creation.
func TestFreshAwgServerIsBornObfuscated(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	server, err := (&AwgService{}).GetServer()
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if server.Jc == 0 || server.Jmin == 0 || server.Jmax == 0 {
		t.Errorf("junk packets left off: Jc=%d Jmin=%d Jmax=%d", server.Jc, server.Jmin, server.Jmax)
	}
	if server.S1 == 0 || server.S2 == 0 {
		t.Errorf("handshake padding left off: S1=%d S2=%d", server.S1, server.S2)
	}
	for i, h := range []string{server.H1, server.H2, server.H3, server.H4} {
		if !strings.Contains(h, "-") {
			t.Errorf("H%d = %q is not a 2.0 range", i+1, h)
		}
	}
	for i, iv := range []string{server.I1, server.I2, server.I3, server.I4, server.I5} {
		if iv == "" {
			t.Errorf("I%d was not generated", i+1)
		}
	}
	if err := tunnel.ValidateObfuscation(&model.TunnelServer{
		Jc: server.Jc, Jmin: server.Jmin, Jmax: server.Jmax,
		S1: server.S1, S2: server.S2, S3: server.S3, S4: server.S4,
		H1: server.H1, H2: server.H2, H3: server.H3, H4: server.H4,
		HeaderProtectionKey:    server.HeaderProtectionKey,
		ContentPaddingAddition: server.ContentPaddingAddition,
		RekeyAfterTime:         server.RekeyAfterTime,
		RejectAfterTime:        server.RejectAfterTime,
	}); err != nil {
		t.Errorf("the seeded set does not validate: %v", err)
	}

	// Second call must not regenerate: the operator's own values would be
	// overwritten on every page load.
	again, err := (&AwgService{}).GetServer()
	if err != nil {
		t.Fatalf("GetServer again: %v", err)
	}
	if again.Jc != server.Jc || again.H1 != server.H1 || again.I2 != server.I2 {
		t.Errorf("the set was regenerated on a later read")
	}

	// An existing record — the upgrade path — keeps whatever it had.
	db := database.GetDB()
	if err := db.Model(&model.TunnelServer{}).Where("id = ?", server.Id).
		Updates(map[string]any{"jc": 0, "jmin": 0, "jmax": 0, "s1": 0, "s2": 0,
			"h1": "1", "h2": "2", "h3": "3", "h4": "4", "i1": "", "i2": ""}).Error; err != nil {
		t.Fatal(err)
	}
	legacy, err := (&AwgService{}).GetServer()
	if err != nil {
		t.Fatalf("GetServer legacy: %v", err)
	}
	if legacy.Jc != 0 || legacy.H1 != "1" || legacy.I1 != "" {
		t.Errorf("a 1.x server was rewritten on read: Jc=%d H1=%q I1=%q", legacy.Jc, legacy.H1, legacy.I1)
	}

	// Native WireGuard has no obfuscation to seed.
	wg, err := (&WgService{}).GetServer()
	if err != nil {
		t.Fatalf("wg GetServer: %v", err)
	}
	if wg.Jc != 0 || wg.H1 != "" || wg.HeaderProtectionKey != "" {
		t.Errorf("obfuscation leaked into a WireGuard server: %+v", wg)
	}
}
