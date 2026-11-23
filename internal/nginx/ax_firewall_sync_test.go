package nginx

import (
	"slices"
	"testing"
)

// The chain was rebuilt only when the mode was applied or the chain had vanished, so an inbound
// that moved port (a 1.x WireGuard server migrated from 17269 to 51821) stayed dropped for good.
func TestSyncFirewallFollowsWhatMustStayOpen(t *testing.T) {
	sshConfig(t, map[string]string{"sshd_config": "Port 22\n"})
	f := &fakeTables{}
	f.install(t)
	hooked := func() {
		f.has["iptables -C INPUT -j "+Chain] = true
		f.has["ip6tables -C INPUT -j "+Chain] = true
	}

	before := Firewall{TCP: Ports(443), UDP: Ports(17269, 55200), Ifaces: []string{"awg0"}}
	if err := ApplyFirewall(before); err != nil {
		t.Fatalf("ApplyFirewall: %v", err)
	}
	hooked()

	f.ran = nil
	if changed, err := SyncFirewall(Firewall{TCP: Ports(443), UDP: Ports(55200, 17269), Ifaces: []string{"awg0"}}); err != nil || changed {
		t.Fatalf("same plan in another order: changed=%v err=%v, want no rebuild", changed, err)
	}
	if len(f.added("iptables")) != 0 {
		t.Fatalf("an unchanged plan rebuilt the chain: %v", f.ran)
	}

	after := Firewall{TCP: Ports(443), UDP: Ports(51821, 55200), Ifaces: []string{"xui-awg4", "xui-awg3"}}
	changed, err := SyncFirewall(after)
	if err != nil || !changed {
		t.Fatalf("moved port: changed=%v err=%v, want a rebuild", changed, err)
	}
	v4 := f.added("iptables")
	for _, want := range []string{"-p udp --dport 51821 -j RETURN", "-i xui-awg3 -j RETURN", "-i xui-awg4 -j RETURN"} {
		if !slices.Contains(v4, want) {
			t.Errorf("rebuilt chain lacks %q: %v", want, v4)
		}
	}
	if slices.Contains(v4, "-p udp --dport 17269 -j RETURN") || slices.Contains(v4, "-i awg0 -j RETURN") {
		t.Errorf("rebuilt chain still lets the old port or interface through: %v", v4)
	}

	f.ran = nil
	if changed, err := SyncFirewall(after); err != nil || changed {
		t.Errorf("second sync of the same plan: changed=%v err=%v", changed, err)
	}

	// A reboot empties netfilter; the plan is unchanged but the chain must come back.
	delete(f.has, "iptables -C INPUT -j "+Chain)
	delete(f.has, "ip6tables -C INPUT -j "+Chain)
	if changed, err := SyncFirewall(after); err != nil || !changed {
		t.Errorf("chain gone: changed=%v err=%v, want it rebuilt", changed, err)
	}
	hooked()

	if err := RemoveFirewall(); err != nil {
		t.Fatal(err)
	}
	if changed, _ := SyncFirewall(after); !changed {
		t.Error("after RemoveFirewall a sync must build the chain again")
	}
}
