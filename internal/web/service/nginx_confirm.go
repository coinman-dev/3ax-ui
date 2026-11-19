package service

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/nginx"
)

// rollBack is the test seam; nothing else may put the server back, since that restarts Xray
// and rewrites /etc/nginx, which no unit test may do.
var rollBack = func(s *NginxService, to NginxSettings) error { return s.Apply(to) }

// ConfirmWindow is how long the panel waits to hear it is still reachable before rolling back:
// time to reload at a new address, short enough that a locked-out operator isn't left wondering.
const ConfirmWindow = 2 * time.Minute

// closesPorts reports whether these settings shut anything. Only the firewall does: the panel
// behind the public port with every port open adds a way in and removes none.
func closesPorts(set NginxSettings) bool {
	return nginx.Mode(set.Mode) == nginx.ModeOnly443 && set.ManageFirewall
}

// armsConfirmation reports whether the change could lock the operator out: the firewall coming
// on, or the panel's own port vanishing behind 443 while it is on.
func armsConfirmation(from, to NginxSettings) bool {
	if !closesPorts(to) {
		return false
	}
	return !closesPorts(from) || (to.PanelBehind443 && !from.PanelBehind443)
}

// armConfirmation persists the fallback and deadline: a panel restart before the deadline is one
// way this goes wrong, and an in-memory timer dying with it would leave the server closed.
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

// CheckConfirmation rolls back if nobody confirmed in time. Run from the job, so it happens
// whether or not anyone still has the panel open.
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

	// Cleared first, so a failing rollback is logged once instead of retried every tick forever.
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
