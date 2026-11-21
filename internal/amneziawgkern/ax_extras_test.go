package amneziawgkern

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// axHost extends fakeHost with the state the 3AX-UI extras drive: sysctls,
// NDP proxy entries, policy routing and the ip6tables / INPUT chains.
type axHost struct {
	*fakeHost
	sysctl map[string]string
	ndp    map[string]string
	rules  map[string]int
	routes map[string]bool
	filter map[string][]string
}

func newAxHost() *axHost {
	return &axHost{
		fakeHost: newFakeHost(),
		sysctl:   map[string]string{"net.ipv6.conf.eth1.accept_ra": "1", "net.ipv6.conf.eth0.accept_ra": "0"},
		ndp:      map[string]string{}, rules: map[string]int{}, routes: map[string]bool{}, filter: map[string][]string{},
	}
}

func (h *axHost) run(stdin, name string, args ...string) (string, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	switch {
	case name == "sysctl" && args[0] == "-w":
		h.commands = append(h.commands, cmd)
		k, v, _ := strings.Cut(args[1], "=")
		h.sysctl[k] = v
		return "", nil
	case name == "sysctl" && args[0] == "-n":
		h.commands = append(h.commands, cmd)
		return h.sysctl[args[1]], nil
	case name == "ip" && args[0] == "-6" && args[1] == "neigh":
		h.commands = append(h.commands, cmd)
		if args[2] == "del" {
			if _, ok := h.ndp[args[4]]; !ok {
				return "", errors.New("RTNETLINK answers: No such file or directory")
			}
			delete(h.ndp, args[4])
			return "", nil
		}
		h.ndp[args[4]] = args[6]
		return "", nil
	case name == "ip" && args[0] == "-6" && args[1] == "address":
		h.commands = append(h.commands, cmd)
		l, ok := h.links[args[5]]
		if !ok {
			return "", errors.New("Cannot find device")
		}
		l.addresses = append(l.addresses, args[3])
		return "", nil
	case name == "ip" && args[1] == "rule":
		h.commands = append(h.commands, cmd)
		key := args[0] + " " + strings.Join(args[3:], " ")
		switch args[2] {
		case "show":
			var out []string
			for k, n := range h.rules {
				fam, rule, _ := strings.Cut(k, " ")
				if fam == args[0] {
					for range n {
						out = append(out, "32765: from all "+rule)
					}
				}
			}
			return strings.Join(out, "\n"), nil
		case "add":
			h.rules[key]++
		case "del":
			if h.rules[key] == 0 {
				return "", errors.New("RTNETLINK answers: No such file or directory")
			}
			h.rules[key]--
			if h.rules[key] == 0 {
				delete(h.rules, key)
			}
		}
		return "", nil
	case name == "ip" && args[1] == "route":
		h.commands = append(h.commands, cmd)
		key := args[0] + " table " + args[len(args)-1]
		h.routes[key] = args[2] == "replace"
		return "", nil
	case name == "nft" && args[0] == "list":
		h.commands = append(h.commands, cmd)
		if _, ok := h.nat[args[3]]; !ok {
			return "", errors.New("No such file or directory")
		}
		return h.nat[args[3]], nil
	case name == "ip6tables" || (name == "iptables" && args[3] == "INPUT"):
		h.commands = append(h.commands, cmd)
		chain := name + " " + args[3]
		switch args[2] {
		case "-S":
			return strings.Join(h.filter[chain], "\n"), nil
		case "-I":
			h.filter[chain] = append([]string{"-A " + args[3] + " " + strings.Join(args[5:], " ")}, h.filter[chain]...)
		case "-D":
			rule := "-A " + args[3] + " " + strings.Join(args[4:], " ")
			i := slices.Index(h.filter[chain], rule)
			if i < 0 {
				return "", errors.New("Bad rule")
			}
			h.filter[chain] = slices.Delete(h.filter[chain], i, i+1)
		}
		return "", nil
	}
	return h.fakeHost.run(stdin, name, args...)
}

func axDesired(id int, mutate func(*Desired)) Desired {
	inst := testInstance()
	inst.Id = id
	inst.PrivateKey = testPrivateKey
	d := Desired{Instance: inst, EgressInterface: "eth0"}
	if mutate != nil {
		mutate(&d)
	}
	return d
}

func withIPv6(d *Desired) {
	d.Instance.Address = []string{"10.8.1.1/24", "2001:db8::1/112"}
	d.Instance.IPv6Enabled = true
	d.Instance.IPv6ExternalInterface = "eth1"
	d.Instance.Peers[0].AllowedIPs = []string{"10.8.1.2/32", "2001:db8::2/128"}
	d.Instance.Peers[1].AllowedIPs = []string{"10.8.1.3/32", "2001:db8::3/128"}
}

func taggedRules(h *axHost, chain, name string) []string {
	var out []string
	for _, r := range h.filter[chain] {
		if strings.Contains(r, "--comment "+forwardComment(name)) {
			out = append(out, r)
		}
	}
	return out
}

// TestKernelIPv6GivesPeersPublicAddresses: a routed prefix reaches peers as
// real addresses -- no NAT66, NDP answered on the IPv6 NIC, forwarding open.
func TestKernelIPv6GivesPeersPublicAddresses(t *testing.T) {
	h := newAxHost()
	m := newManager(h, t.TempDir())
	if err := m.Ensure(axDesired(1, withIPv6)); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	link := h.links["xui-awg1"]
	if !slices.Contains(link.addresses, "2001:db8::1/112") {
		t.Errorf("tunnel addresses = %v, want the server's IPv6 too", link.addresses)
	}
	if h.ndp["2001:db8::2"] != "eth1" || h.ndp["2001:db8::3"] != "eth1" {
		t.Errorf("NDP proxy = %v, want both peers on eth1", h.ndp)
	}
	if h.sysctl["net.ipv6.conf.all.forwarding"] != "1" || h.sysctl["net.ipv6.conf.eth1.proxy_ndp"] != "1" {
		t.Errorf("sysctls = %v", h.sysctl)
	}
	if h.sysctl["net.ipv6.conf.eth1.accept_ra"] != "2" {
		t.Errorf("accept_ra on eth1 = %q: forwarding would make the host drop its SLAAC default route", h.sysctl["net.ipv6.conf.eth1.accept_ra"])
	}
	if got := taggedRules(h, "ip6tables FORWARD", "xui-awg1"); len(got) != 2 {
		t.Errorf("ip6tables FORWARD accepts = %v, want both directions", got)
	}
	if nat := h.nat["xui_awg1"]; strings.Contains(nat, "2001:db8") || !strings.Contains(nat, "ip saddr 10.8.1.0/24") {
		t.Errorf("NAT table must masquerade IPv4 only:\n%s", nat)
	}

	d := axDesired(1, withIPv6)
	d.Instance.Peers = d.Instance.Peers[:1]
	if err := m.Ensure(d); err != nil {
		t.Fatal(err)
	}
	if _, still := h.ndp["2001:db8::3"]; still || h.ndp["2001:db8::2"] != "eth1" {
		t.Errorf("after removing bob NDP proxy = %v", h.ndp)
	}

	m.Reconcile(nil)
	if len(h.ndp) != 0 || len(taggedRules(h, "ip6tables FORWARD", "xui-awg1")) != 0 {
		t.Errorf("after teardown ndp=%v ip6tables=%v", h.ndp, h.filter)
	}
}

// TestKernelIPv6KeepsAStaticNICStatic: accept_ra 0 means no SLAAC; switching it
// to 2 would make the host start acting on advertisements it ignored.
func TestKernelIPv6KeepsAStaticNICStatic(t *testing.T) {
	h := newAxHost()
	m := newManager(h, t.TempDir())
	if err := m.Ensure(axDesired(1, func(d *Desired) { withIPv6(d); d.Instance.IPv6ExternalInterface = "" })); err != nil {
		t.Fatal(err)
	}
	if h.sysctl["net.ipv6.conf.eth0.accept_ra"] != "0" || h.ndp["2001:db8::2"] != "eth0" {
		t.Errorf("accept_ra=%q ndp=%v, want eth0 left at 0 and used for NDP", h.sysctl["net.ipv6.conf.eth0.accept_ra"], h.ndp)
	}
}

func TestKernelPortForwardingDNATsToThePeer(t *testing.T) {
	h := newAxHost()
	m := newManager(h, t.TempDir())
	err := m.Ensure(axDesired(1, func(d *Desired) { d.Instance.Peers[1].ForwardedPorts = "443, 27015-27016" }))
	if err != nil {
		t.Fatal(err)
	}
	nat := h.nat["xui_awg1"]
	for _, want := range []string{
		`iifname "eth0" tcp dport { 443, 27015, 27016 } dnat to 10.8.1.3`,
		`iifname "eth0" udp dport { 443, 27015, 27016 } dnat to 10.8.1.3`,
		`ip saddr 10.8.1.0/24 oifname "eth0" masquerade`,
	} {
		if !strings.Contains(nat, want) {
			t.Errorf("NAT table lacks %q:\n%s", want, nat)
		}
	}
	if !slices.ContainsFunc(forwardRulesFor(h.fakeHost, "xui-awg1"), func(r string) bool { return strings.Contains(r, "--ctstate DNAT") }) {
		t.Errorf("FORWARD has no accept for forwarded connections: %v", h.forward)
	}
}

func tproxied(d *Desired) {
	d.RouteThroughXray = true
	d.TproxyPort = 50001
}

func TestKernelRouteThroughXrayHandsTrafficToXray(t *testing.T) {
	h := newAxHost()
	m := newManager(h, t.TempDir())
	if err := m.Ensure(axDesired(1, tproxied)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.nat["xui_awg1"], "masquerade") {
		t.Errorf("traffic for Xray must not be masqueraded out:\n%s", h.nat["xui_awg1"])
	}
	tp := h.nat["xui_awg1_tp"]
	for _, want := range []string{
		`iifname "xui-awg1" ct status dnat accept`,
		`iifname "xui-awg1" ip daddr 10.8.1.0/24 accept`,
		`tproxy ip to 127.0.0.1:50001 meta mark set ` + tproxyMark + ` accept`,
	} {
		if !strings.Contains(tp, want) {
			t.Errorf("TPROXY table lacks %q:\n%s", want, tp)
		}
	}
	if h.rules["-4 fwmark "+tproxyMark+" lookup "+tproxyTable] != 1 || !h.routes["-4 table "+tproxyTable] {
		t.Errorf("policy routing: rules=%v routes=%v", h.rules, h.routes)
	}
	if len(taggedRules(h, "iptables INPUT", "xui-awg1")) != 1 {
		t.Errorf("INPUT accept missing: %v", h.filter)
	}
	if len(forwardRulesFor(h.fakeHost, "xui-awg1")) != 0 {
		t.Errorf("FORWARD accepts without NAT egress or forwards: %v", h.forward)
	}

	m.Reconcile(nil)
	if _, left := h.nat["xui_awg1_tp"]; left || len(h.rules) != 0 || len(taggedRules(h, "iptables INPUT", "xui-awg1")) != 0 {
		t.Errorf("after teardown tp=%v rules=%v input=%v", left, h.rules, h.filter)
	}
}

// TestKernelTproxyRoutingIsShared: the mark rule serves every TPROXY inbound,
// so removing one must leave it for the other.
func TestKernelTproxyRoutingIsShared(t *testing.T) {
	h := newAxHost()
	m := newManager(h, t.TempDir())
	m.Reconcile([]Desired{axDesired(1, tproxied), axDesired(2, func(d *Desired) {
		tproxied(d)
		d.TproxyPort = 50002
		d.Instance.Address = []string{"10.9.1.1/24"}
		d.Instance.Peers[0].AllowedIPs = []string{"10.9.1.2/32"}
		d.Instance.Peers[1].AllowedIPs = []string{"10.9.1.3/32"}
	})})
	if h.rules["-4 fwmark "+tproxyMark+" lookup "+tproxyTable] != 1 {
		t.Fatalf("rule count = %v, want exactly one shared rule", h.rules)
	}
	m.Reconcile([]Desired{axDesired(1, tproxied)})
	if h.rules["-4 fwmark "+tproxyMark+" lookup "+tproxyTable] != 1 {
		t.Errorf("removing inbound 2 dropped the rule inbound 1 still needs")
	}
	m.Reconcile(nil)
	if len(h.rules) != 0 {
		t.Errorf("rules after the last TPROXY inbound left: %v", h.rules)
	}
}

func TestKernelTproxyHealsWithoutForkingEveryTick(t *testing.T) {
	h := newAxHost()
	m := newManager(h, t.TempDir())
	d := axDesired(1, tproxied)
	if err := m.Ensure(d); err != nil {
		t.Fatal(err)
	}
	h.commands = nil
	if err := m.Ensure(d); err != nil {
		t.Fatal(err)
	}
	if len(h.commands) != 0 {
		t.Fatalf("an unchanged tick within %v ran %q", healEvery, h.commands)
	}

	delete(h.nat, "xui_awg1_tp") // a firewall reload flushed it
	m.ifaces[1].lastHeal = time.Now().Add(-healEvery - time.Second)
	if err := m.Ensure(d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.nat["xui_awg1_tp"], "tproxy ip to 127.0.0.1:50001") {
		t.Errorf("TPROXY table not restored; commands %q", h.commands)
	}
}

// systemd-networkd drops foreign ip rules and routes when it restarts (an unattended
// systemd upgrade did it on a test VM); with the nft table intact, TPROXY went nowhere.
func TestKernelTproxyHealsTheMarkRouting(t *testing.T) {
	h := newAxHost()
	m := newManager(h, t.TempDir())
	d := axDesired(1, tproxied)
	if err := m.Ensure(d); err != nil {
		t.Fatal(err)
	}
	h.rules = map[string]int{}
	h.routes = map[string]bool{}
	m.ifaces[1].lastHeal = time.Now().Add(-healEvery - time.Second)
	if err := m.Ensure(d); err != nil {
		t.Fatal(err)
	}
	if h.rules["-4 fwmark "+tproxyMark+" lookup "+tproxyTable] != 1 || !h.routes["-4 table "+tproxyTable] {
		t.Errorf("mark routing not restored: rules=%v routes=%v", h.rules, h.routes)
	}
}

func TestKernelExtrasStayOffForAPlainInbound(t *testing.T) {
	h := newAxHost()
	m := newManager(h, t.TempDir())
	if err := m.Ensure(axDesired(1, nil)); err != nil {
		t.Fatal(err)
	}
	for _, c := range h.commands {
		for _, extra := range []string{"sysctl", "ip -6", "ip6tables", "rule", "neigh"} {
			if strings.Contains(c, extra) {
				t.Errorf("plain inbound ran %q", c)
			}
		}
	}
	if _, ok := h.nat["xui_awg1_tp"]; ok {
		t.Errorf("plain inbound got a TPROXY table")
	}
}
