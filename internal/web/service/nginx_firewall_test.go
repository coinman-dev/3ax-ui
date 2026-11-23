package service

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/nginx"
)

// only443 is the settings shape stage 3 applies.
func only443(firewall bool) NginxSettings {
	return NginxSettings{
		Mode: string(nginx.ModeOnly443), Domain: "vpn.example.com",
		RealityPort: 8443, ManageFirewall: firewall,
	}
}

// TestOnly443LeavesNothingOnItsOwnPort: dual needs the other ports open; in this mode an
// inbound left in place would advertise a port the firewall blocks.
func TestOnly443LeavesNothingOnItsOwnPort(t *testing.T) {
	svc := newNginxTestServer(t)
	seedInbounds(t)

	shared, _ := svc.collectRoutes(NginxSettings{Mode: string(nginx.ModeShared), RealityPort: 8443})
	if !slices.ContainsFunc(shared, func(r NginxRoute) bool { return r.Dual }) {
		t.Fatal("dual mode has stopped producing dual routes, so this test proves nothing")
	}

	routes, _ := svc.collectRoutes(only443(true))
	if len(routes) == 0 {
		t.Fatal("nothing was routed at all")
	}
	for _, r := range routes {
		if r.Dual {
			t.Errorf("«%s» is still dual with the other ports closed", r.Remark)
		}
		if r.Listen != "127.0.0.1" {
			t.Errorf("«%s» listens on %q, want the loopback", r.Remark, r.Listen)
		}
	}
}

// TestFirewallKeepsThePanelReachable: whatever is not published behind the public port must
// stay open where it is, or the mode becomes a lockout.
func TestFirewallKeepsThePanelReachable(t *testing.T) {
	svc := newNginxTestServer(t)
	seedInbounds(t)
	set := SettingService{}
	for key, value := range map[string]string{
		"webPort": "2053", "subEnable": "true", "subPort": "2096",
	} {
		if err := set.setString(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	fw, _ := svc.firewallPlan(only443(true))
	for _, port := range []int{PublicPort, 2053, 2096} {
		if !slices.Contains(fw.TCP, nginx.Port(port)) {
			t.Errorf("port %d was closed; open TCP ports are %v", port, fw.TCP)
		}
	}

	// Published behind 443, both of them lose the need for a port of their own.
	behind := only443(true)
	behind.PanelBehind443, behind.SubsBehind443 = true, true
	fw, _ = svc.firewallPlan(behind)
	for _, port := range []int{2053, 2096} {
		if slices.Contains(fw.TCP, nginx.Port(port)) {
			t.Errorf("port %d stayed open although it is published behind %d", port, PublicPort)
		}
	}
	if !slices.Contains(fw.TCP, nginx.Port(PublicPort)) {
		t.Errorf("the public port itself was closed; open TCP ports are %v", fw.TCP)
	}
}

// TestFirewallNamesWhatItCutsOff: an inbound without an SNI loses its port, so the plan names
// it; a UDP tunnel never spoke TCP, so it is not listed and keeps its port.
func TestFirewallNamesWhatItCutsOff(t *testing.T) {
	svc := newNginxTestServer(t)
	_, _ = seedInbounds(t)
	db := database.GetDB()
	ss := &model.Inbound{
		UserId: 1, Remark: "ss-old", Enable: true, Port: 8388,
		Protocol: model.Shadowsocks, Tag: "inbound-8388", Settings: `{}`,
	}
	if err := db.Create(ss).Error; err != nil {
		t.Fatalf("create shadowsocks: %v", err)
	}

	fw, cut := svc.firewallPlan(only443(true))

	var closed []string
	for _, c := range cut {
		if c.Kind != "closed" {
			t.Errorf("unexpected change kind %q in the firewall plan", c.Kind)
		}
		closed = append(closed, c.Subject)
	}
	if !slices.Contains(closed, "ss-old") {
		t.Errorf("the inbound that loses its port was not named; plan says %v", closed)
	}
	if slices.Contains(closed, "awg") {
		t.Error("a UDP tunnel was reported as losing its port, which it does not")
	}
	if !slices.Contains(fw.UDP, nginx.Port(55200)) {
		t.Errorf("the tunnel's UDP port was closed; open UDP ports are %v", fw.UDP)
	}
	if slices.Contains(fw.TCP, nginx.Port(8388)) {
		t.Errorf("the port the plan promised to close stayed open: %v", fw.TCP)
	}
	// Nor the UDP half of it: that would be a port a scanner can find, in
	// exchange for an inbound that is broken anyway.
	if slices.Contains(fw.UDP, nginx.Port(8388)) {
		t.Errorf("the UDP half of a closed TCP inbound stayed open: %v", fw.UDP)
	}
}

// TestOperatorsOwnPortsStayOpen: the panel cannot know about a mail relay, monitoring agent or
// resolver, so the operator's extra ports are trusted.
func TestOperatorsOwnPortsStayOpen(t *testing.T) {
	svc := newNginxTestServer(t)
	seedInbounds(t)

	set := only443(true)
	set.FirewallExtra = "53, 9000-9100, nonsense"
	fw, _ := svc.firewallPlan(set)

	for _, want := range []nginx.PortRange{nginx.Port(53), {From: 9000, To: 9100}} {
		// Both protocols: «53» almost always means both, and a UDP port with
		// nothing behind it costs nothing.
		if !slices.Contains(fw.TCP, want) {
			t.Errorf("%v is closed over TCP; open are %v", want, fw.TCP)
		}
		if !slices.Contains(fw.UDP, want) {
			t.Errorf("%v is closed over UDP; open are %v", want, fw.UDP)
		}
	}
}

// TestPlanSaysWhatStaysOpen: SSH must be listed even though firewallPlan does not add it, or
// the plan looks like it takes away the way back in.
func TestPlanSaysWhatStaysOpen(t *testing.T) {
	svc := newNginxTestServer(t)
	seedInbounds(t)

	var kept string
	for _, c := range svc.Plan(only443(true)).Changes {
		if c.Kind == "kept" {
			kept = c.From
		}
	}
	if kept == "" {
		t.Fatal("the plan never says which ports stay open")
	}
	for _, want := range []string{"443/tcp", "55200/udp"} {
		if !strings.Contains(kept, want) {
			t.Errorf("%q is not in %q", want, kept)
		}
	}
	// Whatever sshd is on, it has to be in there.
	ssh := nginx.SSHPorts()
	if !strings.Contains(kept, strconv.Itoa(ssh[0])+"/tcp") {
		t.Errorf("SSH (port %d) is not named in %q", ssh[0], kept)
	}
}

// TestUnreadablePortsBlockTheApply: the parser drops what it cannot read, so «53 8080» (a space
// for a comma) would close both ports the operator believed kept.
func TestUnreadablePortsBlockTheApply(t *testing.T) {
	svc := newNginxTestServer(t)
	seedInbounds(t)

	blockers := func(extra string) []string {
		set := only443(true)
		set.FirewallExtra = extra
		var codes []string
		for _, b := range svc.Plan(set).Blockers {
			if b.Code == "firewallExtraUnreadable" {
				codes = append(codes, strings.Join(b.Params, "|"))
			}
		}
		return codes
	}

	if got := blockers("53, 8080, 9000-9100"); len(got) != 0 {
		t.Errorf("a list the parser understands was rejected: %v", got)
	}
	if got := blockers(""); len(got) != 0 {
		t.Errorf("an empty list was rejected: %v", got)
	}

	got := blockers("53 8080, 9000-9100")
	if len(got) != 1 {
		t.Fatalf("a space where a comma belonged was not caught: %v", got)
	}
	if got[0] != "53 8080" {
		t.Errorf("the blocker names %q, want the token it could not read", got[0])
	}
}

// Inbounds of a remote node run on another host: the local nginx cannot route
// them, and closing local ports does not cut them off.
func TestNodeInboundsStayOutOfTheFrontEnd(t *testing.T) {
	svc := newNginxTestServer(t)
	_, _ = seedInbounds(t)
	nodeID := 7
	db := database.GetDB()
	for _, ib := range []*model.Inbound{
		{
			UserId: 1, Remark: "node-reality", Enable: true, Port: 443, NodeID: &nodeID,
			Protocol: model.VLESS, Tag: "node-443", Settings: `{"clients":[]}`, StreamSettings: axRealityStream,
		},
		{
			UserId: 1, Remark: "node-ss", Enable: true, Port: 8388, NodeID: &nodeID,
			Protocol: model.Shadowsocks, Tag: "node-8388", Settings: `{}`,
		},
	} {
		if err := db.Create(ib).Error; err != nil {
			t.Fatalf("create %s: %v", ib.Remark, err)
		}
	}
	routes, _ := svc.collectRoutes(only443(true))
	for _, r := range routes {
		if r.Remark == "node-reality" {
			t.Error("a node's inbound was routed by the local nginx")
		}
	}
	_, cut := svc.firewallPlan(only443(true))
	for _, c := range cut {
		if c.Subject == "node-ss" {
			t.Error("a node's inbound was reported as cut off by the local firewall")
		}
	}
}

// Let's Encrypt checks a domain over port 80, in webroot and standalone mode alike; with 80
// dropped the certificate the front-end serves stopped renewing and expired (net-ru, 2026-11).
func TestOnly443KeepsPort80ForCertificateRenewal(t *testing.T) {
	svc := newNginxTestServer(t)
	seedInbounds(t)
	set := only443(true)
	set.PanelBehind443, set.SubsBehind443 = true, true
	fw, _ := svc.firewallPlan(set)
	if !slices.Contains(fw.TCP, nginx.Port(80)) {
		t.Errorf("only443 keeps %v/tcp open, want port 80 among them", fw.TCP)
	}
	if slices.Contains(fw.UDP, nginx.Port(80)) {
		t.Errorf("port 80 opened for UDP too: %v", fw.UDP)
	}
}
