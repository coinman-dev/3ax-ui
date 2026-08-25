package service

import (
	"path/filepath"
	"testing"

	"github.com/coinman-dev/3ax-ui/v2/nginx"
)

// TestLoopbackPortsDoNotCollide is the bug this guards: the relay for a dual
// inbound and the loopback port the site's TLS terminates on are chosen
// separately, and neither is bound until nginx reloads. Asking the operating
// system «is this free» answered yes to both, so a fresh server with an MTProto
// inbound and a domain — the ordinary case — got one number twice. The config
// passed `nginx -t`, which binds nothing, and then nginx would not start.
func TestLoopbackPortsDoNotCollide(t *testing.T) {
	svc := newNginxTestServer(t)
	seedInbounds(t)
	dir := t.TempDir()
	certFile, _ := writeTestCert(t, dir, "vpn.example.com")
	certDirs = []string{filepath.Dir(filepath.Dir(certFile))}
	t.Cleanup(func() { certDirs = []string{"/root/cert", "/etc/letsencrypt/live", "/root/cert.crt"} })

	set := NginxSettings{Mode: string(nginx.ModeShared), Domain: "vpn.example.com", RealityPort: 8443}
	cfg, err := svc.buildConfig(set)
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if cfg.Site == nil {
		t.Fatal("no site was built, so nothing is being compared")
	}
	for _, r := range cfg.Routes {
		if r.Relay == "" {
			continue
		}
		t.Logf("relay %s vs site %s", r.Relay, cfg.Site.Listen)
		if r.Relay == cfg.Site.Listen {
			t.Errorf("route %q relays on %s, which is also where the site listens", r.Name, r.Relay)
		}
	}
}
