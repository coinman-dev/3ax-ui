package tunnel

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/coinman-dev/3ax-ui/v2/shared/tproxy"
)

// RouteViaXray depends on two independent pieces of kernel state: the mangle
// rules that mark and redirect tunnel ingress, and the routing policy rule that
// sends marked packets to the local table. PostUp installs both, but the policy
// rule is fragile — systemd-networkd removes routing policy rules it did not
// create whenever it reconfigures an interface (ManageForeignRoutingPolicyRules
// defaults to yes), and a DHCP lease change is enough to trigger that. The
// tunnel then keeps marking packets that no longer have anywhere to go, and
// nothing in the panel would notice.
//
// So the state is verified periodically and repaired in place, which is both
// cheaper and less disruptive than bouncing the interface.

// runCmd is indirected for tests.
var runCmd = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// EnsureTproxyRouting checks the TPROXY wiring of a tunnel that routes through
// Xray and reinstalls whatever is missing. It returns a description of what was
// repaired, empty when everything was already in place.
func EnsureTproxyRouting(k Kind, server *Server) []string {
	if server == nil || !server.RouteViaXray {
		return nil
	}
	name := ifaceName(k, server)
	params := tproxyParams(k, server, name)
	var repaired []string

	for _, fam := range families(server.IPv6Enabled) {
		if policyRuleInstalled(fam, k.TproxyFwmark, k.TproxyTable) {
			continue
		}
		if _, err := runCmd(fam.ipBin, fam.ipArgs("rule", "add", "fwmark",
			k.TproxyFwmark+"/"+k.TproxyFwmark, "lookup", k.TproxyTable)...); err != nil {
			continue
		}
		// The local route lives in the same table and is usually still there;
		// adding it again is harmless because the kernel rejects duplicates.
		_, _ = runCmd(fam.ipBin, fam.ipArgs("route", "add", "local", "default",
			"dev", "lo", "table", k.TproxyTable)...)
		repaired = append(repaired, fmt.Sprintf("%s policy rule for fwmark %s", fam.label, k.TproxyFwmark))
	}

	for _, rule := range mangleChecks(params, server.IPv6Enabled) {
		if _, err := runCmd(rule.bin, append([]string{"-t", "mangle", "-C"}, rule.args...)...); err == nil {
			continue
		}
		if _, err := runCmd(rule.bin, append([]string{"-t", "mangle", "-A"}, rule.args...)...); err != nil {
			continue
		}
		repaired = append(repaired, fmt.Sprintf("%s mangle rule (%s)", rule.label, rule.proto))
	}
	return repaired
}

type ipFamily struct {
	label string
	ipBin string
	six   bool
}

func (f ipFamily) ipArgs(args ...string) []string {
	if f.six {
		return append([]string{"-6"}, args...)
	}
	return args
}

func families(ipv6 bool) []ipFamily {
	out := []ipFamily{{label: "IPv4", ipBin: "ip"}}
	if ipv6 {
		out = append(out, ipFamily{label: "IPv6", ipBin: "ip", six: true})
	}
	return out
}

// policyRuleInstalled reports whether a rule matching this tunnel's fwmark and
// table is present.
func policyRuleInstalled(fam ipFamily, fwmark, table string) bool {
	out, err := runCmd(fam.ipBin, fam.ipArgs("rule", "show")...)
	if err != nil {
		// Unknown state: claim it is installed rather than risk piling up
		// duplicate rules on every tick.
		return true
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.Contains(line, "fwmark "+fwmark) && strings.HasSuffix(strings.TrimSpace(line), "lookup "+table) {
			return true
		}
	}
	return false
}

type mangleRule struct {
	bin   string
	label string
	proto string
	args  []string
}

// mangleChecks lists the PREROUTING rules PostUp installs, in the form used to
// both test (-C) and add (-A) them.
func mangleChecks(p tproxy.Params, ipv6 bool) []mangleRule {
	port := fmt.Sprintf("%d", p.EffectivePort())
	mark := p.Fwmark + "/" + p.Fwmark
	var out []mangleRule
	for _, proto := range []string{"tcp", "udp"} {
		out = append(out, mangleRule{
			bin: "iptables", label: "IPv4", proto: proto,
			args: []string{"PREROUTING", "-i", p.Interface, "-p", proto, "-j", "TPROXY",
				"--on-ip", "127.0.0.1", "--on-port", port, "--tproxy-mark", mark},
		})
		if ipv6 {
			out = append(out, mangleRule{
				bin: "ip6tables", label: "IPv6", proto: proto,
				args: []string{"PREROUTING", "-i", p.Interface, "-p", proto, "-j", "TPROXY",
					"--on-ip", "::1", "--on-port", port, "--tproxy-mark", mark},
			})
		}
	}
	return out
}
