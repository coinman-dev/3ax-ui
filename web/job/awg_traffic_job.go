package job

import (
	"github.com/coinman-dev/3ax-ui/v2/web/service"
)

// AwgTrafficJob periodically collects AmneziaWG peer traffic stats and updates the database.
type AwgTrafficJob struct {
	awgService service.AwgService
}

func NewAwgTrafficJob() *AwgTrafficJob {
	return new(AwgTrafficJob)
}

func (j *AwgTrafficJob) Run() {
	j.awgService.UpdateTrafficStats()
}
