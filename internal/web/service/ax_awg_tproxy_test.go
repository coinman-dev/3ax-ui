package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgnet"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func kernelAwgInbound(id int, tag string, routeThroughXray bool) *model.Inbound {
	server := amneziawg.ServerSettings{SubnetIP: "10.8.1.0", SubnetCIDR: 24, KernelMode: true, RouteThroughXray: routeThroughXray}
	settings, _ := json.Marshal(amneziawg.InboundSettings{Server: &server, Clients: []model.Client{
		{Email: "a@x", Enable: true, PublicKey: "pub-a", AllowedIPs: []string{"10.8.1.2/32"}},
	}})
	return &model.Inbound{Id: id, Tag: tag, Protocol: model.AmneziaWG, Enable: true, Settings: string(settings)}
}

// A kernel inbound routed through Xray needs a TPROXY listener under its own
// tag, so Routing-page rules on that tag apply exactly as in embedded mode.
func TestInjectAmneziawgKernelTproxy(t *testing.T) {
	cfg := egressTestConfig()
	before := string(cfg.RouterConfig)
	injectAmneziawgKernelTproxy(cfg, []*model.Inbound{
		kernelAwgInbound(7, "awg-7", true),
		kernelAwgInbound(8, "awg-8", false),
		amneziawgInbound(9, "awg-9", []model.Client{{Email: "b@x", Enable: true, PublicKey: "pub-b", AllowedIPs: []string{"10.8.1.3/32"}}}),
	})
	if len(cfg.InboundConfigs) != 2 {
		t.Fatalf("want exactly one TPROXY listener (only inbound 7 routes kernel traffic through Xray), got %+v", cfg.InboundConfigs)
	}
	ib := cfg.InboundConfigs[1]
	if ib.Tag != "awg-7" || ib.Port != amneziawgnet.SOCKSPortForInbound(7) || ib.Protocol != "tunnel" || string(ib.Listen) != `"::"` {
		t.Fatalf("listener = %+v", ib)
	}
	if !strings.Contains(string(ib.Settings), `"followRedirect":true`) || !strings.Contains(string(ib.StreamSettings), `"tproxy":"tproxy"`) {
		t.Fatalf("listener must accept TPROXY'd connections: settings %s stream %s", ib.Settings, ib.StreamSettings)
	}
	if !strings.Contains(string(ib.Sniffing), `"routeOnly":true`) {
		t.Fatalf("sniffing must stay routeOnly: %s", ib.Sniffing)
	}
	if string(cfg.RouterConfig) != before {
		t.Fatalf("routing must be left to the admin's own rules, got %s", cfg.RouterConfig)
	}
}

func TestInjectAmneziawgKernelTproxySkipsATakenTag(t *testing.T) {
	cfg := egressTestConfig()
	existing := len(cfg.InboundConfigs)
	cfg.InboundConfigs[0].Tag = "awg-7"
	injectAmneziawgKernelTproxy(cfg, []*model.Inbound{kernelAwgInbound(7, "awg-7", true)})
	if len(cfg.InboundConfigs) != existing {
		t.Fatalf("a tag Xray already has must not be duplicated: %+v", cfg.InboundConfigs)
	}
}

// With routeThroughXray a kernel inbound's relay slot carries the TPROXY
// listener, so another inbound there would steal its traffic.
func TestCheckPortConflict_KernelAmneziaWGRoutedThroughXrayReservesRelay(t *testing.T) {
	setupConflictDB(t)
	seedInboundConflict(t, "awg-kernel-x", "0.0.0.0", 51820, model.AmneziaWG, ``,
		`{"server":{"privateKey":"priv","publicKey":"pub","subnetIp":"10.8.1.0","subnetCidr":24,"kernelMode":true,"routeThroughXray":true},"clients":[]}`)
	var awgInbound model.Inbound
	if err := database.GetDB().Where("tag = ?", "awg-kernel-x").First(&awgInbound).Error; err != nil {
		t.Fatal(err)
	}
	candidate := &model.Inbound{Tag: "vless-on-tproxy", Listen: "0.0.0.0", Port: amneziawgnet.SOCKSPortForInbound(awgInbound.Id), Protocol: model.VLESS}
	got, err := (&InboundService{}).checkPortConflict(candidate, 0)
	if err != nil || got == nil {
		t.Fatalf("an inbound on a routed kernel inbound's TPROXY port must conflict; got=%v err=%v", got, err)
	}
}

// Kernel mode DNATs a forwarded port on the egress NIC, which takes it from
// any inbound listening there just as the embedded forward listener would.
func TestCheckPortConflict_KernelAmneziaWGForwardedPortConflicts(t *testing.T) {
	setupConflictDB(t)
	seedInboundConflict(t, "awg-kernel-fwd", "0.0.0.0", 51820, model.AmneziaWG, ``,
		`{"server":{"privateKey":"priv","publicKey":"pub","subnetIp":"10.8.1.0","subnetCidr":24,"kernelMode":true},`+
			`"clients":[{"email":"gamer","enable":true,"publicKey":"pk","allowedIPs":["10.8.1.2/32"],"forwardedPorts":"27015"}]}`)
	candidate := &model.Inbound{Tag: "trojan-27015", Listen: "0.0.0.0", Port: 27015, Protocol: model.Trojan}
	got, err := (&InboundService{}).checkPortConflict(candidate, 0)
	if err != nil || got == nil || got.ForwardedBy != "gamer" {
		t.Fatalf("an inbound on a kernel peer's forwarded port must conflict and name the peer; got=%+v err=%v", got, err)
	}
}

// 3AX-UI runs forwards and public IPv6 in kernel mode, so client writes that
// upstream refused there must now go through.
func TestKernelAWGClientWritesAcceptForwardsAndIPv6(t *testing.T) {
	setupBulkDB(t)
	inboundSvc := &InboundService{}
	svc := &ClientService{}
	settings, _ := json.Marshal(map[string]any{
		"server":  map[string]any{"subnetIp": "10.8.1.0", "subnetCidr": 24, "kernelMode": true, "ipv6Enabled": true, "ipv6Subnet": "2001:db8::/112"},
		"clients": []model.Client{},
	})
	ib := mkInbound(t, 52010, model.AmneziaWG, string(settings))
	client := model.Client{
		Email: "gamer@awg", Enable: true, PublicKey: "peer-key", ForwardedPorts: model.ForwardedPortsPtr("27015"),
		AllowedIPsByInbound: map[int][]string{ib.Id: {"10.8.1.2/32", "2001:db8::2/128"}},
	}
	if _, err := svc.CreateOne(inboundSvc, ib.Id, client); err != nil {
		t.Fatalf("kernel client with a forward and IPv6 refused: %v", err)
	}
	after, err := inboundSvc.GetInbound(ib.Id)
	if err != nil {
		t.Fatal(err)
	}
	clients := mustAWGClients(t, after)
	if len(clients) != 1 || clients[0].ForwardedPortsSpec() != "27015" || len(clients[0].AllowedIPs) != 2 {
		t.Fatalf("stored client = %+v", clients)
	}
}
