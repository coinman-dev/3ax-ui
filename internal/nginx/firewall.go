package nginx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Chain is ours alone, reached by one jump from INPUT, so hand-written rules are never touched.
// It only RETURNs or DROPs: it can close a port but never open one that ufw/firewalld closed.
const Chain = "THREEAX-IN"

// tag marks our jump rule so it can be found again among rules we did not write.
const tag = "3ax-ui camouflage"

// runFirewall is the seam the tests replace. Nothing in this file may reach a
// real netfilter table any other way.
var runFirewall = func(name string, args ...string) ([]byte, error) {
	return run(name, args...)
}

// sshConfigDir is where the SSH port is looked up. A variable so the test can
// point it at a directory of its own.
var sshConfigDir = "/etc/ssh"

// PortRange is one port, or an inclusive range of them.
type PortRange struct{ From, To int }

// Port is the common case: a range of one.
func Port(n int) PortRange { return PortRange{n, n} }

// Ports wraps a list of single ports.
func Ports(ns ...int) []PortRange {
	out := make([]PortRange, 0, len(ns))
	for _, n := range ns {
		out = append(out, Port(n))
	}
	return out
}

func (p PortRange) valid() bool {
	return validPort(p.From) && validPort(p.To) && p.From <= p.To
}

// dport is the range as iptables wants to read it.
func (p PortRange) dport() string {
	if p.From == p.To {
		return strconv.Itoa(p.From)
	}
	return fmt.Sprintf("%d:%d", p.From, p.To)
}

// Firewall lists what stays reachable; other public traffic is dropped. SSH is not listed:
// ApplyFirewall always adds sshd's ports so a UI toggle can never lock the operator out.
type Firewall struct {
	TCP []PortRange
	UDP []PortRange

	// Ifaces are tunnels left unjudged: their peers already authenticated, and the resolver and
	// router they reach answer on INPUT, so dropping them closes the VPN from within.
	Ifaces []string
}

// FirewallAvailable reports whether this machine has the tools to do any of it.
func FirewallAvailable() bool {
	for _, fam := range families() {
		if fam.usable {
			return true
		}
	}
	return false
}

// FirewallActive reports whether our chain is currently in the INPUT path.
func FirewallActive() bool {
	for _, fam := range families() {
		if !fam.usable {
			continue
		}
		if _, err := runFirewall(fam.bin, "-C", "INPUT", "-j", Chain); err == nil {
			return true
		}
	}
	return false
}

// ApplyFirewall closes all but the given ports, SSH and essential traffic. It rebuilds the chain
// each call so reconcile can rerun it; the brief empty chain fails open, the safe direction.
func ApplyFirewall(fw Firewall) error {
	// SSHPorts never comes back empty, so there is always a way back in even
	// if the caller passed nothing at all.
	tcp := merge(fw.TCP, Ports(SSHPorts()...))
	udp := merge(fw.UDP)

	var applied int
	for _, fam := range families() {
		if !fam.usable {
			continue
		}
		if err := fam.rebuild(tcp, udp, fw.Ifaces); err != nil {
			return err
		}
		applied++
	}
	if applied == 0 {
		return fmt.Errorf("no iptables binary on this machine, so the ports cannot be closed")
	}
	return nil
}

// RemoveFirewall takes our chain back out. A chain already gone is not an error: the operator
// asked for the ports to be open and afterwards they are.
func RemoveFirewall() error {
	for _, fam := range families() {
		if !fam.usable {
			continue
		}
		// The jump may have been added more than once if something went wrong
		// halfway; keep deleting until iptables says there is nothing left.
		for range 10 {
			if _, err := runFirewall(fam.bin, "-D", "INPUT", "-j", Chain); err != nil {
				break
			}
		}
		_, _ = runFirewall(fam.bin, "-F", Chain)
		_, _ = runFirewall(fam.bin, "-X", Chain)
	}
	return nil
}

// SSHPorts reads Port and ListenAddress from sshd_config and sshd_config.d (where modern Debian
// often puts them). Finding none means 22, sshd's default.
func SSHPorts() []int {
	files := []string{filepath.Join(sshConfigDir, "sshd_config")}
	if extra, err := filepath.Glob(filepath.Join(sshConfigDir, "sshd_config.d", "*.conf")); err == nil {
		files = append(files, extra...)
	}

	var ports []int
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			switch strings.ToLower(fields[0]) {
			case "port":
				if port, err := strconv.Atoi(fields[1]); err == nil && validPort(port) {
					ports = append(ports, port)
				}
			case "listenaddress":
				// host:port, [v6]:port, or a bare address with no port at all.
				if _, portText, err := splitHostPort(fields[1]); err == nil {
					if port, err := strconv.Atoi(portText); err == nil && validPort(port) {
						ports = append(ports, port)
					}
				}
			}
		}
	}
	if len(ports) == 0 {
		return []int{22}
	}
	sort.Ints(ports)
	return slices.Compact(ports)
}

// family is one of the two netfilter tables, IPv4 and IPv6.
type family struct {
	bin    string
	v6     bool
	usable bool
}

func families() []family {
	out := make([]family, 0, 2)
	for _, f := range []family{{bin: "iptables"}, {bin: "ip6tables", v6: true}} {
		_, err := exec.LookPath(f.bin)
		f.usable = err == nil
		out = append(out, f)
	}
	return out
}

// rebuild writes the chain from nothing and makes sure INPUT jumps into it.
func (f family) rebuild(tcp, udp []PortRange, ifaces []string) error {
	// -N fails when the chain is already there, which is the normal case and
	// not something to report.
	_, _ = runFirewall(f.bin, "-N", Chain)
	if out, err := runFirewall(f.bin, "-F", Chain); err != nil {
		return fmt.Errorf("%s: clear %s: %w: %s", f.bin, Chain, err, strings.TrimSpace(string(out)))
	}

	for _, rule := range f.rules(tcp, udp, ifaces) {
		if out, err := runFirewall(f.bin, append([]string{"-A", Chain}, rule...)...); err != nil {
			return fmt.Errorf("%s: %s: %w: %s", f.bin, strings.Join(rule, " "), err, strings.TrimSpace(string(out)))
		}
	}

	// Hook last, so a failed rebuild leaves an unhooked or empty chain (fails open), and first in
	// INPUT, since a DROP after somebody else's ACCEPT is never reached.
	if _, err := runFirewall(f.bin, "-C", "INPUT", "-j", Chain); err == nil {
		return nil
	}
	if out, err := runFirewall(f.bin, "-I", "INPUT", "1", "-j", Chain); err != nil {
		return fmt.Errorf("%s: hook %s into INPUT: %w: %s", f.bin, Chain, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// rules is the chain in order. Read top to bottom: everything that must keep
// working is let out of the chain first, and only what is left over is dropped.
func (f family) rules(tcp, udp []PortRange, ifaces []string) [][]string {
	var rules [][]string

	// Unjudged: loopback (the panel talks to Xray, nginx and mtg over it), then the tunnels,
	// whose traffic came from someone who already authenticated.
	for _, iface := range append([]string{"lo"}, ifaces...) {
		rules = append(rules, []string{"-i", iface, "-j", "RETURN"})
	}

	// Replies to connections this machine opened (DNS, package manager, NTP, the Telegram bot),
	// so closing every port does not cost the server its own name resolution.
	rules = append(rules, []string{"-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "RETURN"})

	if f.v6 {
		// ICMPv6 carries neighbour discovery and packet-too-big; drop it and IPv6 breaks in
		// ways that look like anything but a firewall.
		rules = append(rules,
			[]string{"-p", "ipv6-icmp", "-j", "RETURN"},
			// DHCPv6 replies arrive on 546; a lost renewal costs the server its
			// address once the lease runs out, a slow lockout.
			[]string{"-p", "udp", "--dport", "546", "-j", "RETURN"},
		)
	} else {
		// Echo and the unreachable messages that carry path MTU.
		rules = append(rules,
			[]string{"-p", "icmp", "-j", "RETURN"},
			// The DHCP client, for the same reason: conntrack does not always
			// recognise the renewal as a reply.
			[]string{"-p", "udp", "--dport", "68", "-j", "RETURN"},
		)
	}

	for _, port := range tcp {
		rules = append(rules, []string{"-p", "tcp", "--dport", port.dport(), "-j", "RETURN"})
	}
	for _, port := range udp {
		rules = append(rules, []string{"-p", "udp", "--dport", port.dport(), "-j", "RETURN"})
	}

	// DROP rather than REJECT: the point of the whole feature is that a
	// scanner finds nothing here, and a rejection is an answer.
	return append(rules, []string{"-m", "comment", "--comment", tag, "-j", "DROP"})
}

// merge sorts and de-duplicates port lists, dropping anything out of range.
func merge(lists ...[]PortRange) []PortRange {
	seen := map[PortRange]bool{}
	var out []PortRange
	for _, list := range lists {
		for _, port := range list {
			if !port.valid() || seen[port] {
				continue
			}
			seen[port] = true
			out = append(out, port)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

func validPort(port int) bool { return port > 0 && port < 65536 }

// splitHostPort is net.SplitHostPort without the import cycle of caring about
// which half is which.
func splitHostPort(value string) (string, string, error) {
	idx := strings.LastIndex(value, ":")
	if idx < 0 {
		return "", "", fmt.Errorf("no port in %q", value)
	}
	// A bare IPv6 literal is full of colons and carries no port.
	if strings.Count(value, ":") > 1 && !strings.HasPrefix(value, "[") {
		return "", "", fmt.Errorf("no port in %q", value)
	}
	return value[:idx], value[idx+1:], nil
}
