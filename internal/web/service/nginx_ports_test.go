package service

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/nginx"
)

// TestLoopbackPortsDoNotCollide: the dual relay and the site's loopback port, both unbound until
// reload, got the same "free" port; `nginx -t` binds nothing, so only the nginx start failed.
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
