package nginx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Chain is the one chain the panel owns. Everything the firewall does lives
// inside it, reached by a single jump from INPUT, so switching the mode off is
// a matter of deleting two things — and no rule the operator wrote by hand is
// ever touched, reordered or flushed.
//
// The chain only ever RETURNs or DROPs. A port that is allowed falls back into
// INPUT and meets whatever was already there, which means this can close a port
// but never open one: a server already behind ufw or firewalld does not quietly
// lose their protection by turning our switch on.
const Chain = "THREEAX-IN"

// tag marks our jump rule so it can be found again among rules we did not write.
const tag = "3ax-ui camouflage"

// runFirewall is the seam the tests replace. Nothing in this file may reach a
// real netfilter table any other way.
var runFirewall = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// sshConfigDir is where the SSH port is looked up. A variable so the test can
// point it at a directory of its own.
var sshConfigDir = "/etc/ssh"

// Firewall is the set of ports that stay reachable from outside. Everything
// else arriving on a real interface is dropped.
//
// SSH is not in here and cannot be left out of it: ApplyFirewall adds whatever
// sshd is listening on itself. A panel that can lock the operator out of their
// own server over a UI toggle is not a feature.
type Firewall struct {
	TCP []int
	UDP []int
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

// ApplyFirewall closes everything except the given ports, SSH, and the traffic
// that would break the machine to drop.
//
// It is written to be run again and again: the chain is rebuilt from scratch
// every time, so the reconcile job can call it on every tick and the rules
// always match the settings rather than drifting from them. Rebuilding empties
// the chain for an instant, which errs towards letting a packet through — the
// right direction for a mistake to go.
func ApplyFirewall(fw Firewall) error {
	// SSHPorts never comes back empty, so there is always a way back in even
	// if the caller passed nothing at all.
	tcp := merge(fw.TCP, SSHPorts())
	udp := merge(fw.UDP)

	var applied int
	for _, fam := range families() {
		if !fam.usable {
			continue
		}
		if err := fam.rebuild(tcp, udp); err != nil {
			return err
		}
		applied++
	}
	if applied == 0 {
		return fmt.Errorf("no iptables binary on this machine, so the ports cannot be closed")
	}
	return nil
}

// RemoveFirewall takes our chain back out. It is deliberately forgiving: a
// chain that was never there, or was removed by hand, is not an error — the
// operator asked for the ports to be open and afterwards they are.
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

// SSHPorts is where sshd is listening, as far as the config says.
//
// Both "Port" and "ListenAddress host:port" count, and the drop-in directory is
// read too — on a modern Debian the interesting line is often in there rather
// than in sshd_config itself. Finding nothing means the default, because sshd
// with no Port line listens on 22.
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
	return merge(ports)
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
func (f family) rebuild(tcp, udp []int) error {
	// -N fails when the chain is already there, which is the normal case and
	// not something to report.
	_, _ = runFirewall(f.bin, "-N", Chain)
	if out, err := runFirewall(f.bin, "-F", Chain); err != nil {
		return fmt.Errorf("%s: clear %s: %v: %s", f.bin, Chain, err, strings.TrimSpace(string(out)))
	}

	for _, rule := range f.rules(tcp, udp) {
		if out, err := runFirewall(f.bin, append([]string{"-A", Chain}, rule...)...); err != nil {
			return fmt.Errorf("%s: %s: %v: %s", f.bin, strings.Join(rule, " "), err, strings.TrimSpace(string(out)))
		}
	}

	// The hook goes on last, after the chain is known to be complete. A
	// rebuild that fails halfway therefore leaves either an unhooked chain or
	// an empty one — both let packets through, which is the direction a
	// failure here should fall.
	//
	// First in INPUT on purpose: a DROP that comes after somebody else's
	// ACCEPT would never be reached, and the port would stay open while the
	// panel reported it closed.
	if _, err := runFirewall(f.bin, "-C", "INPUT", "-j", Chain); err == nil {
		return nil
	}
	if out, err := runFirewall(f.bin, "-I", "INPUT", "1", "-j", Chain); err != nil {
		return fmt.Errorf("%s: hook %s into INPUT: %v: %s", f.bin, Chain, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// rules is the chain in order. Read top to bottom: everything that must keep
// working is let out of the chain first, and only what is left over is dropped.
func (f family) rules(tcp, udp []int) [][]string {
	rules := [][]string{
		// Loopback: the panel talks to Xray, nginx and mtg over it constantly.
		{"-i", "lo", "-j", "RETURN"},
		// Answers to connections this machine opened itself — updates, DNS,
		// the Telegram bot. Dropping these would cut the server off outwards
		// while looking like it only closed inbound ports.
		{"-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "RETURN"},
	}

	if f.v6 {
		// ICMPv6 is not optional. Neighbour discovery and packet-too-big live
		// here; drop it and IPv6 stops working in ways that look like anything
		// but a firewall.
		rules = append(rules, []string{"-p", "ipv6-icmp", "-j", "RETURN"})
	} else {
		// Echo and the unreachable messages that carry path MTU.
		rules = append(rules, []string{"-p", "icmp", "-j", "RETURN"})
	}

	for _, port := range tcp {
		rules = append(rules, []string{"-p", "tcp", "--dport", strconv.Itoa(port), "-j", "RETURN"})
	}
	for _, port := range udp {
		rules = append(rules, []string{"-p", "udp", "--dport", strconv.Itoa(port), "-j", "RETURN"})
	}

	// DROP rather than REJECT: the point of the whole feature is that a
	// scanner finds nothing here, and a rejection is an answer.
	return append(rules, []string{"-m", "comment", "--comment", tag, "-j", "DROP"})
}

// merge sorts and de-duplicates port lists, dropping anything out of range.
func merge(lists ...[]int) []int {
	seen := map[int]bool{}
	var out []int
	for _, list := range lists {
		for _, port := range list {
			if !validPort(port) || seen[port] {
				continue
			}
			seen[port] = true
			out = append(out, port)
		}
	}
	sort.Ints(out)
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
