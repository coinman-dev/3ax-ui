package service

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/logger"
	"github.com/coinman-dev/3ax-ui/v2/nginx"
	"github.com/coinman-dev/3ax-ui/v2/shared/portfwd"
)

// inboundSnapshot is what an inbound looked like before the front-end took it
// over. It is stored in the settings table rather than kept in memory because
// putting the inbounds back is a separate action, taken at some other time,
// possibly after a restart — and a listen address the operator chose by hand
// must survive the round trip.
//
// It records the individual fields the front-end changes, not the settings
// documents as a whole. Restoring a whole document would undo every edit made
// to the inbound while it sat behind nginx — new clients, a changed cover
// domain — which is not what "switch the front-end off" is supposed to mean.
type inboundSnapshot struct {
	Id         int    `json:"id"`
	Listen     string `json:"listen"`
	Port       int    `json:"port"`
	PublicPort int    `json:"publicPort"`
	// Whether the inbound already expected a PROXY protocol header before the
	// front-end turned it on — the operator may have had their own proxy in
	// front of it.
	ProxyProtocol bool `json:"proxyProtocol"`
}

// applyMu serialises everything that moves inbounds and rewrites /etc/nginx.
//
// Two callers reach Apply: the operator pressing the button, and the reconcile
// job every half minute, which cannot see that an apply is already in flight.
// Left to overlap they stage the same files with separate rollback snapshots,
// so one rolling back can put the other's half-written config back in place.
// NginxService is a zero value copied into the job and into the controller, so
// the lock has to belong to the package rather than to the receiver.
var applyMu sync.Mutex

// Apply moves the server to the requested mode, or leaves it exactly as it was.
//
// The order is dictated by port 443: the inbound that owns it has to let go
// before nginx can take it, and nothing may move until the generated config is
// known to be one nginx accepts.
func (s *NginxService) Apply(in NginxSettings) error {
	if !nginx.Mode(in.Mode).Valid() {
		return fmt.Errorf("unknown mode %q", in.Mode)
	}
	applyMu.Lock()
	defer applyMu.Unlock()

	previous := s.GetSettings()
	if in.Mode == string(nginx.ModeOff) {
		return s.disable(in)
	}
	if !nginx.IsInstalled() {
		return fmt.Errorf("nginx is not installed on this server")
	}

	cfg, err := s.buildConfig(in)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	if err := s.stubService.SyncToDisk(); err != nil {
		logger.Warning("nginx: could not write the cover page:", err)
	}

	// 1. Write and verify the config. Nothing is live and nothing has moved,
	//    so a config nginx refuses costs nothing.
	staged, err := nginx.Stage(cfg)
	if err != nil {
		return err
	}

	// 2. Move the inbounds off the public port and onto the loopback.
	if _, err := s.relocateInbounds(in, false); err != nil {
		staged.Rollback()
		return err
	}

	// 3. Restart Xray so it lets go of 443 before nginx reaches for it.
	if err := s.xrayService.RestartXray(true); err != nil {
		s.restoreInbounds()
		_ = s.xrayService.RestartXray(true)
		staged.Rollback()
		return fmt.Errorf("restart xray: %w", err)
	}

	// 4. Hand 443 to nginx.
	if err := staged.Activate(); err != nil {
		s.restoreInbounds()
		_ = s.xrayService.RestartXray(true)
		return err
	}

	if err := s.SaveSettings(in); err != nil {
		return err
	}

	// 5. Arm the rollback before anything closes. Written down rather than
	//    held in memory: the panel may be restarted between here and the
	//    deadline, and a timer that died with the process would leave the
	//    server shut for good.
	if armsConfirmation(previous, in) {
		if err := s.armConfirmation(previous); err != nil {
			return err
		}
	}

	// 6. The firewall goes last, once everything it has to leave reachable is
	//    already answering. It is also the one step that does not undo the
	//    rest when it fails: the front-end is up and working, the ports are
	//    merely still open, and tearing a working server down over that would
	//    be the worse outcome. The settings are saved first on purpose, so the
	//    reconcile job picks the retry up on its next tick.
	return s.applyFirewall(in)
}

// applyFirewall closes the ports the mode asks to close, or opens them all
// again if it does not.
func (s *NginxService) applyFirewall(in NginxSettings) error {
	if in.Mode != string(nginx.ModeOnly443) || !in.ManageFirewall {
		return nginx.RemoveFirewall()
	}
	fw, _ := s.firewallPlan(in)
	if err := nginx.ApplyFirewall(fw); err != nil {
		logger.Warning("nginx: the ports could not be closed:", err)
		return fmt.Errorf("the front-end is up, but the ports could not be closed: %w", err)
	}
	logger.Infof("nginx: only %v/tcp and %v/udp are open now, plus ssh on %v", fw.TCP, fw.UDP, nginx.SSHPorts())
	return nil
}

// disable puts the inbounds back on their own ports and removes the generated
// config. It runs in the opposite order to Apply for the same reason: nginx has
// to let go of 443 before Xray asks for it.
func (s *NginxService) disable(in NginxSettings) error {
	// The ports come back first. Everything below can fail; the operator
	// asking for the front-end to go away must not be left with a closed
	// server because it did.
	if err := nginx.RemoveFirewall(); err != nil {
		return err
	}
	if err := nginx.Remove(); err != nil {
		return err
	}
	if err := s.restoreInbounds(); err != nil {
		return err
	}
	// nginx reloads gracefully, so its old workers hold the public port for a
	// moment after the config is gone. Xray moving back onto that port meets
	// "address already in use", dies, and only comes back because its own
	// supervisor tries again a couple of seconds later.
	if !nginx.WaitPortReleased(PublicPort, 10*time.Second) {
		logger.Warningf("nginx: port %d is still held after the config was removed, restarting xray anyway", PublicPort)
	}
	if err := s.xrayService.RestartXray(true); err != nil {
		return fmt.Errorf("restart xray: %w", err)
	}
	in.Mode = string(nginx.ModeOff)
	in.SubsBehind443 = false
	in.PanelBehind443 = false
	in.ManageFirewall = false
	// Nothing is closed any more, so there is nothing left to confirm.
	if err := s.clearConfirmation(); err != nil {
		logger.Warning("nginx: could not clear the pending confirmation:", err)
	}
	return s.SaveSettings(in)
}

// relocateInbounds moves every routed inbound onto the loopback, tells it to
// expect the PROXY protocol header nginx will prepend, and records the port its
// links must keep advertising.
//
// The tag is deliberately left alone. It is derived from listen/port everywhere
// else in the panel, but routing rules and the Telegram bot refer to inbounds by
// tag, and renaming one here would quietly detach every rule pointing at it.
func (s *NginxService) relocateInbounds(in NginxSettings, dryRun bool) (bool, error) {
	routes, _ := s.collectRoutes(in)
	if len(routes) == 0 {
		return false, fmt.Errorf("no inbound can be routed by server name — enable a VLESS Reality or MTProto inbound first")
	}
	moved := false

	// Keep whatever was recorded by an earlier apply: an inbound moved once
	// must still remember the port it started on.
	snapshots := s.loadSnapshots()
	known := map[int]bool{}
	for _, snap := range snapshots {
		known[snap.Id] = true
	}

	db := database.GetDB()
	for _, r := range routes {
		// An inbound that kept its own port is not moved at all: it goes on
		// serving the clients it already has, and simply gains a second way
		// in through the public port.
		if r.Dual {
			// If this inbound was relocated in a previous mode (e.g. only443),
			// restore its original listen address, port, and proxy protocol so it
			// can answer directly on its own port again.
			for idx, snap := range snapshots {
				if snap.Id == r.InboundId {
					ib, err := inboundByID(r.InboundId)
					if err != nil {
						break
					}
					if !dryRun {
						updates := map[string]any{
							"listen":      snap.Listen,
							"port":        snap.Port,
							"public_port": snap.PublicPort,
						}
						if err := setProxyProtocol(ib, updates, snap.ProxyProtocol); err != nil {
							return moved, fmt.Errorf("restore «%s»: %w", ib.Remark, err)
						}
						if err := db.Model(model.Inbound{}).Where("id = ?", ib.Id).Updates(updates).Error; err != nil {
							return moved, fmt.Errorf("restore «%s»: %w", ib.Remark, err)
						}
						moved = true
						logger.Infof("nginx: dual inbound %q restored to %s:%d", ib.Remark, snap.Listen, snap.Port)
					} else {
						moved = true
					}
					snapshots = append(snapshots[:idx], snapshots[idx+1:]...)
					break
				}
			}
			continue
		}
		ib, err := inboundByID(r.InboundId)
		if err != nil {
			return moved, fmt.Errorf("read inbound %d: %w", r.InboundId, err)
		}
		if !known[ib.Id] {
			snapshots = append(snapshots, inboundSnapshot{
				Id: ib.Id, Listen: ib.Listen, Port: ib.Port, PublicPort: ib.PublicPort,
				ProxyProtocol: proxyProtocolFlag(ib),
			})
			known[ib.Id] = true
		}

		updates := map[string]any{
			"listen":      "127.0.0.1",
			"port":        r.Port,
			"public_port": PublicPort,
		}
		if r.Port != ib.Port {
			exist, err := s.inboundService.checkPortExist("127.0.0.1", r.Port, ib.Id)
			if err != nil {
				return moved, err
			}
			if exist {
				return moved, fmt.Errorf("port %d is already taken, «%s» cannot move there", r.Port, ib.Remark)
			}
		}

		isReality := ib.Protocol == model.VLESS && realitySNIs(ib.StreamSettings) != nil
		if isReality {
			// Inbounds using REALITY (e.g. VLESS REALITY with xhttp, tcp, grpc)
			// must NEVER have acceptProxyProtocol enabled. The Nginx relay
			// strips the PROXY protocol header before passing bytes to Xray,
			// preventing handshake hangs/timeouts (XTLS/Xray-core Issue #2779).
			if err := setProxyProtocol(ib, updates, false); err != nil {
				return moved, fmt.Errorf("«%s»: %w", ib.Remark, err)
			}
		} else {
			if err := setProxyProtocol(ib, updates, true); err != nil {
				return moved, fmt.Errorf("«%s»: %w", ib.Remark, err)
			}
		}

		expectedPP := !isReality
		if ib.Listen == updates["listen"] && ib.Port == r.Port &&
			ib.PublicPort == PublicPort && proxyProtocolFlag(ib) == expectedPP {
			continue // already where it belongs
		}
		if dryRun {
			return true, nil
		}
		if err := db.Model(model.Inbound{}).Where("id = ?", ib.Id).Updates(updates).Error; err != nil {
			return moved, fmt.Errorf("move «%s»: %w", ib.Remark, err)
		}
		moved = true
		logger.Infof("nginx: inbound %q moved to 127.0.0.1:%d, published on %d", ib.Remark, r.Port, PublicPort)
	}
	if dryRun {
		return moved, nil
	}
	return moved, s.saveSnapshots(snapshots)
}

// restoreInbounds puts every inbound the front-end took over back exactly as it
// was, and forgets the snapshots.
func (s *NginxService) restoreInbounds() error {
	snapshots := s.loadSnapshots()
	if len(snapshots) == 0 {
		return nil
	}
	db := database.GetDB()
	var failures []string
	for _, snap := range snapshots {
		ib, err := inboundByID(snap.Id)
		if err != nil {
			// Deleted while it was behind nginx: nothing left to put back.
			logger.Warningf("nginx: inbound %d is gone, skipping its restore", snap.Id)
			continue
		}
		updates := map[string]any{
			"listen":      snap.Listen,
			"port":        snap.Port,
			"public_port": snap.PublicPort,
		}
		// Leaving this on with nothing in front is exactly how an inbound goes
		// green in the panel and refuses every client on the phone.
		if err := setProxyProtocol(ib, updates, snap.ProxyProtocol); err != nil {
			failures = append(failures, fmt.Sprintf("inbound %d: %v", snap.Id, err))
			continue
		}
		if err := db.Model(model.Inbound{}).Where("id = ?", snap.Id).Updates(updates).Error; err != nil {
			failures = append(failures, fmt.Sprintf("inbound %d: %v", snap.Id, err))
		}
	}
	if len(failures) > 0 {
		// The snapshots stay, so the operator can try again rather than being
		// left with inbounds on the loopback and nothing that remembers where
		// they belong.
		return fmt.Errorf("could not put every inbound back: %s", strings.Join(failures, "; "))
	}
	return s.saveSnapshots(nil)
}

func (s *NginxService) loadSnapshots() []inboundSnapshot {
	raw, err := s.settingService.getString("nginxRelocated")
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil
	}
	var snapshots []inboundSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshots); err != nil {
		logger.Warning("nginx: the saved inbound state is unreadable:", err)
		return nil
	}
	return snapshots
}

func (s *NginxService) saveSnapshots(snapshots []inboundSnapshot) error {
	if len(snapshots) == 0 {
		return s.settingService.setString("nginxRelocated", "")
	}
	raw, err := json.Marshal(snapshots)
	if err != nil {
		return err
	}
	return s.settingService.setString("nginxRelocated", string(raw))
}

// Reconcile keeps the front-end in step with the inbounds without anyone
// pressing anything: an inbound added, renamed, given another cover domain or
// switched off changes what nginx has to route.
//
// It also finishes the job for a newly added inbound, which arrives on its own
// port speaking plain TLS. Leaving it there while nginx sends it PROXY-prefixed
// bytes is the failure that looks like "green in the panel, does not connect on
// the phone", so a new inbound is moved the same way the first ones were.
func (s *NginxService) Reconcile() {
	set := s.GetSettings()
	if !nginx.IsInstalled() {
		s.failures, s.skipTicks = 0, 0
		return
	}

	// Converge in both directions. The mode can go back to off without anyone
	// pressing Apply — a restored backup, a settings reset, an operator editing
	// the database — and leaving nginx in front of 443 while the panel reports
	// it as off means the two disagree about where the inbounds live.
	if set.Mode == string(nginx.ModeOff) {
		s.failures, s.skipTicks = 0, 0
		if stale, err := nginx.NeedsUpdate(nginx.Config{Mode: nginx.ModeOff}); err == nil && stale {
			logger.Info("nginx: the mode is off but the front-end is still up, taking it down")
			// Through Apply rather than straight to disable, so this takes the
			// same lock as the operator pressing the button.
			if err := s.Apply(set); err != nil {
				logger.Error("nginx: could not take the front-end down:", err)
			}
		}
		return
	}

	// Keep the cover page on disk in step with the database. This matters on a
	// panel restored from a backup: the page rides along in the database, and
	// the file nginx serves has to be recreated from it.
	if err := s.stubService.SyncToDisk(); err != nil {
		logger.Warning("nginx reconcile: could not write the cover page:", err)
	}

	// Netfilter rules do not survive a reboot, so the chain can simply be gone
	// while the settings still say the ports are closed — the panel would
	// report a camouflaged server that is in fact wide open. Putting it back
	// touches nothing else, so it does not go through Apply and does not
	// restart Xray to do it.
	if closesPorts(set) && !nginx.FirewallActive() {
		logger.Info("nginx: the firewall chain is gone, putting it back")
		if err := s.applyFirewall(set); err != nil {
			logger.Warning("nginx reconcile:", err)
		}
	}

	// If nginx is not running while a camouflage mode is configured, revive it.
	if !nginx.IsRunning() {
		logger.Warning("nginx reconcile: front-end is enabled but nginx is not running, starting it")
		if err := nginx.Reload(); err != nil {
			logger.Warning("nginx reconcile: could not start nginx:", err)
		}
	}

	// A fresh install switches the front-end on before there is anything to put
	// behind it. That is not a fault to report every half minute — it simply has
	// no work yet, and picks up the first Reality or MTProto inbound the moment
	// one appears.
	if routes, _ := s.collectRoutes(set); len(routes) == 0 {
		return
	}

	cfg, err := s.buildConfig(set)
	if err != nil {
		logger.Warning("nginx reconcile:", err)
		return
	}
	if err := cfg.Validate(); err != nil {
		// A cover domain claimed twice, a certificate that expired: keep the
		// working config rather than replacing it with a broken one.
		logger.Warning("nginx reconcile: the current settings do not make a valid config:", err)
		return
	}

	changed, err := nginx.NeedsUpdate(cfg)
	if err != nil {
		logger.Warning("nginx reconcile:", err)
		return
	}
	needsMove, err := s.relocateInbounds(set, true)
	if err != nil {
		logger.Warning("nginx reconcile:", err)
		return
	}
	if !changed && !needsMove {
		s.failures, s.skipTicks = 0, 0
		return
	}
	if s.skipTicks > 0 {
		s.skipTicks--
		return
	}

	// Apply is the one path that moves inbounds, restarts Xray and hands over
	// the port in the right order — and puts everything back if any step
	// fails. Doing it piecemeal here is how a server ends up with its inbounds
	// on the loopback and nothing listening in front of them.
	if err := s.Apply(set); err != nil {
		s.failures++
		// A server where this cannot succeed would otherwise be retried, and
		// have Xray restarted, twice a minute forever.
		s.skipTicks = min(2*s.failures, 20)
		logger.Errorf("nginx reconcile failed (attempt %d, pausing): %v", s.failures, err)
		return
	}
	s.failures, s.skipTicks = 0, 0
}

// Plan describes what applying the given settings would do, so the panel can
// show it and get a yes before anything moves. Links that change are the part
// that cannot be undone for the clients, so they are spelled out by name.
func (s *NginxService) Plan(in NginxSettings) NginxPlan {
	plan := NginxPlan{Mode: in.Mode}

	if in.Mode == string(nginx.ModeOff) {
		for _, snap := range s.loadSnapshots() {
			ib, err := inboundByID(snap.Id)
			if err != nil {
				continue
			}
			plan.Changes = append(plan.Changes, NginxChange{
				Kind: "move", Subject: ib.Remark,
				From: fmt.Sprintf("127.0.0.1:%d", ib.Port),
				To:   listenLabel(snap.Listen, snap.Port),
			})
			plan.Changes = append(plan.Changes, NginxChange{
				Kind: "links", Subject: ib.Remark,
				From: strconv.Itoa(PublicPort), To: strconv.Itoa(snap.Port),
			})
		}
		if from, to, moved := s.subAddressChange(in); moved {
			plan.Changes = append(plan.Changes, NginxChange{Kind: "subs", From: from, To: to})
		}
		return plan
	}

	if !nginx.IsInstalled() {
		plan.Blockers = append(plan.Blockers, warn("notInstalled"))
	} else if !nginx.HasStream() {
		plan.Blockers = append(plan.Blockers, warn("noStreamModule"))
	}

	routes, warnings := s.collectRoutes(in)
	plan.Blockers = append(plan.Blockers, warnings...)
	if len(routes) == 0 {
		plan.Blockers = append(plan.Blockers, warn("nothingToRoute"))
	}

	for _, r := range routes {
		ib, err := inboundByID(r.InboundId)
		if err != nil {
			continue
		}
		if r.Dual {
			// Nothing moves and no link stops working — the inbound simply
			// answers on two ports from now on.
			plan.Changes = append(plan.Changes, NginxChange{
				Kind: "dual", Subject: r.Remark,
				From: strconv.Itoa(ib.Port), To: strconv.Itoa(PublicPort),
			})
			continue
		}
		if ib.Listen != r.Listen || ib.Port != r.Port {
			if r.Port != ib.Port {
				exist, err := s.inboundService.checkPortExist("127.0.0.1", r.Port, ib.Id)
				if err == nil && exist {
					plan.Blockers = append(plan.Blockers, NginxWarning{
						Code:   "portConflict",
						Params: []string{strconv.Itoa(r.Port), r.Remark},
					})
				}
			}
			plan.Changes = append(plan.Changes, NginxChange{
				Kind: "move", Subject: r.Remark,
				From: listenLabel(ib.Listen, ib.Port),
				To:   fmt.Sprintf("127.0.0.1:%d", r.Port),
			})
		}
		if ib.LinkPort() != PublicPort {
			plan.Changes = append(plan.Changes, NginxChange{
				Kind: "links", Subject: r.Remark,
				From: strconv.Itoa(ib.LinkPort()), To: strconv.Itoa(PublicPort),
			})
		}
	}

	if in.Domain != "" {
		if _, _, _, err := findCertificate(in.Domain); err != nil {
			plan.Blockers = append(plan.Blockers, certWarning(in.Domain, err))
		} else {
			plan.Changes = append(plan.Changes, NginxChange{Kind: "serve", Subject: in.Domain})
		}
	}
	if from, to, moved := s.subAddressChange(in); moved {
		plan.Changes = append(plan.Changes, NginxChange{Kind: "subs", From: from, To: to})
	}
	if in.Mode == string(nginx.ModeOnly443) && in.ManageFirewall {
		if !nginx.FirewallAvailable() {
			plan.Blockers = append(plan.Blockers, warn("noFirewall"))
		}
		if bad := unreadablePorts(in.FirewallExtra); len(bad) > 0 {
			plan.Blockers = append(plan.Blockers, warn("firewallExtraUnreadable", strings.Join(bad, ", ")))
		}
		fw, cut := s.firewallPlan(in)
		plan.Changes = append(plan.Changes, NginxChange{Kind: "kept", From: keptPorts(fw)})
		plan.Changes = append(plan.Changes, cut...)
	}
	if _, err := s.buildConfig(in); err != nil {
		plan.Blockers = append(plan.Blockers, NginxWarning{Code: "configInvalid", Text: err.Error()})
	}
	return plan
}

// subAddressChange reports where the subscription address moves under the given
// settings, and whether it moves at all.
//
// A subscription URL is a link like any other: once handed out it sits on a
// client's phone until it is handed out again. Publishing the subscriptions
// behind the public port changes it, so the plan says so before anything is
// applied — the same courtesy the inbounds already get.
func (s *NginxService) subAddressChange(in NginxSettings) (from string, to string, moved bool) {
	if on, err := s.settingService.GetSubEnable(); err != nil || !on {
		return "", "", false
	}
	label := func(set NginxSettings) string {
		if nginx.Mode(set.Mode) != nginx.ModeOff && set.SubsBehind443 && set.Domain != "" {
			// The public port is 443, and a URL says that by saying nothing.
			return "https://" + set.Domain
		}
		return s.ownSubAddress()
	}
	from = label(s.GetSettings())
	to = label(in)
	return from, to, from != to
}

// firewallPlan is what «only 443» leaves reachable, and what it cuts off to get
// there.
//
// The mode says what it says: afterwards a scanner finds one open TCP port. An
// inbound that cannot be told apart by server name — Shadowsocks, a plain VMess
// — therefore loses its way in, and the operator has to read that list before
// they confirm, not discover it afterwards. The escape hatch is the firewall
// switch itself: mode 3 without it moves the panel behind 443 and leaves every
// port open.
//
// A UDP tunnel keeps its port. Consolidating TCP behind one port gains nothing
// by cutting off something that never spoke TCP, and WireGuard and Hysteria
// cannot hide behind an SNI multiplexer in any case. Anything else loses its
// port outright — leaving the UDP half of a TCP inbound open would be a port a
// scanner can find, in exchange for an inbound that is broken anyway.
func (s *NginxService) firewallPlan(set NginxSettings) (nginx.Firewall, []NginxChange) {
	fw := nginx.Firewall{
		TCP:    nginx.Ports(PublicPort),
		Ifaces: tunnelInterfaces(),
	}

	// Whatever is not published behind the public port has to stay reachable
	// where it is, or applying the mode is the last thing this panel ever does.
	if !set.PanelBehind443 {
		if port, err := s.settingService.GetPort(); err == nil {
			fw.TCP = append(fw.TCP, nginx.Port(port))
		}
	}
	if on, _ := s.settingService.GetSubEnable(); on && !set.SubsBehind443 {
		if port, err := s.settingService.GetSubPort(); err == nil {
			fw.TCP = append(fw.TCP, nginx.Port(port))
		}
	}

	// Whatever else this particular server needs to answer for. The panel
	// cannot know about a mail relay, a monitoring agent or a resolver serving
	// something other than the tunnels, so the operator says so here. Both
	// protocols, because "53" almost always means both and a UDP port with
	// nothing behind it costs nothing.
	for _, spec := range portfwd.Parse(set.FirewallExtra) {
		port := nginx.PortRange{From: spec.Start, To: spec.End}
		fw.TCP = append(fw.TCP, port)
		fw.UDP = append(fw.UDP, port)
	}

	routes, _ := s.collectRoutes(set)
	behind := map[int]bool{}
	for _, r := range routes {
		behind[r.InboundId] = true
	}

	inbounds, err := s.inboundService.GetAllInbounds()
	if err != nil {
		logger.Warning("nginx: cannot read the inbounds to work out the firewall:", err)
		return fw, nil
	}
	var cut []NginxChange
	for _, ib := range inbounds {
		if !ib.Enable || behind[ib.Id] {
			continue
		}
		if udpOnly(ib.Protocol) {
			fw.UDP = append(fw.UDP, nginx.Port(ib.Port))
			continue
		}
		cut = append(cut, NginxChange{
			Kind: "closed", Subject: ib.Remark, From: strconv.Itoa(ib.LinkPort()),
		})
	}
	return fw, cut
}

// keptPorts is the list the operator most wants to read before they confirm:
// what will still answer afterwards. SSH is in it even though firewallPlan does
// not put it there — ApplyFirewall adds it on its own, and leaving it out of the
// list would make the plan look like it takes away the way back in.
//
// Written as "443/tcp", which is notation rather than English and so needs no
// translating.
func keptPorts(fw nginx.Firewall) string {
	var parts []string
	for _, port := range append(nginx.Ports(nginx.SSHPorts()...), fw.TCP...) {
		parts = append(parts, portLabel(port, "tcp"))
	}
	for _, port := range fw.UDP {
		parts = append(parts, portLabel(port, "udp"))
	}
	slices.Sort(parts)
	return strings.Join(slices.Compact(parts), ", ")
}

func portLabel(p nginx.PortRange, proto string) string {
	if p.From == p.To {
		return fmt.Sprintf("%d/%s", p.From, proto)
	}
	return fmt.Sprintf("%d-%d/%s", p.From, p.To, proto)
}

// unreadablePorts names the tokens the port parser threw away.
//
// portfwd.Parse silently drops what it cannot read, which is the right
// behaviour for a free-text port-forwarding field and the wrong one here: a
// port the operator meant to keep open and that was quietly ignored is a port
// this mode closes. «53 8080» — a space where a comma belonged — is the whole
// of it, and nothing else on the page would ever say so.
func unreadablePorts(input string) []string {
	var bad []string
	for _, token := range strings.FieldsFunc(input, func(r rune) bool { return r == ',' || r == ';' }) {
		if token = strings.TrimSpace(token); token == "" {
			continue
		}
		if len(portfwd.Parse(token)) == 0 {
			bad = append(bad, token)
		}
	}
	return bad
}

// tunnelInterfaces names the WireGuard-family interfaces this panel runs.
//
// Traffic arriving on one of them is not judged by the firewall at all: the
// person on the other end authenticated to get there, and the resolver they
// were handed is this very machine. A chain that drops it closes the VPN from
// the inside while the outside looks exactly as intended — the kind of fault
// that gets blamed on anything but a firewall.
func tunnelInterfaces() []string {
	var out []string
	var awg AwgService
	if server, err := awg.GetServer(); err == nil && server.Enable && server.InterfaceName != "" {
		out = append(out, server.InterfaceName)
	}
	var wg WgService
	if server, err := wg.GetServer(); err == nil && server.Enable && server.InterfaceName != "" {
		out = append(out, server.InterfaceName)
	}
	return out
}

// udpOnly is true for the protocols that never listen on TCP at all, so closing
// their TCP port takes nothing away from them.
func udpOnly(p model.Protocol) bool {
	switch p {
	case model.WireGuard, model.AmneziaWG, model.NativeWG:
		return true
	}
	return model.IsHysteria(p)
}

// ownSubAddress is where the subscription server answers for itself.
func (s *NginxService) ownSubAddress() string {
	scheme := "http"
	key, _ := s.settingService.GetSubKeyFile()
	cert, _ := s.settingService.GetSubCertFile()
	if key != "" && cert != "" {
		scheme = "https"
	}
	port, _ := s.settingService.GetSubPort()
	domain, _ := s.settingService.GetSubDomain()
	if domain == "" {
		// The subscription server has no name of its own — the address follows
		// whichever host the visitor typed, so only the port is worth showing.
		domain = "<host>"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, domain, port)
}

func listenLabel(listen string, port int) string {
	if listen == "" {
		listen = "0.0.0.0"
	}
	return fmt.Sprintf("%s:%d", listen, port)
}

// proxyProtocolPath is where each protocol keeps its "expect a PROXY protocol
// header" switch: Xray in the inbound's stream settings, mtg in its own.
func proxyProtocolPath(ib *model.Inbound) (column string, document string, path []string) {
	if ib.Protocol == model.MTProto {
		return "settings", ib.Settings, []string{"proxyProtocolListener"}
	}
	return "stream_settings", ib.StreamSettings, []string{"sockopt", "acceptProxyProtocol"}
}

// proxyProtocolFlag reads the current state of that switch.
func proxyProtocolFlag(ib *model.Inbound) bool {
	_, document, path := proxyProtocolPath(ib)
	return getJSONFlag(document, path...)
}

// setProxyProtocol adds the rewritten settings document to updates.
func setProxyProtocol(ib *model.Inbound, updates map[string]any, value bool) error {
	column, document, path := proxyProtocolPath(ib)
	rewritten, err := setJSONFlag(document, value, path...)
	if err != nil {
		return err
	}
	updates[column] = rewritten
	return nil
}

// getJSONFlag reads a boolean at the given path, treating anything missing or
// of another type as false.
func getJSONFlag(raw string, path ...string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	var node any
	if json.Unmarshal([]byte(raw), &node) != nil {
		return false
	}
	for _, key := range path {
		obj, ok := node.(map[string]any)
		if !ok {
			return false
		}
		node = obj[key]
	}
	flag, _ := node.(bool)
	return flag
}

// setJSONFlag sets a boolean at the given path inside a JSON object, creating
// the intermediate objects, and returns the re-encoded document. Everything it
// does not name is left untouched — these documents carry settings the panel
// does not model.
func setJSONFlag(raw string, value bool, path ...string) (string, error) {
	if len(path) == 0 {
		return raw, fmt.Errorf("no path given")
	}
	doc := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			return raw, fmt.Errorf("unreadable settings: %w", err)
		}
	}
	node := doc
	for _, key := range path[:len(path)-1] {
		child, ok := node[key].(map[string]any)
		if !ok {
			child = map[string]any{}
			node[key] = child
		}
		node = child
	}
	node[path[len(path)-1]] = value

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return raw, err
	}
	return string(out), nil
}
