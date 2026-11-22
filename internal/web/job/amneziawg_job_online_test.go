package job

import (
	"slices"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgkern"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// The inbounds page lights a client from the per-panel online and active-inbound sets, which only
// Xray filled: a kernel-mode AmneziaWG client with a fresh handshake showed offline there.
func TestKernelAmneziaWGHandshakeMarksClientOnlineOnItsInbound(t *testing.T) {
	setupIntegrationDB(t)
	guid, err := (&service.SettingService{}).GetPanelGuid()
	if err != nil || guid == "" {
		t.Fatalf("panel guid = %q, err %v", guid, err)
	}

	const iface = "awg-online-test"
	collector := amneziawgkern.GetCollector()
	t.Cleanup(func() { collector.Forget(iface) })

	desired := []amneziawgkern.Desired{{Instance: amneziawg.Instance{
		Id: 7, Tag: "inbound-55200",
		Peers: []amneziawg.Peer{
			{Email: "laptop@example.test", PublicKey: "fresh-peer"},
			{Email: "phone@example.test", PublicKey: "stale-peer"},
		},
	}}}
	now := time.Now()
	dump := func(string) ([]amneziawgkern.PeerStat, error) {
		return []amneziawgkern.PeerStat{
			{PublicKey: "fresh-peer", LatestHandshake: now.Add(-time.Minute).Unix()},
			{PublicKey: "stale-peer", LatestHandshake: now.Add(-10 * time.Minute).Unix()},
		}, nil
	}
	manager := &kernelTrafficTestManager{id: 7, state: amneziawgkern.TrafficInterfaceState{Name: iface, Generation: 1}}
	inbounds := &service.InboundService{}
	collectKernelTrafficWith(inbounds, desired, manager, collector, dump)

	if got := inbounds.GetOnlineClientsByGuid()[guid]; !slices.Equal(got, []string{"laptop@example.test"}) {
		t.Fatalf("online on this panel = %q, want only the peer with a fresh handshake", got)
	}
	if got := inbounds.GetActiveInboundsByGuid()[guid]; !slices.Equal(got, []string{"inbound-55200"}) {
		t.Fatalf("active inbounds on this panel = %q, want the AmneziaWG inbound", got)
	}
}
