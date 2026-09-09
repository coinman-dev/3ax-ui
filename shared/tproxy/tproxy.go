// Package tproxy builds the iptables/policy-routing rules that hand tunnel
// ingress to a loopback Xray TPROXY listener.
//
// AWG and native WireGuard need byte-identical rules; only the fwmark, routing
// table and default listener port differ, so each tunnel type passes its own
// Params instead of keeping a private copy of the rule text.
package tproxy

import "fmt"

// Params describes one tunnel's TPROXY wiring.
type Params struct {
	Interface   string // tunnel interface, e.g. awg0 / wg0
	Port        int    // Xray dokodemo-door port; DefaultPort is used when <= 0
	Fwmark      string // per-protocol fwmark, e.g. 0x1
	Table       string // per-protocol routing table, e.g. 100
	DefaultPort int    // fallback when Port is unset
	IPv6        bool   // emit the IPv6 half as well
}

// EffectivePort is the port the rules actually use: the configured one, or the
// flavour's default when unset.
func (p Params) EffectivePort() int { return p.port() }

func (p Params) port() int {
	if p.Port > 0 {
		return p.Port
	}
	return p.DefaultPort
}

// PostUpLines returns the rules that redirect tunnel ingress into Xray.
// Dual-stack: the IPv6 half is emitted only when the tunnel has IPv6 enabled.
// A single dokodemo-door inbound listening on :: catches both families via
// v4-mapped addresses (kernel default net.ipv6.bindv6only=0).
func PostUpLines(p Params) []string {
	port, fwmark, table := p.port(), p.Fwmark, p.Table
	lines := []string{
		// Drain before adding, and replace rather than add. PostUp runs under
		// `set -e`, so one "RTNETLINK answers: File exists" aborts it and takes
		// the whole interface down with it — which is what happens whenever a
		// previous PostDown did not run to the end, or the healing job put the
		// routing back while the interface was down. `ip rule add` does not
		// even complain: it just adds another copy, and they pile up.
		drainRule(false, fwmark, table),
		fmt.Sprintf("ip rule add fwmark %s/%s lookup %s", fwmark, fwmark, table),
		fmt.Sprintf("ip route replace local default dev lo table %s", table),
		fmt.Sprintf("iptables -t mangle -A PREROUTING -i %s -p tcp -j TPROXY --on-ip 127.0.0.1 --on-port %d --tproxy-mark %s/%s", p.Interface, port, fwmark, fwmark),
		fmt.Sprintf("iptables -t mangle -A PREROUTING -i %s -p udp -j TPROXY --on-ip 127.0.0.1 --on-port %d --tproxy-mark %s/%s", p.Interface, port, fwmark, fwmark),
	}
	if p.IPv6 {
		lines = append(lines,
			drainRule(true, fwmark, table),
			fmt.Sprintf("ip -6 rule add fwmark %s/%s lookup %s", fwmark, fwmark, table),
			fmt.Sprintf("ip -6 route replace local default dev lo table %s", table),
			fmt.Sprintf("ip6tables -t mangle -A PREROUTING -i %s -p tcp -j TPROXY --on-ip ::1 --on-port %d --tproxy-mark %s/%s", p.Interface, port, fwmark, fwmark),
			fmt.Sprintf("ip6tables -t mangle -A PREROUTING -i %s -p udp -j TPROXY --on-ip ::1 --on-port %d --tproxy-mark %s/%s", p.Interface, port, fwmark, fwmark),
		)
	}
	return lines
}

// drainRule removes every copy of the policy rule, not just the first.
//
// `ip rule del` takes one match at a time, so a single call leaves duplicates
// behind — and duplicates are exactly what an `ip rule add` that never fails
// produces. The loop condition failing is not an error under `set -e`, and it
// terminates because each pass removes one rule.
func drainRule(v6 bool, fwmark, table string) string {
	six := ""
	if v6 {
		six = "-6 "
	}
	return fmt.Sprintf("while ip %srule del fwmark %s/%s lookup %s 2>/dev/null; do :; done",
		six, fwmark, fwmark, table)
}

// PostDownLines mirrors PostUpLines with delete (-D) rules. The order is the
// reverse of setup on purpose: the mangle rules go first, so no packet is
// marked after the policy route that would carry it has been removed.
func PostDownLines(p Params) []string {
	port, fwmark, table := p.port(), p.Fwmark, p.Table
	lines := []string{
		fmt.Sprintf("iptables -t mangle -D PREROUTING -i %s -p tcp -j TPROXY --on-ip 127.0.0.1 --on-port %d --tproxy-mark %s/%s", p.Interface, port, fwmark, fwmark),
		fmt.Sprintf("iptables -t mangle -D PREROUTING -i %s -p udp -j TPROXY --on-ip 127.0.0.1 --on-port %d --tproxy-mark %s/%s", p.Interface, port, fwmark, fwmark),
		fmt.Sprintf("ip route del local default dev lo table %s", table),
		drainRule(false, fwmark, table),
	}
	if p.IPv6 {
		lines = append(lines,
			fmt.Sprintf("ip6tables -t mangle -D PREROUTING -i %s -p tcp -j TPROXY --on-ip ::1 --on-port %d --tproxy-mark %s/%s", p.Interface, port, fwmark, fwmark),
			fmt.Sprintf("ip6tables -t mangle -D PREROUTING -i %s -p udp -j TPROXY --on-ip ::1 --on-port %d --tproxy-mark %s/%s", p.Interface, port, fwmark, fwmark),
			fmt.Sprintf("ip -6 route del local default dev lo table %s", table),
			drainRule(true, fwmark, table),
		)
	}
	return lines
}
