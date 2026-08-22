package service

import (
	"path/filepath"
	"testing"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
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
