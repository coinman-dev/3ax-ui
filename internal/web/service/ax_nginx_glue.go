package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/nginx"
)

// loopbackPortTaken asks upstream's conflict check whether ib could move to
// port on 127.0.0.1, where the nginx front-end relocates inbounds.
func (s *NginxService) loopbackPortTaken(ib *model.Inbound, port int) (bool, error) {
	candidate := *ib
	candidate.Listen = "127.0.0.1"
	candidate.Port = port
	conflict, err := s.inboundService.checkPortConflict(&candidate, ib.Id)
	return conflict != nil, err
}

// mtprotoFakeTLSDomain is the cover domain an MTProto inbound announces as SNI.
func mtprotoFakeTLSDomain(settings string) string {
	var parsed struct {
		FakeTlsDomain string `json:"fakeTlsDomain"`
	}
	_ = json.Unmarshal([]byte(settings), &parsed)
	return strings.TrimSpace(parsed.FakeTlsDomain)
}

// nginxPublicPortConflict reserves 443 while nginx owns it: no inbound row holds
// it then, and one created there would fail to bind and break everything behind.
func nginxPublicPortConflict(inbound *model.Inbound, bits transportBits) *portConflictDetail {
	if inbound.NodeID != nil || inbound.Port != PublicPort || bits&transportTCP == 0 || !nginxOwnsPublicPort() {
		return nil
	}
	if !listenOverlaps(bindAddr{}, inboundBindAddr(inbound)) {
		return nil
	}
	return &portConflictDetail{Tag: "nginx", Port: PublicPort, Transports: transportTCP}
}

func nginxOwnsPublicPort() bool {
	mode, err := (&SettingService{}).getString("nginxMode")
	return err == nil && mode != "" && mode != string(nginx.ModeOff)
}

// nginxResetSurvivors outlive a settings reset: deleting them while nginx still
// serves 443 would strand every inbound on the loopback with no way back.
var nginxResetSurvivors = []string{"nginxMode", "nginxRelocated", "nginxRealityPort", "nginxDomain"}

// nginxDefaults join upstream's defaults table. An upgrade lands on "off": the
// panel cannot know what already sits on 443. nginxHttpPort 0 means "pick one".
var nginxDefaults = map[string]string{
	"nginxMode":            "off",
	"nginxDomain":          "",
	"nginxStubSiteId":      "0",
	"nginxSubsBehind443":   "false",
	"nginxPanelBehind443":  "false",
	"nginxManageFirewall":  "false",
	"nginxFirewallExtra":   "",
	"nginxConfirmDeadline": "0",
	"nginxConfirmFallback": "",
	"nginxRealityPort":     "8443",
	"nginxHttpPort":        "0",
	"nginxRelocated":       "",
	"nginxRelayPorts":      "",
}

func init() {
	for k, v := range nginxDefaults {
		defaultValueMap[k] = v
	}
}

// SetNginxMode is the shell's way to switch the front-end; only443 stays in the
// panel, where closing the other ports is confirmed or rolled back.
func (s *SettingService) SetNginxMode(mode string) error {
	switch nginx.Mode(mode) {
	case nginx.ModeOff, nginx.ModeShared:
		return s.setString("nginxMode", mode)
	case nginx.ModeOnly443:
		return errors.New("only443 closes every other port; switch to it from the panel's Nginx page")
	}
	return fmt.Errorf("unknown nginx mode %q, want off or shared", mode)
}

// localInbounds are the ones this host runs: a node's inbound lives elsewhere,
// out of reach of the local nginx and firewall.
func (s *NginxService) localInbounds() ([]*model.Inbound, error) {
	all, err := s.inboundService.GetAllInbounds()
	if err != nil {
		return nil, err
	}
	local := all[:0]
	for _, ib := range all {
		if ib.NodeID == nil {
			local = append(local, ib)
		}
	}
	return local, nil
}
