package nginx

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestNginxAcceptsGeneratedConfig hands the generated config to nginx itself.
// The golden tests say the output has not changed; only nginx can say the
// output is valid — a directive in the wrong block, a variable that does not
// exist at that level, a module that is not loaded.
func TestNginxAcceptsGeneratedConfig(t *testing.T) {
	if _, err := exec.LookPath("nginx"); err != nil {
		t.Skip("nginx is not installed on this machine")
	}

	root := t.TempDir()
	ConfRoot = root
	t.Cleanup(func() { ConfRoot = "/etc/nginx" })

	for _, dir := range []string{"conf.d", "stream-enabled", "modules-enabled", "logs", "www"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// Carry over the distro's load_module lines. On Debian and Ubuntu the
	// stream module is dynamic and lives in its own package, so without these
	// the temporary tree would reject "stream" as an unknown directive even
	// though the machine can do it.
	copyModuleConfigs(t, root)

	// A stand-in for the distro's nginx.conf: includes conf.d from inside
	// http {} and, crucially, has no stream block of its own — exactly the
	// arrangement withStreamInclude has to patch.
	mainConf := fmt.Sprintf(`include %s/modules-enabled/*.conf;
worker_processes 1;
error_log %s/logs/error.log;
pid %s/logs/nginx.pid;
events { worker_connections 64; }
http {
    access_log off;
    include %s/conf.d/*.conf;
}
`, root, root, root, root)
	if err := os.WriteFile(MainConfPath(), []byte(mainConf), 0644); err != nil {
		t.Fatal(err)
	}

	if !HasStream() {
		t.Skip("this nginx has no stream module (on Debian/Ubuntu: apt install libnginx-mod-stream)")
	}

	certFile, keyFile := writeSelfSignedCert(t, root, "panel.example.net")
	cfg := goldenConfig()
	cfg.Mode = ModeOnly443
	// nginx -t refuses a privileged port when the tests are not run as root.
	// It does not mind a port another process is holding, which is what Apply
	// depends on: the config is verified while Xray still owns 443.
	cfg.Port = 14443
	cfg.Site = &Site{
		Domain:   "panel.example.net",
		CertFile: certFile,
		KeyFile:  keyFile,
		Listen:   "127.0.0.1:8080",
		Root:     filepath.Join(root, "www"),
		Panel:    &Proxy{Name: "panel", Paths: []string{"/uS2J19TzcfZuEAPyNH/"}, Target: "127.0.0.1:33757", TLS: true},
		Sub:      &Proxy{Name: "subscriptions", Paths: []string{"/sub-abc123/"}, Target: "127.0.0.1:2096"},
	}

	staged, err := Stage(cfg)
	if err != nil {
		t.Fatalf("nginx refused the generated config: %v", err)
	}
	// Activate would reload the machine's real nginx; the point here is only
	// that the config parses, so undo the files directly.
	t.Cleanup(func() { staged.tx.rollback() })

	for _, path := range []string{StreamConfPath(), HTTPConfPath()} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was not written: %v", path, err)
		}
	}
	patched, err := os.ReadFile(MainConfPath())
	if err != nil {
		t.Fatal(err)
	}
	if hasForeignStreamBlock(string(patched)) {
		t.Error("the include block we added is not recognised as ours; a second apply would duplicate it")
	}
}

// TestStagePutsFilesBackWhenNginxRefuses covers the other half: a config nginx
// rejects must leave nothing behind.
func TestStagePutsFilesBackWhenNginxRefuses(t *testing.T) {
	if _, err := exec.LookPath("nginx"); err != nil {
		t.Skip("nginx is not installed on this machine")
	}

	root := t.TempDir()
	ConfRoot = root
	t.Cleanup(func() { ConfRoot = "/etc/nginx" })

	for _, dir := range []string{"conf.d", "stream-enabled", "logs"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// Deliberately broken: an unclosed block. Whatever we generate, nginx will
	// refuse the file it is included from.
	mainConf := fmt.Sprintf("worker_processes 1;\nerror_log %s/logs/error.log;\nevents { worker_connections 64;\n", root)
	if err := os.WriteFile(MainConfPath(), []byte(mainConf), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := goldenConfig()
	if _, err := Stage(cfg); err == nil {
		t.Fatal("a config nginx cannot parse was accepted")
	}

	if got, err := os.ReadFile(MainConfPath()); err != nil {
		t.Fatal(err)
	} else if string(got) != mainConf {
		t.Errorf("nginx.conf was left patched after a failure:\n%s", got)
	}
	if _, err := os.Stat(StreamConfPath()); !os.IsNotExist(err) {
		t.Error("the stream config was left behind after a failure")
	}
}

// copyModuleConfigs mirrors the machine's nginx module load lines into the
// temporary tree, so the test exercises the same set of directives the real
// server has.
func copyModuleConfigs(t *testing.T, root string) {
	t.Helper()
	matches, _ := filepath.Glob("/etc/nginx/modules-enabled/*.conf")
	for _, src := range matches {
		body, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		dst := filepath.Join(root, "modules-enabled", filepath.Base(src))
		if err := os.WriteFile(dst, body, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func writeSelfSignedCert(t *testing.T, dir, domain string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certFile, certPEM, 0644); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
