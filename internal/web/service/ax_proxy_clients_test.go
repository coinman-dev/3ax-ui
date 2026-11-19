package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func proxyInboundSettings(t *testing.T, protocol model.Protocol, tag string) map[string]any {
	t.Helper()
	cfg, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	for i := range cfg.InboundConfigs {
		if cfg.InboundConfigs[i].Tag != tag {
			continue
		}
		if cfg.InboundConfigs[i].Protocol != string(protocol) {
			t.Fatalf("inbound %s emitted as %s", tag, cfg.InboundConfigs[i].Protocol)
		}
		var settings map[string]any
		if err := json.Unmarshal(cfg.InboundConfigs[i].Settings, &settings); err != nil {
			t.Fatalf("emitted settings: %v", err)
		}
		return settings
	}
	t.Fatalf("inbound %s not in the generated config", tag)
	return nil
}

func accountUsers(settings map[string]any) []string {
	var users []string
	accounts, _ := settings["accounts"].([]any)
	for _, a := range accounts {
		acc, _ := a.(map[string]any)
		users = append(users, acc["user"].(string)+":"+acc["pass"].(string))
	}
	return users
}

// The client's email is the SOCKS username, which Xray reports as the user in
// its stats, so traffic, quotas and expiry run through the usual client path.
func TestMixedClientsBecomeSocksAccounts(t *testing.T) {
	setupBulkDB(t)
	ib := mkInbound(t, 53000, model.Mixed, `{"auth":"noauth","accounts":[{"user":"static","pass":"kept"}],"udp":true,"ip":"127.0.0.1"}`)
	svc := &ClientService{}
	inboundSvc := &InboundService{}
	if _, err := svc.CreateOne(inboundSvc, ib.Id, model.Client{Email: "alice", Password: "alicepass", Enable: true}); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if _, err := svc.CreateOne(inboundSvc, ib.Id, model.Client{Email: "bob", Password: "bobpass", Enable: false}); err != nil {
		t.Fatalf("create bob: %v", err)
	}
	settings := proxyInboundSettings(t, model.Mixed, ib.Tag)
	if settings["auth"] != "password" {
		t.Errorf("auth = %v, want password once the inbound has clients", settings["auth"])
	}
	if got := accountUsers(settings); strings.Join(got, ",") != "static:kept,alice:alicepass" {
		t.Errorf("accounts = %v, want the static one plus enabled alice only", got)
	}
	if _, leaked := settings["clients"]; leaked {
		t.Errorf("panel-only clients key reached Xray: %v", settings["clients"])
	}
}

// An HTTP inbound with no accounts lets anyone in, so disabling every client
// must not open it.
func TestHTTPWithEveryClientDisabledStaysClosed(t *testing.T) {
	setupBulkDB(t)
	ib := mkInbound(t, 53001, model.HTTP, `{"accounts":[],"allowTransparent":false}`)
	if _, err := (&ClientService{}).CreateOne(&InboundService{}, ib.Id, model.Client{Email: "carol", Password: "carolpass", Enable: false}); err != nil {
		t.Fatalf("create carol: %v", err)
	}
	got := accountUsers(proxyInboundSettings(t, model.HTTP, ib.Tag))
	if len(got) != 1 || strings.HasPrefix(got[0], "carol:") {
		t.Errorf("accounts = %v, want one unusable placeholder", got)
	}
}

func TestProxyInboundWithoutClientsIsLeftAlone(t *testing.T) {
	setupBulkDB(t)
	ib := mkInbound(t, 53002, model.Mixed, `{"auth":"noauth","accounts":[],"udp":false}`)
	settings := proxyInboundSettings(t, model.Mixed, ib.Tag)
	if settings["auth"] != "noauth" {
		t.Errorf("a mixed inbound with no clients must keep its own auth, got %v", settings["auth"])
	}
}

func TestProxyClientDefaultsAndUsernameRules(t *testing.T) {
	setupBulkDB(t)
	ib := mkInbound(t, 53003, model.HTTP, `{"accounts":[]}`)
	svc := &ClientService{}
	inboundSvc := &InboundService{}
	if _, err := svc.CreateOne(inboundSvc, ib.Id, model.Client{Email: "dave", Enable: true}); err != nil {
		t.Fatalf("create dave: %v", err)
	}
	if rec := lookupClientRecord(t, "dave"); len(rec.Password) != 16 {
		t.Errorf("generated password %q, want 16 characters", rec.Password)
	}
	_, err := svc.CreateOne(inboundSvc, ib.Id, model.Client{Email: "eve:admin", Enable: true})
	if err == nil || !strings.Contains(err.Error(), "':'") {
		t.Errorf("a username with ':' breaks HTTP Basic auth; got %v", err)
	}
}
