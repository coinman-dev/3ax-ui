package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// The dashboard named Xray's and MTProto's versions but not AmneziaWG's, which 1.x showed;
// a kernel-mode inbound runs on the module, so its version is the one that matters there.
func TestAmneziawgStatusVersionFollowsTheEngineInUse(t *testing.T) {
	module := filepath.Join(t.TempDir(), "version")
	if err := os.WriteFile(module, []byte("3.1.20260812\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPath, oldEmbedded := awgModuleVersionPath, awgEmbeddedVersion
	awgModuleVersionPath, awgEmbeddedVersion = module, "3.1.20260828"
	t.Cleanup(func() { awgModuleVersionPath, awgEmbeddedVersion = oldPath, oldEmbedded })

	nodeID := 5
	kernel := `{"server":{"kernelMode":true},"clients":[]}`
	embedded := `{"server":{},"clients":[]}`
	for _, tc := range []struct {
		name     string
		inbounds []model.Inbound
		want     axAmneziawgStatus
	}{
		{"no AmneziaWG inbound", []model.Inbound{
			{Tag: "vless", Enable: true, Port: 40001, Protocol: model.VLESS, Settings: `{"clients":[]}`},
		}, axAmneziawgStatus{}},
		{"a kernel-mode inbound reports the module", []model.Inbound{
			{Tag: "awg-kernel", Enable: true, Port: 40002, Protocol: model.AmneziaWG, Settings: kernel},
			{Tag: "awg-embedded", Enable: true, Port: 40003, Protocol: model.AmneziaWG, Settings: embedded},
			{Tag: "awg-off", Enable: false, Port: 40004, Protocol: model.AmneziaWG, Settings: kernel},
			{Tag: "awg-node", Enable: true, Port: 40005, Protocol: model.AmneziaWG, Settings: kernel, NodeID: &nodeID},
		}, axAmneziawgStatus{Configured: 2, Version: "3.1.20260812"}},
		{"embedded only reports the built-in engine", []model.Inbound{
			{Tag: "awg-embedded", Enable: true, Port: 40003, Protocol: model.AmneziaWG, Settings: embedded},
		}, axAmneziawgStatus{Configured: 1, Version: "3.1.20260828"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
			for i := range tc.inbounds {
				tc.inbounds[i].UserId = 1
				if err := database.GetDB().Create(&tc.inbounds[i]).Error; err != nil {
					t.Fatalf("create %s: %v", tc.inbounds[i].Tag, err)
				}
			}
			if got := currentAmneziawgStatus(); got != tc.want {
				t.Fatalf("status = %+v, want %+v (nothing is up in a test, so Running stays 0)", got, tc.want)
			}
		})
	}
}

// encoding/json silently drops both fields of a duplicated key, so reusing upstream's
// "amneziawg" would have blanked its log view's flags as well as the version.
func TestStatusCarriesBothAmneziaWGViews(t *testing.T) {
	var st Status
	st.AmneziaWG.Configured = true
	st.Awg = axAmneziawgStatus{Configured: 1, Running: 1, Version: "3.1.20260812"}
	raw, err := json.Marshal(&st)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"amneziawg":{"configured":true`, `"awg":{"configured":1,"running":1,"version":"3.1.20260812"}`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("status JSON lacks %s", want)
		}
	}
}
