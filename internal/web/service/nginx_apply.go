package service

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgkern"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/nginx"
)

// inboundSnapshot is an inbound before the front-end took it over, persisted so a restore after a
// restart works; only the fields it changes, so edits made behind nginx survive the restore.
type inboundSnapshot struct {
	Id         int    `json:"id"`
	Listen     string `json:"listen"`
	Port       int    `json:"port"`
	PublicPort int    `json:"publicPort"`
	// The operator may already have had their own PROXY-protocol proxy in front of the inbound.
	ProxyProtocol bool `json:"proxyProtocol"`
}

// applyMu serialises Apply between the operator and the reconcile job, whose overlapping stages
// can roll back each other's config; package-level since NginxService is copied by value.
var applyMu sync.Mutex

// Apply moves the server to the requested mode, or leaves it exactly as it was. Port 443 sets the
// order: its inbound lets go before nginx takes it; nothing moves until nginx accepts the config.
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
		if rerr := s.restoreInbounds(); rerr != nil {
			logger.Warning("nginx: rollback could not put the inbounds back:", rerr)
		}
		_ = s.xrayService.RestartXray(true)
		staged.Rollback()
		return fmt.Errorf("restart xray: %w", err)
	}

	// 4. Hand 443 to nginx.
	if err := staged.Activate(); err != nil {
		if rerr := s.restoreInbounds(); rerr != nil {
			logger.Warning("nginx: rollback could not put the inbounds back:", rerr)
		}
		_ = s.xrayService.RestartXray(true)
		return err
	}

	if err := s.SaveSettings(in); err != nil {
		return err
	}

	// 5. Arm the rollback before anything closes; persisted, since a timer that died with a
	//    panel restart would leave the server shut for good.
	if armsConfirmation(previous, in) {
		if err := s.armConfirmation(previous); err != nil {
			return err
		}
	}

	// 6. Firewall last, once all it keeps reachable answers. Its failure does not tear down a
	//    working front-end; settings are already saved, so the reconcile job retries it.
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

// disable puts the inbounds back and removes the generated config, in Apply's reverse order:
// nginx has to let go of 443 before Xray asks for it.
func (s *NginxService) disable(in NginxSettings) error {
	// Ports reopen first: if anything below fails, the operator must not be left locked out.
	if err := nginx.RemoveFirewall(); err != nil {
		return err
	}
	if err := nginx.Remove(); err != nil {
		return err
	}
	if err := s.restoreInbounds(); err != nil {
		return err
	}
	// nginx's graceful reload leaves old workers on the port briefly; Xray binding it then dies
	// with "address already in use" and only recovers on its supervisor's retry.
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

// relocateInbounds moves routed inbounds to the loopback behind nginx's PROXY header. Tags are
// kept, though derived from listen/port elsewhere: routing rules and the TG bot match by tag.
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
		// A dual inbound keeps its own port and clients; the public port is a second way in.
		if r.Dual {
			// Relocated by an earlier mode (e.g. only443): restore it to its own port again.
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
			exist, err := s.loopbackPortTaken(ib, r.Port)
			if err != nil {
				return moved, err
			}
			if exist {
				return moved, fmt.Errorf("port %d is already taken, «%s» cannot move there", r.Port, ib.Remark)
			}
		}

		isReality := ib.Protocol == model.VLESS && realitySNIs(ib.StreamSettings) != nil
		if isReality {
			// REALITY must never have acceptProxyProtocol: the nginx relay strips the PROXY
			// header before Xray, avoiding the handshake hangs of XTLS/Xray-core#2779.
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
		// Snapshots stay so a retry still knows where the loopback inbounds belong.
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

// Reconcile keeps nginx in step with inbound edits and moves new inbounds behind it: one left on
// plain TLS while nginx sends it PROXY-prefixed bytes is "green in the panel, dead on the phone".
func (s *NginxService) Reconcile() {
	set := s.GetSettings()
	if !nginx.IsInstalled() {
		s.failures, s.skipTicks = 0, 0
		return
	}

	// Converge both ways: the mode can return to off without Apply (restored backup, settings
	// reset, DB edit), and nginx must not stay on 443 while the panel says off.
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

	// A restored backup carries the cover page only in the DB, so recreate nginx's file from it.
	if err := s.stubService.SyncToDisk(); err != nil {
		logger.Warning("nginx reconcile: could not write the cover page:", err)
	}

	// Netfilter rules don't survive a reboot, leaving a "closed" server wide open. Restoring the
	// chain touches nothing else, so it skips Apply and the Xray restart.
	if closesPorts(set) && !nginx.FirewallActive() {
		logger.Info("nginx: the firewall chain is gone, putting it back")
		if err := s.applyFirewall(set); err != nil {
			logger.Warning("nginx reconcile:", err)
		}
	}

	// nginx can be stopped outside the panel. Restart it only once our config is on disk (else the
	// distro default site lands on :80), and with backoff so a broken nginx isn't retried forever.
	if !nginx.IsRunning() && nginx.ConfigInstalled() {
		if s.skipTicks > 0 {
			s.skipTicks--
			return
		}
		logger.Warning("nginx reconcile: the front-end is on but nginx is not running, starting it")
		if err := nginx.Reload(); err != nil {
			s.failures++
			s.skipTicks = min(2*s.failures, 20)
			logger.Errorf("nginx reconcile: could not start nginx (attempt %d, pausing): %v", s.failures, err)
			return
		}
		s.failures, s.skipTicks = 0, 0
	}

	// A fresh install enables the front-end before any routable inbound exists: not a fault to
	// report, just no work until the first Reality or MTProto inbound appears.
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

	// Only Apply moves inbounds, restarts Xray and hands over the port in order, with rollback;
	// doing it piecemeal here can leave inbounds on the loopback with nothing in front.
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

// Plan describes what applying the settings would do, for a yes before anything moves. Changed
// links are spelled out by name: for clients they cannot be undone.
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
				exist, err := s.loopbackPortTaken(ib, r.Port)
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

// subAddressChange reports whether and where the subscription URL moves: once handed out it
// stays on clients' phones until re-shared, so the plan names the change before applying.
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

// firewallPlan is what «only 443» leaves reachable and what it cuts off (non-SNI inbounds), shown
// before confirming; turning the firewall switch off keeps every port open instead.
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

	// Operator extras the panel can't know of (mail relay, monitoring, resolver). Both protocols:
	// "53" almost always means both, and an idle UDP port costs nothing.
	for _, port := range nginx.ParsePorts(set.FirewallExtra) {
		fw.TCP = append(fw.TCP, port)
		fw.UDP = append(fw.UDP, port)
	}

	routes, _ := s.collectRoutes(set)
	behind := map[int]bool{}
	for _, r := range routes {
		behind[r.InboundId] = true
	}

	inbounds, err := s.localInbounds()
	if err != nil {
		logger.Warning("nginx: cannot read the inbounds to work out the firewall:", err)
		return fw, nil
	}
	var cut []NginxChange
	for _, ib := range inbounds {
		if !ib.Enable || behind[ib.Id] {
			continue
		}
		// UDP tunnels keep their port: they can't sit behind an SNI mux. Others lose it, as the
		// open UDP half of a TCP inbound is a port scanners find, serving a broken inbound.
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

// keptPorts lists what still answers afterwards, SSH included: ApplyFirewall adds it on its own,
// and leaving it out would make the plan look like it removes the way back in.
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

// unreadablePorts names tokens nginx.ParsePorts silently drops; here a dropped port is one this
// mode closes, e.g. «53 8080» with a space where a comma belonged.
func unreadablePorts(input string) []string {
	var bad []string
	for _, token := range strings.FieldsFunc(input, func(r rune) bool { return r == ',' || r == ';' }) {
		if token = strings.TrimSpace(token); token == "" {
			continue
		}
		if len(nginx.ParsePorts(token)) == 0 {
			bad = append(bad, token)
		}
	}
	return bad
}

// tunnelInterfaces names the panel's WireGuard-family interfaces, which the firewall exempts:
// peers there authenticated and resolve via this host, so a drop breaks the VPN from inside.
func tunnelInterfaces() []string {
	var out []string
	for _, id := range amneziawgkern.GetManager().ManagedIDs() {
		out = append(out, amneziawgkern.InterfaceName(id))
	}
	return out
}

// udpOnly is true for the protocols that never listen on TCP at all, so closing
// their TCP port takes nothing away from them.
func udpOnly(p model.Protocol) bool {
	switch p {
	case model.WireGuard, model.AmneziaWG, model.Hysteria, model.TUIC:
		return true
	}
	return false
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

// setJSONFlag sets a boolean at path, creating intermediate objects, and re-encodes the document
// with everything else intact: these documents carry settings the panel does not model.
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
