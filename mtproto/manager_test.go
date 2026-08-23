package mtproto

import (
	"strings"
	"testing"

	"github.com/coinman-dev/3ax-ui/v2/database/model"
)

func oneClient(secret string) []ClientSecret {
	return []ClientSecret{{Id: "u1", Secret: secret, Email: "alice"}}
}

func TestParseMetricLine(t *testing.T) {
	name, labels, val, err := parseMetricLine(`mtg_traffic{direction="to_client"} 12345`)
	if err != nil {
		t.Fatal(err)
	}
	if name != "mtg_traffic" {
		t.Fatalf("name=%q", name)
	}
	if labels["direction"] != "to_client" {
		t.Fatalf("labels=%v", labels)
	}
	if val != 12345 {
		t.Fatalf("val=%v", val)
	}

	name2, _, val2, err2 := parseMetricLine(`mtg_concurrency 7`)
	if err2 != nil {
		t.Fatal(err2)
	}
	if name2 != "mtg_concurrency" || val2 != 7 {
		t.Fatalf("got %q %v", name2, val2)
	}
}

func TestInstanceFromInbound(t *testing.T) {
	ib := &model.Inbound{
		Id:       3,
		Tag:      "inbound-3",
		Listen:   "0.0.0.0",
		Port:     8443,
		Protocol: model.MTProto,
		Settings: `{"fakeTlsDomain":"example.com",` +
			`"debug":true,"proxyProtocolListener":true,"preferIp":"prefer-ipv4",` +
			`"domainFronting":{"ip":"127.0.0.1","port":9443,"proxyProtocol":true},` +
			`"routeThroughXray":true,"routeXrayPort":50000}`,
	}
	// Clients come from the dedicated table (pre-healed secrets), passed in.
	clients := []model.MtprotoClient{
		{Uuid: "u1", Email: "alice", Secret: "eeAAAA", Enable: true},
		{Uuid: "u2", Email: "bob", Secret: "eeBBBB", Enable: false},
	}
	inst, ok := InstanceFromInbound(ib, clients)
	if !ok {
		t.Fatal("expected a usable instance")
	}
	// Only the enabled client is active; its uuid/secret are carried through.
	if len(inst.Clients) != 1 || inst.Clients[0].Email != "alice" {
		t.Fatalf("expected only the enabled client, got %+v", inst.Clients)
	}
	if inst.Clients[0].Id != "u1" || inst.Clients[0].Secret != "eeAAAA" {
		t.Fatalf("client identity not carried: %+v", inst.Clients[0])
	}
	if inst.Port != 8443 || inst.Id != 3 {
		t.Fatalf("bad instance %+v", inst)
	}
	if !inst.Debug || !inst.ProxyProtocolListener || inst.PreferIP != "prefer-ipv4" {
		t.Fatalf("scalar options not parsed: %+v", inst)
	}
	// The legacy `ip` key still feeds FrontingHost, so an inbound configured
	// before mtg deprecated it keeps its fronting target after an upgrade.
	if inst.FrontingHost != "127.0.0.1" || inst.FrontingPort != 9443 || !inst.FrontingProxyProtocol {
		t.Fatalf("domain-fronting not parsed: %+v", inst)
	}
	if !inst.RouteThroughXray || inst.XrayRoutePort != 50000 {
		t.Fatalf("xray routing not parsed: %+v", inst)
	}

	if _, ok := InstanceFromInbound(&model.Inbound{Protocol: model.VLESS}, nil); ok {
		t.Fatal("non-mtproto inbound should not produce an instance")
	}

	// No enabled client → no instance.
	noActive := &model.Inbound{Id: 4, Protocol: model.MTProto, Port: 1, Settings: `{"fakeTlsDomain":"x"}`}
	noActiveClients := []model.MtprotoClient{{Uuid: "u", Email: "e", Secret: "ee", Enable: false}}
	if _, ok := InstanceFromInbound(noActive, noActiveClients); ok {
		t.Fatal("an inbound with no enabled clients must not produce an instance")
	}
}

func TestRenderConfigSingle(t *testing.T) {
	// Single-secret (mtg): emits secret=, [stats.prometheus], no [secrets].
	bare := renderConfig(Instance{Clients: oneClient("ee00"), Listen: "0.0.0.0", Port: 8443}, 5000)
	for _, unwanted := range []string{"debug", "proxy-protocol-listener", "prefer-ip", "[domain-fronting]", "[secrets]", "api-bind-to"} {
		if strings.Contains(bare, unwanted) {
			t.Fatalf("bare single config should not contain %q:\n%s", unwanted, bare)
		}
	}
	if !strings.Contains(bare, `secret = "ee00"`) || !strings.Contains(bare, `bind-to = "0.0.0.0:8443"`) {
		t.Fatalf("missing secret/bind-to:\n%s", bare)
	}
	if !strings.Contains(bare, "[stats.prometheus]") || !strings.Contains(bare, "127.0.0.1:5000") {
		t.Fatalf("prometheus block must be present in single mode:\n%s", bare)
	}
}

func TestRenderConfigMulti(t *testing.T) {
	// Multi-user (mtg-multi): emits api-bind-to + [secrets], no secret=, no prometheus.
	inst := Instance{
		MultiUser: true, Listen: "0.0.0.0", Port: 443,
		Clients: []ClientSecret{
			{Id: "u1", Secret: "eeAAA", Email: "alice"},
			{Id: "u2", Secret: "eeBBB", Email: "bob"},
		},
	}
	cfg := renderConfig(inst, 6000)
	for _, want := range []string{
		`api-bind-to = "127.0.0.1:6000"`,
		"[secrets]",
		`"u1" = "eeAAA"`,
		`"u2" = "eeBBB"`,
		`bind-to = "0.0.0.0:443"`,
	} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("multi config missing %q:\n%s", want, cfg)
		}
	}
	for _, unwanted := range []string{"secret = ", "[stats.prometheus]"} {
		if strings.Contains(cfg, unwanted) {
			t.Fatalf("multi config should not contain %q:\n%s", unwanted, cfg)
		}
	}
	// TOML: api-bind-to (top-level) must precede the [secrets] section.
	if strings.Index(cfg, "api-bind-to") > strings.Index(cfg, "[secrets]") {
		t.Fatalf("api-bind-to must precede [secrets]:\n%s", cfg)
	}
}

func TestRenderConfigXrayEgress(t *testing.T) {
	routed := renderConfig(Instance{
		Clients: oneClient("ee22"), Listen: "0.0.0.0", Port: 443,
		RouteThroughXray: true, XrayRoutePort: 50000,
	}, 7000)
	if !strings.Contains(routed, "[network]") ||
		!strings.Contains(routed, `proxies = ["socks5://127.0.0.1:50000"]`) {
		t.Fatalf("routed config must emit the SOCKS upstream:\n%s", routed)
	}
	if strings.Index(routed, "[network]") > strings.Index(routed, "[stats.prometheus]") {
		t.Fatalf("[network] must precede [stats.prometheus]:\n%s", routed)
	}
	for _, inst := range []Instance{
		{Clients: oneClient("ee"), Listen: "0.0.0.0", Port: 443},
		{Clients: oneClient("ee"), Listen: "0.0.0.0", Port: 443, RouteThroughXray: true},
	} {
		if got := renderConfig(inst, 7000); strings.Contains(got, "[network]") {
			t.Fatalf("unrouted config must omit [network]:\n%s", got)
		}
	}
}

func TestFingerprintReactsToOptions(t *testing.T) {
	base := Instance{Clients: oneClient("ee"), Listen: "0.0.0.0", Port: 443}
	for name, mutate := range map[string]func(*Instance){
		"multiUser":     func(i *Instance) { i.MultiUser = true },
		"debug":         func(i *Instance) { i.Debug = true },
		"listener":      func(i *Instance) { i.ProxyProtocolListener = true },
		"preferIp":      func(i *Instance) { i.PreferIP = "only-ipv4" },
		"frontingHost":  func(i *Instance) { i.FrontingHost = "127.0.0.1" },
		"frontingPort":  func(i *Instance) { i.FrontingPort = 9443 },
		"frontingProxy": func(i *Instance) { i.FrontingProxyProtocol = true },
		"concurrency":   func(i *Instance) { i.Concurrency = 4096 },
		"publicIPv4":    func(i *Instance) { i.PublicIPv4 = "1.2.3.4" },
		"publicIPv6":    func(i *Instance) { i.PublicIPv6 = "2001:db8::1" },
		"timeSkew":      func(i *Instance) { i.TolerateTimeSkewness = "10s" },
		"dns":           func(i *Instance) { i.DNS = "https://1.1.1.1" },
		"doppelUrls":    func(i *Instance) { i.DoppelgangerURLs = []string{"https://example.com/a.js"} },
		"doppelRepeats": func(i *Instance) { i.DoppelgangerRepeats = 10 },
		"doppelRaid":    func(i *Instance) { i.DoppelgangerRaidEach = "6h" },
		"doppelDRS":     func(i *Instance) { i.DoppelgangerDRS = true },
		"antiReplayOff": func(i *Instance) { i.AntiReplayDisabled = true },
		"blocklistOff":  func(i *Instance) { i.BlocklistDisabled = true },
		"blocklistUrls": func(i *Instance) { i.BlocklistURLs = []string{"https://example.com/list.netset"} },
		"routeXray":     func(i *Instance) { i.RouteThroughXray = true },
		"routeXrayPort": func(i *Instance) { i.XrayRoutePort = 50000 },
		"addClient": func(i *Instance) {
			i.MultiUser = true
			i.Clients = append(i.Clients, ClientSecret{Id: "u2", Secret: "ee2", Email: "bob"})
		},
		"changeSecret": func(i *Instance) { i.Clients = []ClientSecret{{Id: "u1", Secret: "eeXX", Email: "alice"}} },
	} {
		changed := base
		changed.Clients = append([]ClientSecret(nil), base.Clients...)
		mutate(&changed)
		if base.fingerprint() == changed.fingerprint() {
			t.Fatalf("fingerprint must change when %s changes", name)
		}
	}
}
