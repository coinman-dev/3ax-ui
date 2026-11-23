package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The cleanup must take only what a build recreates: a dev database, the notes
// kept outside git or the Xray binaries in bin/ have no other copy.
func TestCleanArtifactsRemovesOnlyRebuildableFiles(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	root := filepath.Join(t.TempDir(), "3ax-ui")
	script, err := os.ReadFile(filepath.Join("scripts", "clean-artifacts.sh"))
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "scripts", "clean-artifacts.sh"), string(script))
	removed := []string{
		"frontend/node_modules/vite/index.js", "frontend/storybook-static/index.html",
		"internal/web/dist/index.html", ".cache/node-e2e/x-ui", "docs/.next/cache",
		"tmp/scratch", "x-ui-linux-amd64.tar.gz", "frontend/src/App.tsx.orig",
	}
	kept := []string{
		"main.go", "frontend/src/App.tsx", "frontend/package-lock.json", ".ai/UPSTREAM_REBASE_PLAN.md",
		".claude/settings.local.json", "bin/xray-linux-amd64", "x-ui.db", "internal/web/translation/en-US.json",
	}
	for _, f := range append(append([]string{}, removed...), kept...) {
		mustWrite(t, filepath.Join(root, f), "x")
	}
	if err := os.Symlink("../vite/index.js", filepath.Join(root, "frontend/node_modules/.bin")); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bash, append([]string{filepath.Join(root, "scripts", "clean-artifacts.sh")}, args...)...)
		cmd.Dir = t.TempDir()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("clean-artifacts.sh %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	exists := func(f string) bool {
		_, err := os.Lstat(filepath.Join(root, f))
		return err == nil
	}

	if out := run(); !strings.Contains(out, "DRY RUN") {
		t.Errorf("default run is not a dry run:\n%s", out)
	}
	for _, f := range removed {
		if !exists(f) {
			t.Fatalf("the dry run removed %s", f)
		}
	}

	run("--force")
	for _, f := range removed {
		if exists(f) {
			t.Errorf("--force left %s", f)
		}
	}
	for _, f := range kept {
		if !exists(f) {
			t.Errorf("--force removed %s, which no build recreates", f)
		}
	}
	// A bare `go build` needs the embedded folder to exist, as make dist-stub leaves it.
	if !exists("internal/web/dist/.gitkeep") {
		t.Error("internal/web/dist was not left as a stub")
	}
}
