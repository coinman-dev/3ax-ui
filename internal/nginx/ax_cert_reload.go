package nginx

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
)

// reloadNginx is the seam the tests replace.
var reloadNginx = Reload

// loadedCertificate is the digest of the certificate and key nginx last reloaded with.
var loadedCertificate struct {
	sync.Mutex
	sum string
}

func forgetLoadedCertificate() {
	loadedCertificate.Lock()
	loadedCertificate.sum = ""
	loadedCertificate.Unlock()
}

// ReloadOnRenewedCertificate reloads nginx when the site's certificate changed on disk: renewals
// rewrite the files in place, and acme.sh's reload command does not know about nginx.
func ReloadOnRenewedCertificate(c Config) error {
	if c.Site == nil || c.Site.CertFile == "" {
		return nil
	}
	h := sha256.New()
	for _, path := range []string{c.Site.CertFile, c.Site.KeyFile} {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		h.Write(raw)
	}
	sum := hex.EncodeToString(h.Sum(nil))

	loadedCertificate.Lock()
	defer loadedCertificate.Unlock()
	if loadedCertificate.sum == sum {
		return nil
	}
	if err := reloadNginx(); err != nil {
		return err
	}
	loadedCertificate.sum = sum
	return nil
}
