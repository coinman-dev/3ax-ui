package mtproto

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

const axDefenseSettings = `{"fakeTlsDomain":"www.cloudflare.com",
 "concurrency":4096,"tolerateTimeSkewness":"5s","dns":"https://1.1.1.1/dns-query",
 "doppelganger":{"urls":["https://www.cloudflare.com/"],"repeatsPerRaid":10,"raidEach":"6h","drs":true},
 "antiReplay":{"enabled":false},
 "blocklist":{"enabled":true,"urls":["https://iplists.firehol.org/files/firehol_level1.netset"]},
 "routeThroughXray":true,"routeXrayPort":50001,
 "clients":[{"email":"alice","secret":"ee0123456789abcdef0123456789abcdef7777772e636c6f7564666c6172652e636f6d","enable":true}]}`

func axDefenseInbound(settings string) *model.Inbound {
	return &model.Inbound{Id: 9, Tag: "inbound-8443", Port: 8443, Protocol: model.MTProto, Settings: settings}
}

// The anti-blocking options 3AX-UI 1.x exposed reach mtg-multi's config, in
// tables TOML accepts once each: [network] carries both dns and the Xray proxy.
func TestRenderConfigAxDefense(t *testing.T) {
	inst, ok := InstanceFromInbound(axDefenseInbound(axDefenseSettings))
	if !ok {
		t.Fatal("instance not built")
	}
	cfg := renderConfig(inst, 40000, "")
	for _, want := range []string{
		"concurrency = 4096\n",
		"tolerate-time-skewness = \"5s\"\n",
		"\n[network]\ndns = \"https://1.1.1.1/dns-query\"\nproxies = [\"socks5://127.0.0.1:50001\"]\n",
		"\n[defense.doppelganger]\nurls = [\"https://www.cloudflare.com/\"]\nrepeats-per-raid = 10\nraid-each = \"6h\"\ndrs = true\n",
		"\n[defense.anti-replay]\nenabled = false\n",
		"\n[defense.blocklist]\nurls = [\"https://iplists.firehol.org/files/firehol_level1.netset\"]\n",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
	if n := strings.Count(cfg, "[network]"); n != 1 {
		t.Errorf("[network] appears %d times; TOML rejects a repeated table", n)
	}
}

func TestRenderConfigAxDefenseDefaultsStayOut(t *testing.T) {
	inst, ok := InstanceFromInbound(axDefenseInbound(`{"clients":[{"email":"alice","secret":"ee0123456789abcdef0123456789abcdef7777772e636c6f7564666c6172652e636f6d","enable":true}]}`))
	if !ok {
		t.Fatal("instance not built")
	}
	cfg := renderConfig(inst, 40000, "")
	for _, unwanted := range []string{"concurrency", "tolerate-time", "[network]", "[defense.doppelganger]", "[defense.anti-replay]", "[defense.blocklist]"} {
		if strings.Contains(cfg, unwanted) {
			t.Errorf("unset option %q was written:\n%s", unwanted, cfg)
		}
	}
}

// mtg reads these options only at start, so changing one must restart it.
func TestAxDefenseChangeRestartsMtg(t *testing.T) {
	before, _ := InstanceFromInbound(axDefenseInbound(axDefenseSettings))
	after, _ := InstanceFromInbound(axDefenseInbound(strings.Replace(axDefenseSettings, `"raidEach":"6h"`, `"raidEach":"12h"`, 1)))
	if before.structuralFingerprint() == after.structuralFingerprint() {
		t.Error("a doppelganger change left the structural fingerprint alone, so mtg would keep the old one")
	}
}
