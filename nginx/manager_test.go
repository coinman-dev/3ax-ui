package nginx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// debianConf is the shape of a stock Debian/Ubuntu nginx.conf: no stream block,
// and the word "stream" nowhere near the top level.
const debianConf = `user www-data;
worker_processes auto;
pid /run/nginx.pid;
include /etc/nginx/modules-enabled/*.conf;

events {
	worker_connections 768;
}

http {
	sendfile on;
	include /etc/nginx/mime.types;
	include /etc/nginx/conf.d/*.conf;
	include /etc/nginx/sites-enabled/*;
}
`

func TestWithStreamIncludeAddsBlockOnce(t *testing.T) {
	ConfRoot = t.TempDir()

	once, err := withStreamInclude(debianConf)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if !strings.Contains(once, "include "+filepath.Join(ConfRoot, streamDir)+"/*.conf;") {
		t.Fatalf("the include is missing:\n%s", once)
	}
	if !strings.HasPrefix(once, "user www-data;") {
		t.Error("the original config was not kept")
	}

	// Applying twice must not stack two stream blocks — nginx allows only one.
	twice, err := withStreamInclude(once)
	if err != nil {
		t.Fatalf("second patch: %v", err)
	}
	if n := strings.Count(twice, "stream {"); n != 1 {
		t.Errorf("after two applies there are %d stream blocks, want 1", n)
	}
	if n := strings.Count(twice, includeBegin); n != 1 {
		t.Errorf("after two applies there are %d markers, want 1", n)
	}
}

// TestForeignStreamBlockIsRefused: a second top-level stream block is a syntax
// error, and editing the operator's own block is how a working server gets
// broken. Say so instead.
func TestForeignStreamBlockIsRefused(t *testing.T) {
	ConfRoot = t.TempDir()

	foreign := debianConf + "\nstream {\n    server { listen 8443; proxy_pass 127.0.0.1:9000; }\n}\n"
	if _, err := withStreamInclude(foreign); err == nil {
		t.Fatal("a config with its own stream block was accepted")
	} else if !strings.Contains(err.Error(), "already has a stream block") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestHasForeignStreamBlock(t *testing.T) {
	cases := []struct {
		name string
		conf string
		want bool
	}{
		{"stock debian", debianConf, false},
		{"top level", "stream {\n}\n", true},
		{"no space before brace", "stream{\n}\n", true},
		{"commented out", "# stream {\n#     server { listen 443; }\n# }\n", false},
		{
			// "stream" is also a valid word inside http {} — as a log format
			// name, a location, or an upstream. Depth has to be tracked.
			name: "inside http",
			conf: "http {\n    upstream stream {\n        server 127.0.0.1:1;\n    }\n}\n",
			want: false,
		},
		{
			name: "trailing comment on the line",
			conf: "http {\n}\nstream { # sni router\n}\n",
			want: true,
		},
		{
			// Ours does not count: it is replaced, not duplicated.
			name: "our own block",
			conf: debianConf + "\n" + includeBegin + "\nstream {\n    include /etc/nginx/stream-enabled/*.conf;\n}\n" + includeEnd + "\n",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasForeignStreamBlock(tc.conf); got != tc.want {
				t.Errorf("hasForeignStreamBlock = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRemoveStripsOnlyOurBlock checks the other direction: going back to mode
// off must leave the operator's config exactly as it was.
func TestRemoveStripsOnlyOurBlock(t *testing.T) {
	ConfRoot = t.TempDir()

	patched, err := withStreamInclude(debianConf)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	stripped := includeRe.ReplaceAllString(patched, "\n")
	if strings.Contains(stripped, "stream") {
		t.Errorf("something of ours was left behind:\n%s", stripped)
	}
	if strings.TrimSpace(stripped) != strings.TrimSpace(debianConf) {
		t.Errorf("the config did not come back to the original:\n%s", stripped)
	}
}

// TestTxRollback is the safety net behind Apply: a config nginx refuses must
// leave every file the way it was, including ones that did not exist before.
func TestTxRollback(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.conf")
	fresh := filepath.Join(dir, "sub", "fresh.conf")
	doomed := filepath.Join(dir, "doomed.conf")

	if err := os.WriteFile(existing, []byte("before\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doomed, []byte("keep me\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tx := newTx()
	if err := tx.write(existing, "after\n", 0644); err != nil {
		t.Fatal(err)
	}
	if err := tx.write(fresh, "new\n", 0644); err != nil {
		t.Fatal(err)
	}
	if err := tx.remove(doomed); err != nil {
		t.Fatal(err)
	}
	tx.rollback()

	if got := read(t, existing); got != "before\n" {
		t.Errorf("existing file = %q, want the original content", got)
	}
	if info, err := os.Stat(existing); err != nil {
		t.Error(err)
	} else if info.Mode().Perm() != 0640 {
		t.Errorf("permissions became %v, want 0640", info.Mode().Perm())
	}
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Error("a file that did not exist before was left behind")
	}
	if got := read(t, doomed); got != "keep me\n" {
		t.Errorf("deleted file = %q, want it restored", got)
	}
}

func TestTxCommitKeepsChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")

	tx := newTx()
	if err := tx.write(path, "new\n", 0644); err != nil {
		t.Fatal(err)
	}
	tx.commit()
	tx.done() // the deferred call must not undo a committed transaction

	if got := read(t, path); got != "new\n" {
		t.Errorf("after commit the file is %q, want the new content", got)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
