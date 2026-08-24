package service

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/logger"
	"github.com/coinman-dev/3ax-ui/v2/nginx"
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

// Apply moves the server to the requested mode, or leaves it exactly as it was.
//
// The order is dictated by port 443: the inbound that owns it has to let go
// before nginx can take it, and nothing may move until the generated config is
// known to be one nginx accepts.
func (s *NginxService) Apply(in NginxSettings) error {
	if !nginx.Mode(in.Mode).Valid() {
		return fmt.Errorf("unknown mode %q", in.Mode)
	}
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

	return s.SaveSettings(in)
}

// disable puts the inbounds back on their own ports and removes the generated
// config. It runs in the opposite order to Apply for the same reason: nginx has
// to let go of 443 before Xray asks for it.
func (s *NginxService) disable(in NginxSettings) error {
	if err := nginx.Remove(); err != nil {
		return err
	}
	if err := s.restoreInbounds(); err != nil {
		return err
	}
	if err := s.xrayService.RestartXray(true); err != nil {
		return fmt.Errorf("restart xray: %w", err)
	}
	in.Mode = string(nginx.ModeOff)
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

		if err := setProxyProtocol(ib, updates, true); err != nil {
			return moved, fmt.Errorf("«%s»: %w", ib.Remark, err)
		}

		if ib.Listen == updates["listen"] && ib.Port == r.Port &&
			ib.PublicPort == PublicPort && proxyProtocolFlag(ib) {
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
			if err := s.disable(set); err != nil {
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
		return plan
	}

	if !nginx.IsInstalled() {
		plan.Blockers = append(plan.Blockers, "nginx is not installed on this server")
	} else if !nginx.HasStream() {
		plan.Blockers = append(plan.Blockers, "this nginx was built without the stream module")
	}

	routes, warnings := s.collectRoutes(in)
	plan.Blockers = append(plan.Blockers, warnings...)
	if len(routes) == 0 {
		plan.Blockers = append(plan.Blockers,
			"no inbound can be routed by server name — a VLESS Reality or MTProto inbound is needed")
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
			plan.Blockers = append(plan.Blockers, err.Error())
		} else {
			plan.Changes = append(plan.Changes, NginxChange{Kind: "serve", Subject: in.Domain})
		}
	}
	if _, err := s.buildConfig(in); err != nil {
		plan.Blockers = append(plan.Blockers, err.Error())
	}
	return plan
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
