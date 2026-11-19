package panel

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

func TestCompareReleaseVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"v2.0.0", "2.0.0", 0, true},
		{"2.0.1", "2.0.0", 1, true},
		{"2.0.0", "2.0.0-beta.3", 1, true},
		{"2.0.0-beta.2", "2.0.0-beta.10", -1, true},
		{"2.0.0-rc.1", "2.0.0-beta.9", 1, true},
		{"2.0.0-beta", "2.0.0-beta.1", -1, true},
		{"2.1.0-beta.1", "2.0.0", 1, true},
		{"1.8.1", "2.0.0-beta.1", -1, true},
		{"dev+1d1128cf", "2.0.0", 0, false},
		{"2.0", "2.0.0", 0, false},
		{"2.0.0-", "2.0.0", 0, false},
	}
	for _, tc := range cases {
		got, ok := compareReleaseVersions(tc.a, tc.b)
		if ok != tc.ok || got != tc.want {
			t.Errorf("compareReleaseVersions(%q, %q) = (%d, %v), want (%d, %v)", tc.a, tc.b, got, ok, tc.want, tc.ok)
		}
	}
}

// A beta-channel panel must be offered a stable release that outranks its
// prerelease, and must never be told a lower stable tag is an update.
func TestIsNewerVersionAcrossPrereleases(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v2.0.0", "2.0.0-beta.3", true},
		{"v2.0.0", "2.1.0-beta.1", false},
		{"v2.0.0-beta.4", "2.0.0-beta.3", true},
		{"v2.0.0-beta.3", "2.0.0-beta.3", false},
	}
	for _, tc := range cases {
		if got := isNewerVersion(tc.latest, tc.current); got != tc.want {
			t.Errorf("isNewerVersion(%q, %q) = %v, want %v", tc.latest, tc.current, got, tc.want)
		}
	}
}

func TestNewestReleaseTag(t *testing.T) {
	cases := []struct {
		name     string
		releases []service.Release
		want     string
		ok       bool
	}{
		{
			name:     "prerelease newer than stable",
			releases: []service.Release{{TagName: "v1.8.1"}, {TagName: "v2.0.0-beta.2", Prerelease: true}, {TagName: "v2.0.0-beta.1", Prerelease: true}},
			want:     "v2.0.0-beta.2",
			ok:       true,
		},
		{
			name:     "stable newer than every prerelease",
			releases: []service.Release{{TagName: "v2.0.0-rc.1", Prerelease: true}, {TagName: "v2.0.0"}},
			want:     "v2.0.0",
			ok:       true,
		},
		{
			name:     "unparseable tags are skipped",
			releases: []service.Release{{TagName: "dev-latest", Prerelease: true}, {TagName: "v1.8.1"}},
			want:     "v1.8.1",
			ok:       true,
		},
		{name: "nothing usable", releases: []service.Release{{TagName: "dev-latest"}}, ok: false},
	}
	for _, tc := range cases {
		got, ok := newestReleaseTag(tc.releases)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: newestReleaseTag = (%q, %v), want (%q, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}
