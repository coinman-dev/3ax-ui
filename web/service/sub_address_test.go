package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/nginx"
)

// setNginxFront stores a front-end configuration without applying it, which is
// what Apply leaves behind once it has succeeded.
func setNginxFront(t *testing.T, mode string, subsBehind443 bool, domain string) {
	t.Helper()
	set := SettingService{}
	pairs := map[string]string{
		"nginxMode":          mode,
		"nginxDomain":        domain,
		"nginxSubsBehind443": map[bool]string{true: "true", false: "false"}[subsBehind443],
	}
	for key, value := range pairs {
		if err := set.setString(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
}

func TestPublicSubBase(t *testing.T) {
	cases := []struct {
		name          string
		mode          string
		subsBehind443 bool
		domain        string
		wantOk        bool
		wantHost      string
	}{
		{name: "front-end off", mode: string(nginx.ModeOff), subsBehind443: true, domain: "vpn.example.com"},
		{name: "subscriptions left on their own port", mode: string(nginx.ModeShared), domain: "vpn.example.com"},
		{name: "no domain to publish under", mode: string(nginx.ModeShared), subsBehind443: true},
		{
			name: "published", mode: string(nginx.ModeShared), subsBehind443: true,
			domain: "vpn.example.com", wantOk: true, wantHost: "vpn.example.com",
		},
		{
			name: "published, only 443", mode: string(nginx.ModeOnly443), subsBehind443: true,
			domain: "vpn.example.com", wantOk: true, wantHost: "vpn.example.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
				t.Fatalf("InitDB: %v", err)
			}
			setNginxFront(t, tc.mode, tc.subsBehind443, tc.domain)

			scheme, host, ok := PublicSubBase()
			if ok != tc.wantOk {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOk)
			}
			if !ok {
				return
			}
			if scheme != "https" {
				t.Errorf("scheme = %q, want https", scheme)
			}
			if host != tc.wantHost {
				t.Errorf("host = %q, want %q", host, tc.wantHost)
			}
		})
	}
}

// TestSubURIFollowsTheFrontEnd is the reason PublicSubBase exists: the address
// the panel shows and hands to clients has to be the one nginx answers on, not
// the subscription server's own port. That port is an unadvertised second way
// in at best, and closed at worst.
func TestSubURIFollowsTheFrontEnd(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	// GetDefaultSettings asks Xray where its access log is, which means reading
	// the generated config; give it one so the whole call does not fail here.
	bin := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", bin)
	if err := os.WriteFile(filepath.Join(bin, "config.json"), []byte(`{"log":{}}`), 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	set := SettingService{}
	for key, value := range map[string]string{
		"subEnable": "true",
		"subDomain": "subs.example.com",
		"subPort":   "2096",
		"subPath":   "/sub/",
	} {
		if err := set.setString(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	// Without the front-end the subscription server's own address stands.
	setNginxFront(t, string(nginx.ModeOff), false, "")
	got, err := set.GetDefaultSettings("panel.example.com:2053")
	if err != nil {
		t.Fatalf("GetDefaultSettings: %v", err)
	}
	if uri := got.(map[string]any)["subURI"].(string); uri != "http://subs.example.com:2096/sub/" {
		t.Errorf("subURI with the front-end off = %q", uri)
	}

	// With it, the site's domain on the public port — and a URL says 443 by
	// saying nothing.
	setNginxFront(t, string(nginx.ModeShared), true, "vpn.example.com")
	got, err = set.GetDefaultSettings("panel.example.com:2053")
	if err != nil {
		t.Fatalf("GetDefaultSettings: %v", err)
	}
	if uri := got.(map[string]any)["subURI"].(string); uri != "https://vpn.example.com/sub/" {
		t.Errorf("subURI with the front-end on = %q, want https://vpn.example.com/sub/", uri)
	}
}

// TestPlanSaysWhenTheSubscriptionAddressMoves: nothing about links may change
// silently. The plan already names every inbound whose port moves; publishing
// the subscriptions moves an address too, and has to be said out loud before
// the operator confirms.
func TestPlanSaysWhenTheSubscriptionAddressMoves(t *testing.T) {
	svc := newNginxTestServer(t)
	set := SettingService{}
	for key, value := range map[string]string{
		"subEnable": "true",
		"subDomain": "subs.example.com",
		"subPort":   "2096",
	} {
		if err := set.setString(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	subsLine := func(plan NginxPlan) *NginxChange {
		for i := range plan.Changes {
			if plan.Changes[i].Kind == "subs" {
				return &plan.Changes[i]
			}
		}
		return nil
	}

	// Leaving the subscriptions where they are says nothing.
	if line := subsLine(svc.Plan(NginxSettings{Mode: string(nginx.ModeShared), Domain: "vpn.example.com"})); line != nil {
		t.Errorf("unexpected subscription line: %+v", line)
	}

	line := subsLine(svc.Plan(NginxSettings{
		Mode: string(nginx.ModeShared), Domain: "vpn.example.com", SubsBehind443: true,
	}))
	if line == nil {
		t.Fatal("publishing the subscriptions was not mentioned in the plan")
	}
	if line.From != "http://subs.example.com:2096" || line.To != "https://vpn.example.com" {
		t.Errorf("subscription line = %q → %q", line.From, line.To)
	}
}
