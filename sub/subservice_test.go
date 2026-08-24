package sub

import (
	"path/filepath"
	"sync"
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
