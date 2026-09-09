package service

import (
	"path/filepath"
	"testing"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
)

func TestLinkPort(t *testing.T) {
	cases := []struct {
		name       string
		port       int
		publicPort int
		want       int
	}{
		{"not relocated", 443, 0, 443},
		{"behind nginx", 8443, 443, 443},
		{"negative is ignored", 8443, -1, 8443},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ib := &model.Inbound{Port: tc.port, PublicPort: tc.publicPort}
			if got := ib.LinkPort(); got != tc.want {
				t.Errorf("LinkPort() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestUpdateInboundKeepsPublicPort is the guard behind calling PublicPort
// "panel-managed": the inbound form neither shows nor submits it, so if an
// update copied it off the request it would silently reset to zero and every
// link the inbound ever issued would start pointing at the loopback port.
func TestUpdateInboundKeepsPublicPort(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	db := database.GetDB()
	s := &InboundService{}

	stored := &model.Inbound{
		UserId: 1, Remark: "telegram", Enable: true, Port: 4343, PublicPort: 443,
		Protocol: model.MTProto, Tag: "inbound-4343",
		Settings: `{"fakeTlsDomain":"www.samsung.com"}`,
	}
	if err := db.Create(stored).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	// What the form sends back: everything but PublicPort, which it does not know about.
	edited := &model.Inbound{
		Id: stored.Id, UserId: 1, Remark: "telegram renamed", Enable: true, Port: 4343,
		Protocol: model.MTProto, Settings: `{"fakeTlsDomain":"www.samsung.com"}`,
	}
	if _, _, err := s.UpdateInbound(edited); err != nil {
		t.Fatalf("UpdateInbound: %v", err)
	}

	after, err := s.GetInbound(stored.Id)
	if err != nil {
		t.Fatalf("GetInbound: %v", err)
	}
	if after.PublicPort != 443 {
		t.Errorf("PublicPort = %d after an edit, want it kept at 443", after.PublicPort)
	}
	if after.Remark != "telegram renamed" {
		t.Errorf("the edit itself was lost: remark = %q", after.Remark)
	}
	if got := after.LinkPort(); got != 443 {
		t.Errorf("LinkPort() = %d, want 443", got)
	}
}
