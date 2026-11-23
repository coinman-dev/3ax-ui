package nginx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// acme.sh's reload command restarts only the panel, so a renewed certificate sat on disk while
// nginx kept serving the expired one it had loaded (net-ru, 2026-11-23).
func TestRenewedCertificateReloadsNginx(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(cert, "old cert")
	write(key, "old key")
	reloads, fail := 0, false
	prev := reloadNginx
	reloadNginx = func() error {
		if fail {
			return errors.New("nginx is down")
		}
		reloads++
		return nil
	}
	t.Cleanup(func() { reloadNginx = prev; forgetLoadedCertificate() })
	forgetLoadedCertificate()
	cfg := Config{Mode: ModeOnly443, Site: &Site{CertFile: cert, KeyFile: key}}

	steps := []struct {
		name        string
		change      func()
		failReload  bool
		wantReloads int
	}{
		{"first look after the panel starts: nginx may hold anything", nil, false, 1},
		{"nothing changed", nil, false, 1},
		{"renewed certificate and key", func() { write(cert, "new cert"); write(key, "new key") }, false, 2},
		{"same files again", nil, false, 2},
		{"renewed, but the reload fails", func() { write(cert, "newer cert") }, true, 2},
		{"the failed reload is retried", nil, false, 3},
	}
	for _, s := range steps {
		if s.change != nil {
			s.change()
		}
		fail = s.failReload
		_ = ReloadOnRenewedCertificate(cfg)
		if reloads != s.wantReloads {
			t.Fatalf("%s: %d reloads, want %d", s.name, reloads, s.wantReloads)
		}
	}

	if err := ReloadOnRenewedCertificate(Config{Mode: ModeShared}); err != nil {
		t.Errorf("a config without a site: %v", err)
	}
}
