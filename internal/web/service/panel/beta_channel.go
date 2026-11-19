package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/util/version"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// 3AX-UI has no per-commit build; its beta channel is the newest release
// GitHub lists, prerelease or not, compared by semver precedence.
const panelReleasesURL = "https://api.github.com/repos/coinman-dev/3ax-ui/releases?per_page=30"

func getBetaUpdateInfo() (*PanelUpdateInfo, error) {
	latest, err := latestBetaTag()
	if err != nil {
		return nil, err
	}
	current := config.GetBaseVersion()
	return &PanelUpdateInfo{
		Channel:         "beta",
		CurrentVersion:  current,
		LatestVersion:   latest,
		UpdateAvailable: isNewerVersion(latest, current),
	}, nil
}

func latestBetaTag() (string, error) {
	releases, err := fetchPanelReleases()
	if err != nil {
		return "", err
	}
	tag, ok := newestReleaseTag(releases)
	if !ok {
		return "", fmt.Errorf("no panel release with a version tag was found")
	}
	return tag, nil
}

func fetchPanelReleases() ([]service.Release, error) {
	client := (&service.SettingService{}).NewProxiedHTTPClient(10 * time.Second)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, panelReleasesURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, resp.Status)
	}
	var releases []service.Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxPanelReleaseBytes)).Decode(&releases); err != nil {
		return nil, err
	}
	return releases, nil
}

func newestReleaseTag(releases []service.Release) (string, bool) {
	best := ""
	for _, r := range releases {
		if _, ok := compareReleaseVersions(r.TagName, r.TagName); !ok {
			continue
		}
		if best == "" {
			best = r.TagName
			continue
		}
		if cmp, _ := compareReleaseVersions(r.TagName, best); cmp > 0 {
			best = r.TagName
		}
	}
	return best, best != ""
}

// compareReleaseVersions orders MAJOR.MINOR.PATCH[-PRERELEASE] tags by semver
// precedence; ok is false when either side is not such a version.
func compareReleaseVersions(a, b string) (int, bool) {
	aCore, aPre, okA := splitPrerelease(a)
	bCore, bPre, okB := splitPrerelease(b)
	if !okA || !okB {
		return 0, false
	}
	cmp, ok := version.Compare(aCore, bCore)
	if !ok {
		return 0, false
	}
	if cmp != 0 {
		return cmp, true
	}
	return comparePrerelease(aPre, bPre), true
}

func splitPrerelease(v string) (core, pre string, ok bool) {
	core, pre, hasPre := strings.Cut(version.Normalize(v), "-")
	if hasPre && pre == "" {
		return "", "", false
	}
	if _, valid := version.Compare(core, core); !valid {
		return "", "", false
	}
	return core, pre, true
}

func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	aIDs, bIDs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(aIDs) && i < len(bIDs); i++ {
		if c := comparePrereleaseID(aIDs[i], bIDs[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(aIDs) < len(bIDs):
		return -1
	case len(aIDs) > len(bIDs):
		return 1
	}
	return 0
}

func comparePrereleaseID(a, b string) int {
	an, aErr := strconv.Atoi(a)
	bn, bErr := strconv.Atoi(b)
	switch {
	case aErr == nil && bErr == nil:
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		}
		return 0
	case aErr == nil:
		return -1
	case bErr == nil:
		return 1
	}
	return strings.Compare(a, b)
}
