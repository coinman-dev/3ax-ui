package sub

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"testing"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
)

// TestGetSubsKeepsSharedServiceImmutable guards the fix for the shared-state
// bug: SUBController keeps a single SubService for every request, so GetSubs
// must not write the caller's Host onto the shared receiver. When it did, two
// concurrent requests raced and one visitor could be served links built from
// the other visitor's host.
func TestGetSubsKeepsSharedServiceImmutable(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	svc := NewSubService(false, "", "")

	var wg sync.WaitGroup
	for _, host := range []string{"host-a.example.com", "host-b.example.com", "10.0.0.1"} {
		wg.Add(1)
		go func(h string) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				// No inbounds exist, so this returns an error — irrelevant here:
				// what matters is that the shared instance is left untouched.
				_, _, _, _ = svc.GetSubs("some-sub-id", h)
			}
		}(host)
	}
	wg.Wait()

	if svc.address != "" {
		t.Errorf("shared SubService.address = %q, want it untouched", svc.address)
	}
	if svc.datepicker != "" {
		t.Errorf("shared SubService.datepicker = %q, want it untouched", svc.datepicker)
	}
}

// TestBuildURLsFollowsTheFrontEnd: once nginx publishes the subscriptions under
// the site's domain, the link handed to a client has to say that domain on the
// public port. The subscription server's own port is not the way in any more —
// in "only 443" mode the firewall has closed it.
func TestBuildURLsFollowsTheFrontEnd(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	save := func(pairs map[string]string) {
		t.Helper()
		db := database.GetDB()
		for key, value := range pairs {
			db.Where("key = ?", key).Delete(&model.Setting{})
			if err := db.Create(&model.Setting{Key: key, Value: value}).Error; err != nil {
				t.Fatalf("set %s: %v", key, err)
			}
		}
	}
	save(map[string]string{"subDomain": "subs.example.com", "subPort": "2096"})

	svc := NewSubService(false, "", "")

	// The front-end is off: the subscription server answers for itself.
	subURL, _, _ := svc.BuildURLs("https", "panel.example.com:2053", "/sub/", "/json/", "/clash/", "abc")
	if subURL != "http://subs.example.com:2096/sub/abc" {
		t.Errorf("sub URL with the front-end off = %q", subURL)
	}

	save(map[string]string{
		"nginxMode":          "shared",
		"nginxDomain":        "vpn.example.com",
		"nginxSubsBehind443": "true",
	})
	subURL, jsonURL, clashURL := svc.BuildURLs("https", "panel.example.com:2053", "/sub/", "/json/", "/clash/", "abc")
	for _, got := range []struct{ url, want string }{
		{subURL, "https://vpn.example.com/sub/abc"},
		{jsonURL, "https://vpn.example.com/json/abc"},
		{clashURL, "https://vpn.example.com/clash/abc"},
	} {
		if got.url != got.want {
			t.Errorf("URL = %q, want %q", got.url, got.want)
		}
	}
}

func TestHiddifyCompatAddsALPN(t *testing.T) {
	s := &SubService{
		address:       "example.com",
		remarkModel:   "-ieo",
		hiddifyCompat: true,
	}
	inbound := &model.Inbound{
		Protocol: "vless",
		Port:     443,
		Settings: `{"clients":[{"id":"00000000-0000-0000-0000-000000000000","email":"test@example.com"}]}`,
		StreamSettings: `{
			"network": "xhttp",
			"security": "reality",
			"xhttpSettings": {"path": "/xhttp"},
			"realitySettings": {
				"serverNames": ["www.amd.com"],
				"shortIds": ["0123456789abcdef"],
				"settings": {"publicKey": "abcdef", "fingerprint": "chrome"}
			}
		}`,
	}
	link := s.genVlessLink(inbound, "test@example.com")
	if !strings.Contains(link, "alpn=h2") {
		t.Fatalf("expected alpn=h2 in link, got: %s", link)
	}

	// When hiddifyCompat is disabled
	s.hiddifyCompat = false
	linkNoCompat := s.genVlessLink(inbound, "test@example.com")
	if strings.Contains(linkNoCompat, "alpn=h2") {
		t.Fatalf("did not expect alpn=h2 when hiddifyCompat is false, got: %s", linkNoCompat)
	}
}

func TestResolveRequestDoesNotUseClientRealIPAsHost(t *testing.T) {
	s := NewSubService(false, "", "")

	// Simulate a request proxied by Nginx:
	// Host is the domain (net-ru.modulator.net)
	// X-Real-IP is the client phone's IP (198.51.100.23)
	// X-Forwarded-Proto is https
	req, _ := http.NewRequest("GET", "/sub/test-id", nil)
	req.Host = "net-ru.modulator.net"
	req.Header.Set("X-Real-IP", "198.51.100.23")
	req.Header.Set("X-Forwarded-Proto", "https")

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req

	scheme, host, hostWithPort, hostHeader := s.ResolveRequest(c)

	if scheme != "https" {
		t.Errorf("scheme = %q, want https", scheme)
	}
	if host != "net-ru.modulator.net" {
		t.Errorf("host = %q, want net-ru.modulator.net (X-Real-IP must not override host)", host)
	}
	if hostWithPort != "net-ru.modulator.net" {
		t.Errorf("hostWithPort = %q, want net-ru.modulator.net", hostWithPort)
	}
	if hostHeader != "net-ru.modulator.net" {
		t.Errorf("hostHeader = %q, want net-ru.modulator.net", hostHeader)
	}

	// Also test when X-Forwarded-Host is explicitly set by Nginx
	req.Header.Set("X-Forwarded-Host", "net-ru.modulator.net")
	_, host, _, _ = s.ResolveRequest(c)
	if host != "net-ru.modulator.net" {
		t.Errorf("host with X-Forwarded-Host = %q, want net-ru.modulator.net", host)
	}
}
