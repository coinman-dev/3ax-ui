package service

import "github.com/mhsanaei/3x-ui/v3/internal/database"

// 3AX-UI: off on a fresh install; the 1.x migration turns it on for an upgraded panel.
func init() {
	defaultValueMap[database.AxLegacyInboundsViewKey] = "false"
}

// GetLegacyInboundsView reports whether inbounds and their clients share one page, as in 1.x.
func (s *SettingService) GetLegacyInboundsView() (bool, error) {
	return s.getBool(database.AxLegacyInboundsViewKey)
}
