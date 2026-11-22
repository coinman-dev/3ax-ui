package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Releases publish one SHA256SUMS for every archive instead of a .sha256 beside each; the
// installer and updater must check against it, and still against the sidecar of older releases.
func TestScriptsVerifyArchivesAgainstSHA256SUMS(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	const asset = "x-ui-linux-amd64.tar.gz"
	archive := []byte("archive bytes")
	sum := sha256.Sum256(archive)
	good := hex.EncodeToString(sum[:])
	bad := hex.EncodeToString(make([]byte, 32))

	for _, tc := range []struct {
		name   string
		served map[string]string
		ok     bool
	}{
		{"its line in SHA256SUMS matches", map[string]string{"SHA256SUMS": bad + "  x-ui-linux-arm64.tar.gz\n" + good + "  " + asset + "\n"}, true},
		{"its line in SHA256SUMS differs", map[string]string{"SHA256SUMS": bad + "  " + asset + "\n"}, false},
		{"SHA256SUMS lacks its line", map[string]string{"SHA256SUMS": good + "  x-ui-linux-arm64.tar.gz\n"}, false},
		{"older release with a sidecar", map[string]string{asset + ".sha256": good + "  " + asset + "\n"}, true},
		{"older release with a wrong sidecar", map[string]string{asset + ".sha256": bad + "  " + asset + "\n"}, false},
		{"release with no checksums at all", nil, true},
	} {
		for _, script := range []string{"install.sh", "update.sh"} {
			t.Run(script+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				served := filepath.Join(dir, "release")
				for name, body := range tc.served {
					mustWrite(t, filepath.Join(served, name), body)
				}
				file := filepath.Join(dir, "x-ui-linux-amd64.tar.gz")
				mustWrite(t, file, string(archive))
				driver := "set -u\nred= green= yellow= plain=\n" +
					"_fail() { echo \"$1\"; exit 2; }\n" +
					// Stands in for curl against the release: serves files by name, prints the HTTP code.
					"curl() { local out= url=; while [ $# -gt 0 ]; do case $1 in -o) out=$2; shift 2;;" +
					" -w|--retry|--retry-delay|--connect-timeout|--max-time) shift 2;; -*) shift;; *) url=$1; shift;; esac; done;" +
					" if [ -f \"" + served + "/${url##*/}\" ]; then cp \"" + served + "/${url##*/}\" \"$out\"; printf 200; else printf 404; fi; }\n" +
					"curl_bin=curl\n" +
					scriptFunction(t, script, "verify_release_checksum") +
					"verify_release_checksum https://github.com/coinman-dev/3ax-ui/releases/download/v9.9.9/" + asset + " " + file + "\n"
				out, err := exec.Command(bash, "-c", driver).CombinedOutput()
				if tc.ok && err != nil {
					t.Fatalf("rejected a verified archive: %v\n%s", err, out)
				}
				if !tc.ok {
					if err == nil {
						t.Fatalf("accepted an archive it could not verify:\n%s", out)
					}
					if _, statErr := os.Stat(file); !os.IsNotExist(statErr) {
						t.Errorf("unverified archive left on disk")
					}
				}
			})
		}
	}
}
