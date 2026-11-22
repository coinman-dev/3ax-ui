package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
)

// The sidebar and the inbounds page read the combined-view switch from the
// defaults payload; a missing key would leave an upgraded panel on the split pages.
func TestDefaultSettingsCarryTheCombinedInboundsView(t *testing.T) {
	initHappTestDB(t)
	s := &SettingService{}
	read := func() any {
		t.Helper()
		raw, err := s.GetDefaultSettings("panel.example.com")
		if err != nil {
			t.Fatal(err)
		}
		return raw.(map[string]any)["legacyInboundsView"]
	}
	if got := read(); got != false {
		t.Fatalf("fresh panel: legacyInboundsView = %v, want false", got)
	}
	if err := s.setString(database.AxLegacyInboundsViewKey, "true"); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != true {
		t.Fatalf("upgraded panel: legacyInboundsView = %v, want true", got)
	}
}
