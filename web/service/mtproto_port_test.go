package service

import (
	"path/filepath"
	"testing"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
)

// TestMtprotoXrayPortCollisions covers the loopback egress port the panel picks
// for a routed mtproto inbound: it must not land on a port another inbound uses,
// a new inbound must not be allowed to claim an egress port, and editing an
// inbound must not report its own port as taken.
func TestMtprotoXrayPortCollisions(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	db := database.GetDB()
	s := &InboundService{}

	vless := &model.Inbound{
		UserId: 1, Remark: "vless", Enable: true, Port: 41000,
		Protocol: "vless", Tag: "inbound-41000", Settings: `{"clients":[]}`,
	}
	mt := &model.Inbound{
		UserId: 1, Remark: "mtproto", Enable: true, Port: 41001,
		Protocol: model.MTProto, Tag: "inbound-41001",
		Settings: `{"fakeTlsDomain":"www.cloudflare.com","routeThroughXray":true,"routeXrayPort":41002}`,
	}
	for _, ib := range []*model.Inbound{vless, mt} {
		if err := db.Create(ib).Error; err != nil {
			t.Fatalf("create %s: %v", ib.Remark, err)
		}
	}

	reserved := s.mtprotoReservedPorts(0)
	for _, port := range []int{41000, 41001, 41002} {
		if _, ok := reserved[port]; !ok {
			t.Errorf("port %d missing from the reserved set", port)
		}
	}

	// Editing the mtproto inbound must not see its own ports as foreign.
	own := s.mtprotoReservedPorts(mt.Id)
	for _, port := range []int{41001, 41002} {
		if _, ok := own[port]; ok {
			t.Errorf("port %d of the edited inbound must be excluded", port)
		}
	}

	// A new inbound cannot take the egress port of the routed mtproto inbound.
	exists, err := s.checkPortExist("", 41002, 0)
	if err != nil {
		t.Fatalf("checkPortExist: %v", err)
	}
	if !exists {
		t.Error("port 41002 is the mtproto egress port, it must be reported as taken")
	}

	// A genuinely free port stays free.
	exists, err = s.checkPortExist("", 41999, 0)
	if err != nil {
		t.Fatalf("checkPortExist: %v", err)
	}
	if exists {
		t.Error("unused port 41999 reported as taken")
	}

	// Re-saving an inbound on its own port is not a collision.
	exists, err = s.checkPortExist("", 41001, mt.Id)
	if err != nil {
		t.Fatalf("checkPortExist: %v", err)
	}
	if exists {
		t.Error("an inbound keeping its own port must not be a collision")
	}

	// A stored egress port that another inbound has since taken is replaced.
	clash := &model.Inbound{
		UserId: 1, Remark: "clash", Enable: true, Port: 41500,
		Protocol: "vless", Tag: "inbound-41500", Settings: `{"clients":[]}`,
	}
	if err := db.Create(clash).Error; err != nil {
		t.Fatal(err)
	}
	victim := &model.Inbound{
		Id: mt.Id, Protocol: model.MTProto,
		Settings: `{"routeThroughXray":true,"routeXrayPort":41500}`,
	}
	if err := s.normalizeMtprotoXrayPort(victim, victim.Settings); err != nil {
		t.Fatalf("normalizeMtprotoXrayPort: %v", err)
	}
	got := mtprotoParseXrayPort(victim.Settings)
	if got == 41500 {
		t.Error("egress port 41500 collides with an existing inbound but was kept")
	}
	if got <= 0 || got > 65535 {
		t.Errorf("no usable egress port allocated, got %d", got)
	}
}
