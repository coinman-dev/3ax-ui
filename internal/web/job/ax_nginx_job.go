package job

import (
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// NginxJob keeps the generated nginx config in step with the inbounds, whose edits change how
// 443 is split without going through the nginx settings page.
type NginxJob struct {
	nginxService service.NginxService
}

func NewNginxJob() *NginxJob {
	return new(NginxJob)
}

func (j *NginxJob) Run() {
	// First: an unconfirmed mode that closed the ports must roll back before reconcile
	// dutifully puts it back in place.
	j.nginxService.CheckConfirmation()
	j.nginxService.Reconcile()
}
