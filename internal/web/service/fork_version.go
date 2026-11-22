package service

import (
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/util/version"
)

// axUpstreamBase is the upstream release each 3AX-UI major line is built on.
// Raise it on every upstream merge that a 3AX-UI release ships.
const axUpstreamBase = "3.11.0"

// upstreamComparableVersion maps a 3AX-UI 2.x version onto the upstream release
// it carries, so upstream-numbered feature gates judge it correctly.
func upstreamComparableVersion(v string) string {
	// A pre-release (2.0.0-beta.1) belongs to its line; upstream's parser rejects the suffix.
	core, _, _ := strings.Cut(v, "-")
	if cmp, ok := version.Compare(core, "3.0.0"); ok && cmp < 0 {
		if lo, ok := version.Compare(core, "2.0.0"); ok && lo >= 0 {
			return axUpstreamBase
		}
	}
	return v
}
