package service

import (
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/mtproto"
)

type axMtprotoStatus struct {
	Configured int    `json:"configured" example:"2"`
	Running    int    `json:"running" example:"2"`
	Version    string `json:"version" example:"1.16.1"`
}

func currentMtprotoStatus() axMtprotoStatus {
	var configured int64
	if err := database.GetDB().Model(model.Inbound{}).
		Where("protocol = ? AND enable = ? AND node_id IS NULL", model.MTProto, true).
		Count(&configured).Error; err != nil {
		logger.Warning("count mtproto inbounds failed:", err)
	}
	st := axMtprotoStatus{Configured: int(configured)}
	if configured > 0 {
		st.Running = mtproto.GetManager().RunningCount()
		st.Version = mtproto.Version()
	}
	return st
}
