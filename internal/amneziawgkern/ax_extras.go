package amneziawgkern

// 3AX-UI additions to kernel mode: public IPv6 for peers (NDP proxy, no
// NAT66), per-peer port forwarding (DNAT) and routing through Xray (TPROXY).

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// tproxyMark routes TPROXY-marked packets to the local table; one pair serves
// every kernel inbound because each sends to its own Xray port.
const (
	tproxyMark  = "0x5a17"
	tproxyTable = "23063"
)

// healEvery bounds how often a TPROXY inbound checks that its rules survived
// someone else's firewall reload; an unchanged tick otherwise forks nothing.
const healEvery = time.Minute

type peerForward struct {
	addr  string
	ports []int
}

// axExtras is what buildSpec derives for the 3AX-UI features; fp makes it
// comparable so an unchanged tick can skip it.
type axExtras struct {
	ipv6Iface  string
	ipv6Peers  []string
	tunnelV4   []string
	tunnelV6   []string
	forwards   []peerForward
	tproxyPort int
	fp         string
}

func buildExtras(d Desired, addresses []string, egress string) axExtras {
	inst := d.Instance
	x := axExtras{}
	for _, a := range addresses {
		p := netip.MustParsePrefix(a).Masked()
		if p.Addr().Is4() {
			x.tunnelV4 = append(x.tunnelV4, p.String())
		} else {
			x.tunnelV6 = append(x.tunnelV6, p.String())
		}
	}
	if len(x.tunnelV6) > 0 {
		x.ipv6Iface = inst.IPv6ExternalInterface
		if x.ipv6Iface == "" {
			x.ipv6Iface = egress
		}
	}
	for _, p := range inst.Peers {
		if x.ipv6Iface != "" {
			if v6 := amneziawg.FirstIPv6(p.AllowedIPs); v6 != "" {
				x.ipv6Peers = append(x.ipv6Peers, v6)
			}
		}
		if ports := amneziawg.ExpandForwardedPorts(p.ForwardedPorts); len(ports) > 0 {
			if v4 := amneziawg.FirstIPv4(p.AllowedIPs); v4 != "" {
				x.forwards = append(x.forwards, peerForward{addr: v4, ports: ports})
			}
		}
	}
	slices.Sort(x.ipv6Peers)
	if d.RouteThroughXray {
		x.tproxyPort = d.TproxyPort
	}
	x.fp = fmt.Sprintf("%s|%v|%v|%v|%v|%d", x.ipv6Iface, x.ipv6Peers, x.tunnelV4, x.tunnelV6, x.forwards, x.tproxyPort)
	return x
}

// dnatChain sends each peer's forwarded ports, arriving on egress, to the peer.
func dnatChain(forwards []peerForward, egress string) string {
	if len(forwards) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\tchain prerouting {\n\t\ttype nat hook prerouting priority -100; policy accept;\n")
	for _, f := range forwards {
		set := make([]string, len(f.ports))
		for i, p := range f.ports {
			set[i] = fmt.Sprint(p)
		}
		for _, proto := range []string{"tcp", "udp"} {
			fmt.Fprintf(&b, "\t\tiifname %q %s dport { %s } dnat to %s\n", egress, proto, strings.Join(set, ", "), f.addr)
		}
	}
	b.WriteString("\t}\n")
	return b.String()
}

func onlyIPv4(addresses []string) []string {
	var out []string
	for _, a := range addresses {
		if netip.MustParsePrefix(a).Addr().Is4() {
			out = append(out, a)
		}
	}
	return out
}

func tproxyTableName(id int) string {
	return fmt.Sprintf("xui_awg%d_tp", id)
}

// tproxyTransaction replaces the inbound's TPROXY table; port 0 only deletes it.
// Traffic to the tunnel itself and replies on forwarded ports stay in the kernel.
func tproxyTransaction(id int, tunnel string, x axExtras) string {
	table := tproxyTableName(id)
	tx := fmt.Sprintf("add table inet %s\ndelete table inet %s\n", table, table)
	if x.tproxyPort == 0 {
		return tx
	}
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %s {\n\tchain prerouting {\n\t\ttype filter hook prerouting priority mangle; policy accept;\n", table)
	fmt.Fprintf(&b, "\t\tiifname %q ct status dnat accept\n", tunnel)
	for _, s := range x.tunnelV4 {
		fmt.Fprintf(&b, "\t\tiifname %q ip daddr %s accept\n", tunnel, s)
	}
	for _, s := range x.tunnelV6 {
		fmt.Fprintf(&b, "\t\tiifname %q ip6 daddr %s accept\n", tunnel, s)
	}
	fmt.Fprintf(&b, "\t\tiifname %q meta nfproto ipv4 meta l4proto { tcp, udp } tproxy ip to 127.0.0.1:%d meta mark set %s accept\n", tunnel, x.tproxyPort, tproxyMark)
	if len(x.tunnelV6) > 0 {
		fmt.Fprintf(&b, "\t\tiifname %q meta nfproto ipv6 meta l4proto { tcp, udp } tproxy ip6 to [::1]:%d meta mark set %s accept\n", tunnel, x.tproxyPort, tproxyMark)
	}
	b.WriteString("\t}\n}\n")
	return tx + b.String()
}

// applyExtras converges IPv6, TPROXY and their filter accepts. force re-applies
// everything, for a link this process just built or adopted.
func (m *Manager) applyExtras(id int, name string, prev, cur axExtras, force bool) error {
	if !force && prev.fp == cur.fp {
		return nil
	}
	if err := m.applyIPv6(name, prev, cur, force); err != nil {
		return err
	}
	if prev.tproxyPort > 0 || cur.tproxyPort > 0 {
		if out, err := m.host.run(tproxyTransaction(id, name, cur), "nft", "-f", "-"); err != nil {
			return fmt.Errorf("amneziawgkern: applying TPROXY for %s: %w (%s)", name, err, out)
		}
		if cur.tproxyPort > 0 {
			m.ensureTproxyRouting(len(cur.tunnelV6) > 0)
		}
	}
	if len(filterAccepts(name, prev)) > 0 || len(filterAccepts(name, cur)) > 0 {
		m.syncFilterAccepts(name, cur)
	}
	return nil
}

func (m *Manager) applyIPv6(name string, prev, cur axExtras, force bool) error {
	for _, addr := range prev.ipv6Peers {
		if prev.ipv6Iface != cur.ipv6Iface || !slices.Contains(cur.ipv6Peers, addr) {
			_, _ = m.host.run("", "ip", "-6", "neigh", "del", "proxy", addr, "dev", prev.ipv6Iface)
		}
	}
	if cur.ipv6Iface == "" {
		return nil
	}
	if err := amneziawg.ValidateInterfaceName(cur.ipv6Iface); err != nil {
		return fmt.Errorf("amneziawgkern: %w", err)
	}
	if force || prev.ipv6Iface != cur.ipv6Iface {
		if out, err := m.host.run("", "sysctl", "-w", "net.ipv6.conf.all.forwarding=1"); err != nil {
			return fmt.Errorf("amneziawgkern: enabling IPv6 forwarding: %w (%s)", err, out)
		}
		// Forwarding makes the kernel ignore router advertisements unless
		// accept_ra is 2, which would drop a SLAAC-configured default route.
		key := "net.ipv6.conf." + cur.ipv6Iface
		if ra, err := m.host.run("", "sysctl", "-n", key+".accept_ra"); err == nil && strings.TrimSpace(ra) == "1" {
			_, _ = m.host.run("", "sysctl", "-w", key+".accept_ra=2")
		}
		if out, err := m.host.run("", "sysctl", "-w", key+".proxy_ndp=1"); err != nil {
			return fmt.Errorf("amneziawgkern: enabling NDP proxy on %s: %w (%s)", cur.ipv6Iface, err, out)
		}
	}
	for _, addr := range cur.ipv6Peers {
		if out, err := m.host.run("", "ip", "-6", "neigh", "replace", "proxy", addr, "dev", cur.ipv6Iface); err != nil {
			return fmt.Errorf("amneziawgkern: NDP proxy for %s on %s: %w (%s)", addr, cur.ipv6Iface, err, out)
		}
	}
	return nil
}

func (m *Manager) ensureTproxyRouting(ipv6 bool) {
	families := []string{"-4"}
	if ipv6 {
		families = append(families, "-6")
	}
	for _, fam := range families {
		rules, err := m.host.run("", "ip", fam, "rule", "show")
		if err != nil {
			logger.Warning("amneziawgkern: listing ", fam, " rules: ", err)
			continue
		}
		if !strings.Contains(rules, "fwmark "+tproxyMark+" lookup "+tproxyTable) {
			if out, err := m.host.run("", "ip", fam, "rule", "add", "fwmark", tproxyMark, "lookup", tproxyTable); err != nil {
				logger.Warning("amneziawgkern: adding TPROXY rule: ", err, " ", out)
			}
		}
		if out, err := m.host.run("", "ip", fam, "route", "replace", "local", "default", "dev", "lo", "table", tproxyTable); err != nil {
			logger.Warning("amneziawgkern: adding TPROXY route: ", err, " ", out)
		}
	}
}

// dropTproxyRouting removes the shared mark rule once no inbound uses it.
func (m *Manager) dropTproxyRouting() {
	for _, cur := range m.ifaces {
		if cur.spec.extras.tproxyPort > 0 {
			return
		}
	}
	for _, fam := range []string{"-4", "-6"} {
		for range 8 {
			if _, err := m.host.run("", "ip", fam, "rule", "del", "fwmark", tproxyMark, "lookup", tproxyTable); err != nil {
				break
			}
		}
		_, _ = m.host.run("", "ip", fam, "route", "del", "local", "default", "dev", "lo", "table", tproxyTable)
	}
}

// filterAccepts are the iptables/ip6tables accepts the extras need on hosts
// whose FORWARD or INPUT policy is DROP (Docker, ufw); best effort.
func filterAccepts(name string, x axExtras) map[string][][]string {
	comment := []string{"-m", "comment", "--comment", forwardComment(name), "-j", "ACCEPT"}
	out := map[string][][]string{}
	if x.ipv6Iface != "" {
		out["ip6tables FORWARD"] = [][]string{
			append([]string{"-i", name, "-o", x.ipv6Iface}, comment...),
			append([]string{"-i", x.ipv6Iface, "-o", name}, comment...),
		}
	}
	if x.tproxyPort > 0 {
		out["iptables INPUT"] = [][]string{append([]string{"-i", name}, comment...)}
		if len(x.tunnelV6) > 0 {
			out["ip6tables INPUT"] = [][]string{append([]string{"-i", name}, comment...)}
		}
	}
	return out
}

func (m *Manager) syncFilterAccepts(name string, x axExtras) {
	m.removeFilterAccepts(name)
	for chain, rules := range filterAccepts(name, x) {
		tool, ch, _ := strings.Cut(chain, " ")
		if !m.host.hasCommand(tool) {
			continue
		}
		for _, rule := range rules {
			if out, err := m.host.run("", tool, append([]string{"-w", "5", "-I", ch, "1"}, rule...)...); err != nil {
				logger.Warning("amneziawgkern: adding ", chain, " accept for ", name, ": ", err, " ", out)
			}
		}
	}
}

// removeFilterAccepts deletes this interface's tagged rules from the chains
// the extras use; FORWARD for IPv4 stays with removeForwardRules.
func (m *Manager) removeFilterAccepts(name string) {
	tag := forwardComment(name)
	for _, chain := range []string{"ip6tables FORWARD", "iptables INPUT", "ip6tables INPUT"} {
		tool, ch, _ := strings.Cut(chain, " ")
		if !m.host.hasCommand(tool) {
			continue
		}
		listing, err := m.host.run("", tool, "-w", "5", "-S", ch)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(listing, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 || fields[0] != "-A" {
				continue
			}
			for i := range fields {
				fields[i] = strings.Trim(fields[i], `"`)
			}
			if !hasCommentTag(fields, tag) {
				continue
			}
			fields[0] = "-D"
			_, _ = m.host.run("", tool, append([]string{"-w", "5"}, fields...)...)
		}
	}
}

// teardownExtras undoes what applyExtras set up for one interface; the shared
// sysctls stay, since something else on the host may rely on them.
func (m *Manager) teardownExtras(id int, name string, x axExtras) {
	for _, addr := range x.ipv6Peers {
		_, _ = m.host.run("", "ip", "-6", "neigh", "del", "proxy", addr, "dev", x.ipv6Iface)
	}
	if x.tproxyPort > 0 {
		if out, err := m.host.run(tproxyTransaction(id, name, axExtras{}), "nft", "-f", "-"); err != nil {
			logger.Warning("amneziawgkern: removing TPROXY for ", name, ": ", err, " ", out)
		}
		m.dropTproxyRouting()
	}
	if len(filterAccepts(name, x)) > 0 {
		m.removeFilterAccepts(name)
	}
}

// healExtras re-applies a TPROXY inbound's table and mark routing when something
// outside the panel (a firewall reload, nft flush, networkd restart) removed them.
func (m *Manager) healExtras(id int, cur *managed) {
	if cur.spec.extras.tproxyPort == 0 {
		return
	}
	if cur.lastHeal.IsZero() {
		// Just built or adopted: the rules are fresh, the clock starts now.
		cur.lastHeal = time.Now()
		return
	}
	if time.Since(cur.lastHeal) < healEvery {
		return
	}
	cur.lastHeal = time.Now()
	if _, err := m.host.run("", "nft", "list", "table", "inet", tproxyTableName(id)); err == nil {
		// systemd-networkd drops foreign rules and routes when it restarts.
		m.ensureTproxyRouting(len(cur.spec.extras.tunnelV6) > 0)
		return
	}
	logger.Warning("amneziawgkern: TPROXY table for ", cur.spec.name, " is gone, restoring it")
	if err := m.applyExtras(id, cur.spec.name, axExtras{}, cur.spec.extras, true); err != nil {
		logger.Warning("amneziawgkern: restoring TPROXY for ", cur.spec.name, ": ", err)
	}
}

// InterfaceName is the kernel link an inbound runs on; the nginx front-end's
// firewall lets traffic from it through untouched.
func InterfaceName(id int) string { return ownedInterfaceName(id) }
