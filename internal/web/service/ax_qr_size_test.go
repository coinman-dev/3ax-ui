package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// 1.x databases already hold qrCodeSize; only the three sizes the panel offers
// may reach the QR renderer, anything else falls back to the 450 default.
func TestQrCodeSize(t *testing.T) {
	setupBulkDB(t)
	svc := &SettingService{}
	if got, err := svc.GetQrCodeSize(); err != nil || got != 450 {
		t.Fatalf("default = %d, %v; want 450", got, err)
	}
	for stored, want := range map[string]int{"600": 600, "300": 300, "9999": 450, "abc": 450} {
		database.GetDB().Where("key = ?", "qrCodeSize").Delete(&model.Setting{})
		database.GetDB().Create(&model.Setting{Key: "qrCodeSize", Value: stored})
		if got, _ := svc.GetQrCodeSize(); got != want {
			t.Errorf("stored %q -> %d, want %d", stored, got, want)
		}
	}
}
