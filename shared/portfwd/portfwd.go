// Package portfwd parses user-supplied port lists and generates iptables rules
// for per-client DNAT (port forwarding) on WireGuard / AmneziaWG interfaces.
package portfwd

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/coinman-dev/3ax-ui/v2/logger"
)

// PortSpec is a single port (Start == End) or an inclusive range Start..End.
type PortSpec struct {
	Start int
	End   int
}

// IsRange reports whether the spec covers more than one port.
func (p PortSpec) IsRange() bool { return p.End > p.Start }

// dportArg returns the iptables --dport argument: "N" or "N:M".
func (p PortSpec) dportArg() string {
	if p.IsRange() {
		return fmt.Sprintf("%d:%d", p.Start, p.End)
	}
	return strconv.Itoa(p.Start)
}

// dnatTarget returns the DNAT target: "ip:N" or "ip:N-M".
func (p PortSpec) dnatTarget(clientIP string) string {
	if p.IsRange() {
		return fmt.Sprintf("%s:%d-%d", clientIP, p.Start, p.End)
	}
	return fmt.Sprintf("%s:%d", clientIP, p.Start)
}

// Parse splits a user-supplied string ("80, 443; 8000-8100") into validated specs.
// Tokens are separated by comma or semicolon; whitespace is ignored.
// Invalid tokens are silently dropped (validation is best-effort by design —
// the input is a free-form text field).
func Parse(input string) []PortSpec {
	if input == "" {
		return nil
	}
	// Normalize separators: turn ';' into ',' so we can split once.
	input = strings.ReplaceAll(input, ";", ",")
	tokens := strings.Split(input, ",")

	var specs []PortSpec
	seen := make(map[string]struct{}, len(tokens))

	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		spec, ok := parseToken(tok)
		if !ok {
			continue
		}
		key := fmt.Sprintf("%d-%d", spec.Start, spec.End)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		specs = append(specs, spec)
	}
	return specs
}

func parseToken(tok string) (PortSpec, bool) {
	if idx := strings.IndexByte(tok, '-'); idx >= 0 {
		start, ok1 := parsePort(strings.TrimSpace(tok[:idx]))
		end, ok2 := parsePort(strings.TrimSpace(tok[idx+1:]))
		if !ok1 || !ok2 || start > end {
			return PortSpec{}, false
		}
		return PortSpec{Start: start, End: end}, true
	}
	p, ok := parsePort(tok)
	if !ok {
		return PortSpec{}, false
	}
	return PortSpec{Start: p, End: p}, true
}

func parsePort(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, false
	}
	return n, true
}

// Normalize parses the input and returns a canonical comma-separated form
// (deduplicated, individual ports as "N", ranges as "N-M").
// Returns empty string if no valid specs were found.
func Normalize(input string) string {
	specs := Parse(input)
	if len(specs) == 0 {
		return ""
	}
	parts := make([]string, len(specs))
	for i, s := range specs {
		if s.IsRange() {
			parts[i] = fmt.Sprintf("%d-%d", s.Start, s.End)
		} else {
			parts[i] = strconv.Itoa(s.Start)
		}
	}
	return strings.Join(parts, ",")
}

// RuleSet holds the DNAT and FORWARD iptables rule arguments (without the
// leading "iptables" command and without the action flag -A/-D/-I).
// Each entry is a list of arguments suitable for direct exec or for
// joining into a shell-safe PostUp / PostDown line.
type RuleSet struct {
	Nat     [][]string // -t nat PREROUTING / OUTPUT rules
	Forward [][]string // filter FORWARD rules
}

// ruleComment returns a unique iptables comment used to tag each rule so we
// can reliably remove them later via -D regardless of source order.
func ruleComment(uuid string) string {
	if uuid == "" {
		return "3ax-fwd"
	}
	return "3ax-fwd-" + uuid
}

// Rules generates the full set of iptables rules for the given client.
//
//	extIface  — server's external (WAN) interface where traffic arrives
//	tunIface  — wg/awg interface name (used in FORWARD -i)
//	clientIP  — tunnel-side IPv4 of the client, with or without /32 mask
//	uuid      — client UUID, embedded into rule comments for safe removal
//	specs     — parsed port specs
//
// For each spec we emit two PREROUTING DNAT rules (tcp + udp) and two
// FORWARD rules. UDP is included unconditionally because most games rely on
// it (Source engine, Northstar, P2P).
func Rules(extIface, tunIface, clientIP, uuid string, specs []PortSpec) RuleSet {
	if len(specs) == 0 {
		return RuleSet{}
	}
	clientIP = stripCIDR(clientIP)
	if clientIP == "" {
		return RuleSet{}
	}
	comment := ruleComment(uuid)

	var rs RuleSet
	for _, spec := range specs {
		dport := spec.dportArg()
		target := spec.dnatTarget(clientIP)
		for _, proto := range []string{"tcp", "udp"} {
			natRule := []string{
				"-t", "nat", "PREROUTING",
				"-p", proto,
			}
			if extIface != "" {
				natRule = append(natRule, "-i", extIface)
			}
			natRule = append(natRule,
				"--dport", dport,
				"-m", "comment", "--comment", comment,
				"-j", "DNAT",
				"--to-destination", target,
			)
			rs.Nat = append(rs.Nat, natRule)

			fwdRule := []string{
				"FORWARD",
				"-d", clientIP,
				"-p", proto,
				"-o", tunIface,
				"--dport", dport,
				"-m", "comment", "--comment", comment,
				"-j", "ACCEPT",
			}
			rs.Forward = append(rs.Forward, fwdRule)
		}
	}
	return rs
}

// PostUpLines turns a RuleSet into shell-safe `iptables -A ...` strings,
// suitable for joining with "; " into the wg-quick PostUp directive.
func PostUpLines(rs RuleSet) []string {
	return iptablesLines(rs, "-A")
}

// PostDownLines turns a RuleSet into shell-safe `iptables -D ...` strings.
func PostDownLines(rs RuleSet) []string {
	return iptablesLines(rs, "-D")
}

func iptablesLines(rs RuleSet, action string) []string {
	out := make([]string, 0, len(rs.Nat)+len(rs.Forward))
	for _, args := range rs.Nat {
		out = append(out, joinIptables(action, args, true))
	}
	for _, args := range rs.Forward {
		out = append(out, joinIptables(action, args, false))
	}
	return out
}

// joinIptables builds a single iptables command line. natTable=true means the
// rule list already starts with "-t nat" — we must place the action flag
// AFTER the table specifier so iptables parses it correctly.
func joinIptables(action string, args []string, natTable bool) string {
	parts := make([]string, 0, len(args)+3)
	parts = append(parts, "iptables")
	if natTable && len(args) >= 2 && args[0] == "-t" {
		parts = append(parts, args[0], args[1]) // "-t nat"
		parts = append(parts, action)
		parts = append(parts, args[2:]...)
	} else {
		parts = append(parts, action)
		parts = append(parts, args...)
	}
	return strings.Join(parts, " ")
}

// Apply runs the rule set live via the iptables binary using -A (append).
// Errors on individual rules are logged but do not abort the rest — at the
// next interface restart PostUp will reconcile the state from the database.
// noopWhenMissing=true skips execution if iptables is not available, useful
// during config previews on machines without the binary.
func Apply(rs RuleSet) {
	runIptables(rs, "-A")
}

// Revoke removes the rule set live via -D. Missing rules produce a warning
// but are non-fatal.
func Revoke(rs RuleSet) {
	runIptables(rs, "-D")
}

func runIptables(rs RuleSet, action string) {
	if _, err := exec.LookPath("iptables"); err != nil {
		return
	}
	for _, args := range rs.Nat {
		cmd := exec.Command("iptables", buildArgs(action, args, true)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			logger.Warningf("iptables %s nat failed: %v: %s", action, err, strings.TrimSpace(string(out)))
		}
	}
	for _, args := range rs.Forward {
		cmd := exec.Command("iptables", buildArgs(action, args, false)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			logger.Warningf("iptables %s forward failed: %v: %s", action, err, strings.TrimSpace(string(out)))
		}
	}
}

func buildArgs(action string, args []string, natTable bool) []string {
	out := make([]string, 0, len(args)+1)
	if natTable && len(args) >= 2 && args[0] == "-t" {
		out = append(out, args[0], args[1])
		out = append(out, action)
		out = append(out, args[2:]...)
	} else {
		out = append(out, action)
		out = append(out, args...)
	}
	return out
}

// stripCIDR removes a "/N" suffix if present.
func stripCIDR(addr string) string {
	if idx := strings.IndexByte(addr, '/'); idx >= 0 {
		return addr[:idx]
	}
	return addr
}
