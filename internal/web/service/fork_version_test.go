package service

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// A 3AX-UI 2.x node is built on upstream 3.11+, so the upstream-numbered
// first-release gate must not refuse it, while real old upstream nodes still are.
func TestCheckNodeCanHostProtocol_3AXVersions(t *testing.T) {
	setupConflictDB(t)
	tests := []struct {
		version  string
		protocol model.Protocol
		wantErr  string
	}{
		{"2.0.0", model.AmneziaWG, ""},
		{"v2.0.0", model.MASQUE, ""},
		{"2.3.1", model.TUIC, ""},
		{"v3.6.0", model.AmneziaWG, "need v3.7.0 or newer"},
		{"v3.11.0", model.AmneziaWG, ""},
	}
	for i, tc := range tests {
		t.Run(tc.version+"/"+string(tc.protocol), func(t *testing.T) {
			node := &model.Node{Name: "ax-node-" + tc.version + "-" + string(tc.protocol), Address: "127.0.0.1", Port: 3096 + i, PanelVersion: tc.version}
			if err := database.GetDB().Create(node).Error; err != nil {
				t.Fatalf("create node: %v", err)
			}
			err := checkNodeCanHostProtocol(database.GetDB(), node.Id, tc.protocol)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("node on %s refused %s: %v", tc.version, tc.protocol, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("node on %s: err = %v, want one containing %q", tc.version, err, tc.wantErr)
			}
		})
	}
}

func TestUpstreamComparableVersion(t *testing.T) {
	tests := []struct{ in, want string }{
		{"2.0.0", "3.11.0"},
		{"v2.0.0", "3.11.0"},
		{"2.4.2", "3.11.0"},
		{"v3.9.1", "v3.9.1"},
		{"dev+1d1128cf", "dev+1d1128cf"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := upstreamComparableVersion(tc.in); got != tc.want {
			t.Errorf("upstreamComparableVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
