package job

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgkern"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// Kernel-mode traffic never passes Xray, so nothing else moves the inbound's own
// counters: without the roll-up the inbounds page showed a frozen total.
func TestKernelTrafficRollsUpIntoItsInboundTotal(t *testing.T) {
	cases := []struct {
		name             string
		routeThroughXray bool
		wantUp, wantDown int64
	}{
		{name: "direct egress", wantUp: 1000 + 130, wantDown: 2000 + 260},
		// Xray meters a routed inbound through its TPROXY listener, under the same tag.
		{name: "routed through Xray", routeThroughXray: true, wantUp: 1000, wantDown: 2000},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupIntegrationDB(t)
			db := database.GetDB()
			inbound := &model.Inbound{
				UserId: 1, Tag: "kernel-total", Enable: true, Port: 45230 + i, Up: 1000, Down: 2000,
				Protocol: model.AmneziaWG, Settings: `{"clients":[]}`,
			}
			if err := db.Create(inbound).Error; err != nil {
				t.Fatalf("create inbound: %v", err)
			}
			for _, email := range []string{"total-a@example.test", "total-b@example.test"} {
				if err := db.Create(&xray.ClientTraffic{InboundId: inbound.Id, Email: email, Enable: true}).Error; err != nil {
					t.Fatalf("create client traffic: %v", err)
				}
			}
			desired := []amneziawgkern.Desired{{
				RouteThroughXray: tc.routeThroughXray,
				Instance: amneziawg.Instance{Id: inbound.Id, Tag: inbound.Tag, Peers: []amneziawg.Peer{
					{Email: "total-a@example.test", PublicKey: "peer-a"},
					{Email: "total-b@example.test", PublicKey: "peer-b"},
				}},
			}}
			manager := &kernelTrafficTestManager{id: inbound.Id, state: amneziawgkern.TrafficInterfaceState{Name: "xui-awg-total", Generation: 1}}
			dump := func(string) ([]amneziawgkern.PeerStat, error) {
				return []amneziawgkern.PeerStat{
					{PublicKey: "peer-a", RxBytes: 100, TxBytes: 200},
					{PublicKey: "peer-b", RxBytes: 30, TxBytes: 60},
				}, nil
			}
			collectKernelTrafficWith(&service.InboundService{}, desired, manager, amneziawgkern.NewCollector(), dump)

			var got model.Inbound
			if err := db.First(&got, inbound.Id).Error; err != nil {
				t.Fatalf("reload inbound: %v", err)
			}
			if got.Up != tc.wantUp || got.Down != tc.wantDown {
				t.Fatalf("inbound total = up %d down %d, want %d/%d", got.Up, got.Down, tc.wantUp, tc.wantDown)
			}
			var peer xray.ClientTraffic
			if err := db.Where("email = ?", "total-a@example.test").First(&peer).Error; err != nil {
				t.Fatalf("reload client traffic: %v", err)
			}
			if peer.Up != 100 || peer.Down != 200 {
				t.Fatalf("client traffic = up %d down %d, want 100/200 either way", peer.Up, peer.Down)
			}
		})
	}
}
