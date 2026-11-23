package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 3AX-UI 1.x left nginx's stock site on port 80 while its certificates were issued in
// acme.sh standalone mode, so every renewal failed to bind port 80 and the certificate expired.
func TestUpdateMovesStandaloneCertsBehindNginxToWebroot(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	for _, tc := range []struct {
		name, version string
		want          map[string]string
	}{
		{"upgrade from 1.x", "1.8.1", map[string]string{
			"domain": "Le_Webroot='WEBROOT'", "custom": "Le_Webroot='/srv/own'", "fwd": "Le_Webroot='no'",
		}},
		{"not a 1.x upgrade", "2.0.0", map[string]string{
			"domain": "Le_Webroot='no'", "custom": "Le_Webroot='/srv/own'", "fwd": "Le_Webroot='no'",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			webroot := filepath.Join(dir, "www")
			site := filepath.Join(dir, "default")
			acme := filepath.Join(dir, "acme")
			mustWrite(t, site, "server {\n\tlisten 80 default_server;\n\troot "+webroot+";\n}\n")
			if err := os.MkdirAll(webroot, 0o755); err != nil {
				t.Fatal(err)
			}
			for name, body := range map[string]string{
				"domain": "Le_Domain='d.example'\nLe_Webroot='no'\nLe_HTTPPort='80'\n",
				"custom": "Le_Domain='c.example'\nLe_Webroot='/srv/own'\nLe_HTTPPort='80'\n",
				"fwd":    "Le_Domain='f.example'\nLe_Webroot='no'\nLe_HTTPPort='8080'\n",
			} {
				mustWrite(t, filepath.Join(acme, name+"_ecc", name+".conf"), body)
			}
			mustWrite(t, filepath.Join(acme, "account.conf"), "Le_Webroot='no'\n")
			driver := "set -u\nred= green= yellow= plain=\nnginx() { :; }\ncurrent_xui_version=" + tc.version + "\n" +
				scriptFunction(t, "update.sh", "ax1x_acme_webroot") +
				"ax1x_acme_webroot " + acme + " " + site + "\n"
			if out, err := exec.Command(bash, "-c", driver).CombinedOutput(); err != nil {
				t.Fatalf("ax1x_acme_webroot: %v\n%s", err, out)
			}
			for name, want := range tc.want {
				raw, _ := os.ReadFile(filepath.Join(acme, name+"_ecc", name+".conf"))
				want = strings.ReplaceAll(want, "WEBROOT", webroot)
				if !strings.Contains(string(raw), want) {
					t.Errorf("%s.conf:\n%s\nwant %s", name, raw, want)
				}
			}
			if raw, _ := os.ReadFile(filepath.Join(acme, "account.conf")); string(raw) != "Le_Webroot='no'\n" {
				t.Errorf("account.conf was touched: %q", raw)
			}
		})
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Switching to webroot only fixed the next renewal: an already expired certificate stayed
// in place until acme.sh's daily cron, so an upgraded panel kept serving it for up to a day.
func TestUpdateRenewsDueCertsRightAfterMovingThemToWebroot(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	for _, tc := range []struct {
		name, version, webroot, want string
	}{
		{"a certificate was moved to webroot", "1.8.1", "no", "--cron --home ACME\n"},
		{"nothing was moved", "1.8.1", "/srv/own", ""},
		{"not a 1.x upgrade", "2.0.0-beta.3", "no", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			webroot := filepath.Join(dir, "www")
			site := filepath.Join(dir, "default")
			acme := filepath.Join(dir, "acme")
			calls := filepath.Join(dir, "calls")
			mustWrite(t, site, "server {\n\tlisten 80 default_server;\n\troot "+webroot+";\n}\n")
			if err := os.MkdirAll(webroot, 0o755); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(acme, "d_ecc", "d.conf"), "Le_Domain='d.example'\nLe_Webroot='"+tc.webroot+"'\nLe_HTTPPort='80'\n")
			mustWrite(t, filepath.Join(acme, "acme.sh"), "#!/bin/bash\necho \"$*\" >> "+calls+"\n")
			if err := os.Chmod(filepath.Join(acme, "acme.sh"), 0o755); err != nil {
				t.Fatal(err)
			}
			driver := "set -u\nred= green= yellow= plain=\nnginx() { :; }\ncurrent_xui_version=" + tc.version + "\n" +
				scriptFunction(t, "update.sh", "ax1x_acme_webroot") +
				scriptFunction(t, "update.sh", "ax1x_acme_renew_due") +
				"ax1x_acme_webroot " + acme + " " + site + "\nax1x_acme_renew_due " + acme + "\n"
			if out, err := exec.Command(bash, "-c", driver).CombinedOutput(); err != nil {
				t.Fatalf("update.sh functions: %v\n%s", err, out)
			}
			got, _ := os.ReadFile(calls)
			if want := strings.ReplaceAll(tc.want, "ACME", acme); string(got) != want {
				t.Errorf("acme.sh calls = %q, want %q", got, want)
			}
		})
	}
}
