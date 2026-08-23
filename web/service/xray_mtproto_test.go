package service

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/util/json_util"
	"github.com/coinman-dev/3ax-ui/v2/xray"
)

func baseEgressConfig() *xray.Config {
	return &xray.Config{
		RouterConfig: json_util.RawMessage(`{"rules":[{"type":"field","outboundTag":"direct","ip":["geoip:private"]}]}`),
		InboundConfigs: []xray.InboundConfig{
			{Port: 443, Protocol: "vless", Tag: "inbound-443"},
		},
		OutboundConfigs: json_util.RawMessage(`[{"protocol":"freedom","tag":"direct"},{"protocol":"vless","tag":"proxy"}]`),
	}
}

func routedMtprotoInbound(routed bool, port int, outboundTag string) *model.Inbound {
	s := map[string]any{
		"secret":           "eedeadbeef",
		"fakeTlsDomain":    "www.cloudflare.com",
		"routeThroughXray": routed,
		"routeXrayPort":    port,
		"outboundTag":      outboundTag,
	}
	bs, _ := json.Marshal(s)
	return &model.Inbound{
		Id:       7,
		Protocol: model.MTProto,
		Enable:   true,
		Port:     8443,
		Tag:      "inbound-8443",
		Settings: string(bs),
	}
}

func findSocksBridge(cfg *xray.Config, tag string) *xray.InboundConfig {
	for i := range cfg.InboundConfigs {
		if cfg.InboundConfigs[i].Tag == tag && cfg.InboundConfigs[i].Protocol == "socks" {
			return &cfg.InboundConfigs[i]
		}
	}
	return nil
}

func TestInjectMtprotoEgress_RoutedWithOutbound(t *testing.T) {
	cfg := baseEgressConfig()
	injectMtprotoEgress(cfg, routedMtprotoInbound(true, 51000, "proxy"))

	// SOCKS bridge inbound appended on the egress port, tagged with the inbound tag.
	br := findSocksBridge(cfg, "inbound-8443")
	if br == nil {
		t.Fatal("expected a socks bridge inbound tagged inbound-8443")
	}
	if br.Port != 51000 {
		t.Fatalf("bridge port = %d, want 51000", br.Port)
	}
	if string(br.Listen) != `"127.0.0.1"` {
		t.Fatalf("bridge listen = %s, want loopback", string(br.Listen))
	}

	// A routing rule sending the bridge tag to the outbound was prepended.
	var routing map[string]any
	if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
		t.Fatalf("routing unparsable: %v", err)
	}
	rules, _ := routing["rules"].([]any)
	if len(rules) == 0 {
		t.Fatal("no routing rules")
	}
	first, _ := rules[0].(map[string]any)
	tags, _ := first["inboundTag"].([]any)
	if len(tags) != 1 || tags[0] != "inbound-8443" {
		t.Fatalf("first rule inboundTag = %v, want [inbound-8443]", first["inboundTag"])
	}
	if first["outboundTag"] != "proxy" {
		t.Fatalf("first rule outboundTag = %v, want proxy", first["outboundTag"])
	}
}

func TestInjectMtprotoEgress_NotRouted(t *testing.T) {
	cfg := baseEgressConfig()
	before := len(cfg.InboundConfigs)
	beforeRouting := string(cfg.RouterConfig)
	injectMtprotoEgress(cfg, routedMtprotoInbound(false, 51000, "proxy"))
	if len(cfg.InboundConfigs) != before {
		t.Fatalf("inbounds changed for a non-routed inbound: %d -> %d", before, len(cfg.InboundConfigs))
	}
	if string(cfg.RouterConfig) != beforeRouting {
		t.Fatal("routing changed for a non-routed inbound")
	}
}

func TestInjectMtprotoEgress_RoutedNoOutbound(t *testing.T) {
	cfg := baseEgressConfig()
	beforeRouting := string(cfg.RouterConfig)
	injectMtprotoEgress(cfg, routedMtprotoInbound(true, 51000, ""))
	// Bridge is added even without an outbound...
	if findSocksBridge(cfg, "inbound-8443") == nil {
		t.Fatal("expected a socks bridge even without an outbound tag")
	}
	// ...but no routing rule is added (uses default outbound).
	if string(cfg.RouterConfig) != beforeRouting {
		t.Fatal("routing should be untouched when no outbound tag is set")
	}
}

func TestInjectMtprotoEgress_NoPort(t *testing.T) {
	cfg := baseEgressConfig()
	before := len(cfg.InboundConfigs)
	injectMtprotoEgress(cfg, routedMtprotoInbound(true, 0, "proxy"))
	if len(cfg.InboundConfigs) != before {
		t.Fatal("routed inbound with no egress port must not add a bridge")
	}
}

// TestGetInboundTagsOffersRoutedMtproto: a routed mtproto inbound answers to
// its own tag in the generated config (the loopback SOCKS bridge), so the
// routing-rule dropdown must offer it — otherwise the only way to steer
// Telegram is the inbound form's single "outbound tag" field. An unrouted one
// must stay out: nothing in the config carries that tag.
func TestGetInboundTagsOffersRoutedMtproto(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	db := database.GetDB()

	routed := routedMtprotoInbound(true, 62001, "nl-vpn")
	routed.Id = 0
	routed.Tag = "inbound-10777"
	plain := routedMtprotoInbound(false, 0, "")
	plain.Id = 0
	plain.Port = 10888
	plain.Tag = "inbound-10888"
	for _, in := range []*model.Inbound{routed, plain} {
		if err := db.Create(in).Error; err != nil {
			t.Fatalf("seed inbound %s: %v", in.Tag, err)
		}
	}

	tags, err := (&InboundService{}).GetInboundTags()
	if err != nil {
		t.Fatalf("GetInboundTags: %v", err)
	}
	if !strings.Contains(tags, routed.Tag) {
		t.Errorf("routing tag list %s does not offer the routed mtproto tag %q", tags, routed.Tag)
	}
	if strings.Contains(tags, plain.Tag) {
		t.Errorf("routing tag list %s offers %q, but that inbound never enters the Xray config", tags, plain.Tag)
	}
}
