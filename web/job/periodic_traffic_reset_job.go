package job

import (
	"time"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/logger"
	"github.com/coinman-dev/3ax-ui/v2/web/service"
)

// Period represents the time period for traffic resets.
type Period string

// PeriodicTrafficResetJob resets traffic statistics for inbounds based on their configured reset period.
type PeriodicTrafficResetJob struct {
	inboundService service.InboundService
	awgService     service.AwgService
	period         Period
}

// NewPeriodicTrafficResetJob creates a new periodic traffic reset job for the specified period.
func NewPeriodicTrafficResetJob(period Period) *PeriodicTrafficResetJob {
	return &PeriodicTrafficResetJob{
		period: period,
	}
}

// Run resets traffic statistics for all inbounds that match the configured reset period.
func (j *PeriodicTrafficResetJob) Run() {
	inbounds, err := j.inboundService.GetInboundsByTrafficReset(string(j.period))
	if err != nil {
		logger.Warning("Failed to get inbounds for traffic reset:", err)
		return
	}

	if len(inbounds) == 0 {
		return
	}
	logger.Infof("Running periodic traffic reset job for period: %s (%d matching inbounds)", j.period, len(inbounds))

	resetCount := 0

	for _, inbound := range inbounds {
		if inbound.Protocol == model.AmneziaWG {
			if err := j.awgService.ResetAllClientTraffics(); err != nil {
				logger.Warning("Failed to reset AWG client traffics:", err)
			} else {
				// Update lastTrafficResetTime on the AWG inbound record
				now := time.Now().UnixMilli()
				db := database.GetDB()
				db.Model(model.Inbound{}).Where("id = ?", inbound.Id).Update("last_traffic_reset_time", now)
				resetCount++
			}
			continue
		}

		resetClientErr := j.inboundService.ResetAllClientTraffics(inbound.Id)
		if resetClientErr != nil {
			logger.Warning("Failed to reset traffic for all users of inbound", inbound.Id, ":", resetClientErr)
		} else {
			resetCount++
		}
	}

	if resetCount > 0 {
		logger.Infof("Periodic traffic reset completed: %d inbounds reset", resetCount)
	}
}
