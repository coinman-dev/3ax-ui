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
	j.nginxService.Reconcile()
}
