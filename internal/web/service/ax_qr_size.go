package service

import "slices"

// qrCodeSizes are the QR sizes 3AX-UI offers, in pixels; 1.x databases already
// carry the setting, so an out-of-range value falls back to the default.
var qrCodeSizes = []int{300, 450, 600}

const defaultQrCodeSize = 450

func (s *SettingService) GetQrCodeSize() (int, error) {
	size, err := s.getInt("qrCodeSize")
	if err != nil || !slices.Contains(qrCodeSizes, size) {
		return defaultQrCodeSize, nil
	}
	return size, nil
}
