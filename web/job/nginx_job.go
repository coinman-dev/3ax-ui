package job

import (
	"github.com/coinman-dev/3ax-ui/v2/web/service"
)

// NginxJob keeps the generated nginx config in step with the inbounds. An
// inbound added, renamed, disabled or given another cover domain changes what
// port 443 has to be split into, and none of those actions go through the
// nginx settings page.
type NginxJob struct {
	nginxService service.NginxService
}

func NewNginxJob() *NginxJob {
	return new(NginxJob)
}

func (j *NginxJob) Run() {
	// First, because it can undo everything below: a mode that closed the
	// ports and was never confirmed has to be rolled back before the reconcile
	// dutifully puts it back in place.
	j.nginxService.CheckConfirmation()
	j.nginxService.Reconcile()
}
