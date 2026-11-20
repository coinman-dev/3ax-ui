package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// The form never sends publicPort; an edit that zeroed it would point every
// link of a relocated inbound at its loopback port.
func TestUpdateInboundKeepsPublicPort(t *testing.T) {
	setupBulkDB(t)
	ib := mkInbound(t, 18443, model.VLESS, `{"clients":[],"decryption":"none"}`)
	if err := database.GetDB().Model(&model.Inbound{}).Where("id = ?", ib.Id).Updates(map[string]any{"listen": "127.0.0.1", "public_port": 443}).Error; err != nil {
		t.Fatal(err)
	}
	edit := *ib
	edit.Listen = "127.0.0.1"
	edit.Remark = "renamed"
	edit.PublicPort = 0
	if _, _, err := (&InboundService{}).UpdateInbound(&edit); err != nil {
		t.Fatalf("UpdateInbound: %v", err)
	}
	var stored model.Inbound
	database.GetDB().First(&stored, ib.Id)
	if stored.Remark != "renamed" || stored.PublicPort != 443 {
		t.Errorf("after edit remark %q publicPort %d, want renamed / 443", stored.Remark, stored.PublicPort)
	}
}

// With nginx publishing the subscriptions on the site's domain, a sub URL built
// from subDomain:subPort points at a port the "only 443" firewall has closed.
func TestBuildSubURIBaseBehindNginx(t *testing.T) {
	setupConflictDB(t)
	s := &SettingService{}
	for k, v := range map[string]string{
		"subDomain": "sub.cdn.com", "subPort": "2096",
		"nginxMode": "shared", "nginxDomain": "site.example", "nginxSubsBehind443": "true",
	} {
		if err := s.saveSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.BuildSubURIBase("panel.example.com"); got != "https://site.example" {
		t.Fatalf("BuildSubURIBase = %q, want https://site.example", got)
	}
	if err := s.saveSetting("nginxSubsBehind443", "false"); err != nil {
		t.Fatal(err)
	}
	if got := s.BuildSubURIBase("panel.example.com"); got != "http://sub.cdn.com:2096" {
		t.Fatalf("subs not behind 443: BuildSubURIBase = %q, want http://sub.cdn.com:2096", got)
	}
}

// install.sh turns the front-end on from the shell; only443 closes ports and
// must go through the panel's confirm/rollback, so the CLI refuses it.
func TestSetNginxModeFromCLI(t *testing.T) {
	setupConflictDB(t)
	s := &SettingService{}
	for _, tc := range []struct {
		mode    string
		wantErr bool
		stored  string
	}{
		{"shared", false, "shared"},
		{"only443", true, "shared"},
		{"bogus", true, "shared"},
		{"off", false, "off"},
	} {
		err := s.SetNginxMode(tc.mode)
		if (err != nil) != tc.wantErr {
			t.Fatalf("SetNginxMode(%q) err = %v, wantErr %v", tc.mode, err, tc.wantErr)
		}
		if got, _ := s.getString("nginxMode"); got != tc.stored {
			t.Fatalf("after SetNginxMode(%q) nginxMode = %q, want %q", tc.mode, got, tc.stored)
		}
	}
}

// A panel certificate issued for the server's IP is no site name: browsers send no SNI for an
// IP, and with no certificate under that name every reconcile aborted (seen after a 1.x upgrade).
func TestDefaultDomainSkipsAnIPCertificate(t *testing.T) {
	s := newNginxTestServer(t)
	certFile, _ := writeTestCert(t, t.TempDir(), "203.0.113.7")
	if err := (&SettingService{}).setString("webCertFile", certFile); err != nil {
		t.Fatal(err)
	}
	if got := s.GetSettings().Domain; got != "" {
		t.Fatalf("domain = %q, want none for an IP certificate", got)
	}
}
