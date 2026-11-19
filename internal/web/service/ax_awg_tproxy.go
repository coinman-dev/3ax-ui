package service

import (
	"encoding/json"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgnet"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

const (
	awgKernelTproxySettings = `{"network":"tcp,udp","followRedirect":true}`
	awgKernelTproxyStream   = `{"sockopt":{"tproxy":"tproxy"}}`
)

// injectAmneziawgKernelTproxy gives each kernel-mode AmneziaWG inbound routed
// through Xray the TPROXY listener amneziawgkern hands its traffic to (3AX-UI).
// It takes the inbound's own tag and the relay port the embedded path would
// use, so Routing rules and the reserved port slot work the same in both modes.
func injectAmneziawgKernelTproxy(cfg *xray.Config, inbounds []*model.Inbound) {
	taken := make(map[string]struct{}, len(cfg.InboundConfigs))
	for i := range cfg.InboundConfigs {
		taken[cfg.InboundConfigs[i].Tag] = struct{}{}
	}
	for _, inbound := range inbounds {
		if inbound.Protocol != model.AmneziaWG || !inbound.Enable || inbound.NodeID != nil {
			continue
		}
		inst, ok := amneziawg.InstanceFromInbound(inbound)
		if !ok || !inst.KernelMode || !inst.RouteThroughXray {
			continue
		}
		if _, dup := taken[inbound.Tag]; dup {
			logger.Warning("amneziawg kernel tproxy: inbound tag [", inbound.Tag, "] already present in generated config, skipping its listener")
			continue
		}
		taken[inbound.Tag] = struct{}{}
		cfg.InboundConfigs = append(cfg.InboundConfigs, xray.InboundConfig{
			Listen:         json_util.RawMessage(`"::"`),
			Port:           amneziawgnet.SOCKSPortForInbound(inbound.Id),
			Protocol:       "tunnel",
			Settings:       json_util.RawMessage(awgKernelTproxySettings),
			StreamSettings: json_util.RawMessage(awgKernelTproxyStream),
			Sniffing:       json_util.RawMessage(amneziawgEgressSniffingSettings),
			Tag:            inbound.Tag,
		})
	}
}

// amneziawgRelayUnused reports a local AmneziaWG row that leaves its derived
// relay port free: kernel mode without routeThroughXray. With it, the port
// carries the TPROXY listener and stays reserved like the embedded relay.
func amneziawgRelayUnused(ib *model.Inbound) bool {
	if !amneziawgKernelMode(ib) {
		return false
	}
	inst, ok := amneziawg.InstanceFromInbound(ib)
	if ok {
		return !inst.RouteThroughXray
	}
	return !amneziawgRouteThroughXraySettings(ib.Settings)
}

func amneziawgRouteThroughXraySettings(raw string) bool {
	var settings amneziawg.InboundSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil || settings.Server == nil {
		return false
	}
	return settings.Server.RouteThroughXray
}
