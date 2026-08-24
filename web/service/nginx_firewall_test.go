package service

import (
	"slices"
	"testing"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/nginx"
)

// only443 is the settings shape stage 3 applies.
func only443(firewall bool) NginxSettings {
	return NginxSettings{
		Mode: string(nginx.ModeOnly443), Domain: "vpn.example.com",
		RealityPort: 8443, ManageFirewall: firewall,
	}
}

// TestOnly443LeavesNothingOnItsOwnPort: an inbound left where it is would go on
// advertising a link to a port the firewall no longer lets through. Dual mode
// is a property of «other ports are open», and in this mode they are not.
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

// TestFirewallKeepsThePanelReachable is the difference between a mode and a
// lockout. Whatever is not published behind the public port has to stay open
// where it is.
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
		if !slices.Contains(fw.TCP, port) {
			t.Errorf("port %d was closed; open TCP ports are %v", port, fw.TCP)
		}
	}

	// Published behind 443, both of them lose the need for a port of their own.
	behind := only443(true)
	behind.PanelBehind443, behind.SubsBehind443 = true, true
	fw, _ = svc.firewallPlan(behind)
	for _, port := range []int{2053, 2096} {
		if slices.Contains(fw.TCP, port) {
			t.Errorf("port %d stayed open although it is published behind %d", port, PublicPort)
		}
	}
	if !slices.Contains(fw.TCP, PublicPort) {
		t.Errorf("the public port itself was closed; open TCP ports are %v", fw.TCP)
	}
}

// TestFirewallNamesWhatItCutsOff: an inbound nobody can tell apart by server
// name cannot live behind 443, and closing its port takes it away. That has to
// be in the plan the operator reads, not a surprise afterwards.
//
// A UDP tunnel is not in that list and must not be: it never spoke TCP, so
// closing TCP takes nothing from it, and its own port stays open.
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
	if !slices.Contains(fw.UDP, 55200) {
		t.Errorf("the tunnel's UDP port was closed; open UDP ports are %v", fw.UDP)
	}
	if slices.Contains(fw.TCP, 8388) {
		t.Errorf("the port the plan promised to close stayed open: %v", fw.TCP)
	}
}
