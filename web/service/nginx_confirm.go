package service

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/coinman-dev/3ax-ui/v2/logger"
	"github.com/coinman-dev/3ax-ui/v2/nginx"
)

// rollBack is the seam the tests replace. Nothing else here may put the server
// back, because doing so restarts Xray and rewrites /etc/nginx — not things a
// unit test can be allowed to do.
var rollBack = func(s *NginxService, to NginxSettings) error { return s.Apply(to) }

// ConfirmWindow is how long the panel waits to be told it is still reachable
// before putting the server back the way it was.
//
// Two minutes is long enough to reload a page at a new address and short enough
// that an operator who has locked themselves out is not sitting there wondering
// whether anything is going to happen.
const ConfirmWindow = 2 * time.Minute

// closesPorts reports whether these settings shut anything.
//
// Publishing the panel behind the public port is not, by itself, a risk: with
// every port still open it adds a second way in and takes none away. Only the
// firewall takes something away.
func closesPorts(set NginxSettings) bool {
	return nginx.Mode(set.Mode) == nginx.ModeOnly443 && set.ManageFirewall
}

// armsConfirmation reports whether going from one state to the other could
// leave the operator unable to reach their own server — the firewall coming on
// for the first time, or the panel's own port disappearing behind 443 while it
// is on.
func armsConfirmation(from, to NginxSettings) bool {
	if !closesPorts(to) {
		return false
	}
	return !closesPorts(from) || (to.PanelBehind443 && !from.PanelBehind443)
}

// armConfirmation writes down where to go back to and when to stop waiting.
//
// It is stored rather than held in memory on purpose: the panel may well be
// restarted between the change and the deadline — that is one of the ways this
// goes wrong — and a timer that dies with the process would leave the server
// closed for good.
func (s *NginxService) armConfirmation(fallback NginxSettings) error {
	raw, err := json.Marshal(fallback)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(ConfirmWindow).UnixMilli()
	if err := s.settingService.setString("nginxConfirmFallback", string(raw)); err != nil {
		return err
	}
	if err := s.settingService.setString("nginxConfirmDeadline", strconv.FormatInt(deadline, 10)); err != nil {
		return err
	}
	logger.Infof("nginx: ports are closing — waiting %s to be told the panel is still reachable", ConfirmWindow)
	return nil
}

// PendingConfirmation is the deadline in unix milliseconds, or zero when
// nothing is waiting to be confirmed.
func (s *NginxService) PendingConfirmation() int64 {
	raw, err := s.settingService.getString("nginxConfirmDeadline")
	if err != nil {
		return 0
	}
	deadline, _ := strconv.ParseInt(raw, 10, 64)
	return deadline
}

// Confirm is the panel saying it got through. It can only be called from a
// session that reached the panel, which is the whole proof required.
func (s *NginxService) Confirm() error {
	if s.PendingConfirmation() == 0 {
		return nil
	}
	logger.Info("nginx: the panel was reached after the ports closed, keeping the new settings")
	return s.clearConfirmation()
}

func (s *NginxService) clearConfirmation() error {
	if err := s.settingService.setString("nginxConfirmDeadline", "0"); err != nil {
		return err
	}
	return s.settingService.setString("nginxConfirmFallback", "")
}

// CheckConfirmation puts the server back if nobody came to say it still works.
// It is called from the job, so the rollback happens whether or not anyone is
// still looking at the panel.
func (s *NginxService) CheckConfirmation() {
	deadline := s.PendingConfirmation()
	if deadline == 0 || time.Now().UnixMilli() < deadline {
		return
	}

	raw, err := s.settingService.getString("nginxConfirmFallback")
	if err != nil || raw == "" {
		logger.Warning("nginx: the confirmation ran out but there is nothing recorded to go back to")
		_ = s.clearConfirmation()
		return
	}
	var fallback NginxSettings
	if err := json.Unmarshal([]byte(raw), &fallback); err != nil {
		logger.Warning("nginx: cannot read what to go back to:", err)
		_ = s.clearConfirmation()
		return
	}

	// Cleared first: a rollback that fails must not be retried on every tick
	// forever, and whatever went wrong is worth reading in the log rather than
	// watching scroll past.
	if err := s.clearConfirmation(); err != nil {
		logger.Warning("nginx: could not clear the pending confirmation:", err)
	}
	logger.Warningf("nginx: nobody confirmed within %s, opening the ports again", ConfirmWindow)
	if err := rollBack(s, fallback); err != nil {
		// The ports are the part that locks people out, so try that on its own
		// even if putting the rest back did not work.
		logger.Error("nginx: could not roll back:", err)
		if err := nginx.RemoveFirewall(); err != nil {
			logger.Error("nginx: could not open the ports either:", err)
		}
	}
}
