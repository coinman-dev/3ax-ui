package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The beta channel must pick the highest semver among all releases: a stable
// tag outranks its own pre-releases, and beta.10 outranks beta.2.
func TestScriptsResolveNewestTag(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	cases := []struct {
		name string
		tags []string
		want string
	}{
		{"stable beats its pre-releases", []string{"v2.0.0-rc.1", "v2.0.0", "v2.0.0-beta.3"}, "v2.0.0"},
		{"numeric pre-release order", []string{"v2.0.0-beta.2", "v2.0.0-beta.10", "v1.8.1"}, "v2.0.0-beta.10"},
		{"pre-release of a newer version", []string{"v1.8.1", "v2.1.0-beta.1", "v2.0.0"}, "v2.1.0-beta.1"},
		{"non-version tags are ignored", []string{"dev-latest", "v1.8.1"}, "v1.8.1"},
	}
	for _, script := range []string{"install.sh", "update.sh"} {
		fn := scriptFunction(t, script, "resolve_newest_tag")
		for _, tc := range cases {
			t.Run(script+"/"+tc.name, func(t *testing.T) {
				var body strings.Builder
				for _, tag := range tc.tags {
					body.WriteString(`  "tag_name": "` + tag + `",\n`)
				}
				driver := "fake_api() { printf '" + body.String() + "'; }\n" +
					"curl() { fake_api; }\ncurl_bin=fake_api\n" + fn + "resolve_newest_tag\n"
				out, err := exec.Command(bash, "-c", driver).CombinedOutput()
				if err != nil {
					t.Fatalf("resolve_newest_tag: %v\n%s", err, out)
				}
				if got := strings.TrimSpace(string(out)); got != tc.want {
					t.Errorf("resolve_newest_tag = %q, want %q", got, tc.want)
				}
			})
		}
	}
}
