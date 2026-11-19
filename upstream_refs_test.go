package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Shipped code that points at upstream's repository makes a 3AX-UI panel
// offer, download or install MHSanaei/3x-ui; upstream merges re-add such URLs.
var upstreamRepoRef = regexp.MustCompile(`(?i)(raw\.githubusercontent\.com|api\.github\.com/repos|github\.com|ghcr\.io)/mhsanaei/(3x-ui|sponsors)\b(/[a-z0-9]+)?`)

// The Go module path, upstream issue/PR citations and the mtg-multi fork the
// MTProto manager is written against are references, not destinations.
func allowedUpstreamRef(match string) bool {
	lower := strings.ToLower(match)
	return strings.HasSuffix(lower, "/3x-ui/v3") ||
		strings.HasSuffix(lower, "/3x-ui/issues") ||
		strings.HasSuffix(lower, "/3x-ui/pull")
}

var upstreamRefRoots = []string{
	"install.sh", "update.sh", "x-ui.sh", "DockerInit.sh", "DockerEntrypoint.sh",
	"Dockerfile", "docker-compose.yml", "deploy", "internal", "frontend/src",
	".github/workflows", ".github/ISSUE_TEMPLATE", "winres",
}

func isTestSource(path string) bool {
	base := filepath.Base(path)
	return strings.HasSuffix(base, "_test.go") ||
		strings.Contains(base, ".test.") ||
		strings.Contains(base, ".stories.") ||
		strings.Contains(filepath.ToSlash(path), "/testdata/")
}

func TestNoUpstreamRepoReferences(t *testing.T) {
	var offenders []string
	for _, root := range upstreamRefRoots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == "dist" {
					return filepath.SkipDir
				}
				return nil
			}
			if isTestSource(path) {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(data), "\n") {
				for _, m := range upstreamRepoRef.FindAllString(line, -1) {
					if !allowedUpstreamRef(m) {
						offenders = append(offenders, path+":"+strconv.Itoa(i+1)+": "+m)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}
	if len(offenders) > 0 {
		t.Errorf("%d reference(s) to the upstream repository; point them at coinman-dev/3ax-ui:\n%s",
			len(offenders), strings.Join(offenders, "\n"))
	}
}
