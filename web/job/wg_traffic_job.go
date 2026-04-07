package job

import (
	"github.com/coinman-dev/3ax-ui/v2/web/service"
)

// WgTrafficJob periodically collects WireGuard Native peer traffic stats and updates the database.
type WgTrafficJob struct {
	wgService service.WgService
}

func NewWgTrafficJob() *WgTrafficJob {
	return new(WgTrafficJob)
}

func (j *WgTrafficJob) Run() {
	j.wgService.UpdateTrafficStats()
}
