package service

import (
	"os"
	"runtime/debug"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgkern"
	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgnet"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// 3AX-UI shows AmneziaWG's state and engine version on the dashboard, as 1.x did.
type axAmneziawgStatus struct {
	Configured int    `json:"configured" example:"1"`
	Running    int    `json:"running" example:"1"`
	Version    string `json:"version" example:"3.1.20260812"`
}

var (
	awgModuleVersionPath = "/sys/module/amneziawg/version"
	awgEmbeddedVersion   = embeddedAmneziawgVersion()
)

func embeddedAmneziawgVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, dep := range info.Deps {
		if strings.HasPrefix(dep.Path, "github.com/amnezia-vpn/amneziawg-go") {
			return strings.TrimPrefix(dep.Version, "v")
		}
	}
	return ""
}

func currentAmneziawgStatus() axAmneziawgStatus {
	var inbounds []model.Inbound
	if err := database.GetDB().Select("id", "protocol", "settings").
		Where("protocol = ? AND enable = ? AND node_id IS NULL", model.AmneziaWG, true).
		Find(&inbounds).Error; err != nil {
		logger.Warning("list amneziawg inbounds failed:", err)
		return axAmneziawgStatus{}
	}
	st := axAmneziawgStatus{Configured: len(inbounds)}
	if len(inbounds) == 0 {
		return st
	}
	kernelUp := map[int]bool{}
	for _, id := range amneziawgkern.GetManager().ManagedIDs() {
		kernelUp[id] = true
	}
	anyKernel := false
	for i := range inbounds {
		if amneziawgKernelMode(&inbounds[i]) {
			anyKernel = true
			if kernelUp[inbounds[i].Id] {
				st.Running++
			}
		} else if _, _, ok := amneziawgnet.GetManager().Lookup(inbounds[i].Id); ok {
			st.Running++
		}
	}
	st.Version = awgEmbeddedVersion
	if anyKernel {
		raw, _ := os.ReadFile(awgModuleVersionPath)
		st.Version = strings.TrimSpace(string(raw))
	}
	return st
}
