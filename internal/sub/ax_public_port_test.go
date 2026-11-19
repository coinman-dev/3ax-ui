package sub

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// 3AX-UI: the nginx front-end moves an inbound to a loopback port while
// clients keep dialling 443; a link with the loopback port reaches nothing.
func TestSubLinksCarryThePublicPort(t *testing.T) {
	cases := []struct {
		name string
		seed func(t *testing.T)
	}{
		{"relocated inbound", func(t *testing.T) {
			ib := seedSubInbound(t, "pp", "behind443", 18443, 1, `{"network":"tcp","security":"none"}`)
			placeInbound(t, ib.Id, "127.0.0.1", 443)
		}},
		{"fallback child of a relocated master", func(t *testing.T) {
			master := seedRelocatedMaster(t, `{"clients":[],"decryption":"none"}`)
			child := seedSubInbound(t, "pp", "child", 4450, 1, `{"network":"ws","security":"none"}`)
			placeInbound(t, child.Id, "127.0.0.1", 0)
			if err := database.GetDB().Create(&model.InboundFallback{MasterId: master.Id, ChildId: child.Id}).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"socket child of a relocated master", func(t *testing.T) {
			seedRelocatedMaster(t, `{"clients":[],"decryption":"none","fallbacks":[{"dest":"@vless-ws"}]}`)
			child := seedSubInbound(t, "pp", "child", 4451, 1, `{"network":"ws","security":"none"}`)
			placeInbound(t, child.Id, "@vless-ws", 0)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seedSubDB(t)
			tc.seed(t)
			links, _, _, _, err := NewSubService("").GetSubs("pp", "req.example.com")
			if err != nil {
				t.Fatalf("GetSubs: %v", err)
			}
			if len(links) != 1 || !strings.Contains(links[0], ":443?") || strings.Contains(links[0], ":18443") {
				t.Fatalf("links = %q, want the public port 443", links)
			}
		})
	}
}

func placeInbound(t *testing.T, id int, listen string, publicPort int) {
	t.Helper()
	updates := map[string]any{"listen": listen, "public_port": publicPort}
	if err := database.GetDB().Model(&model.Inbound{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		t.Fatal(err)
	}
}

func seedRelocatedMaster(t *testing.T, settings string) *model.Inbound {
	t.Helper()
	master := &model.Inbound{
		UserId: 1, Tag: "master", Enable: true, Listen: "127.0.0.1", Port: 18443, PublicPort: 443,
		Protocol: model.VLESS, Remark: "master", Settings: settings,
		StreamSettings: `{"network":"tcp","security":"tls","tlsSettings":{"serverName":"master.sni"}}`,
	}
	if err := database.GetDB().Create(master).Error; err != nil {
		t.Fatal(err)
	}
	return master
}
