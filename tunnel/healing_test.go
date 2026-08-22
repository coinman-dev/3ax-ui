package tunnel

import (
	"fmt"
	"strings"
	"testing"
)

// fakeShell records the commands the healer runs and answers them from a script.
type fakeShell struct {
	ruleShow   string // what `ip rule show` returns
	rule6Show  string
	mangleHave map[string]bool // key: bin + args of a -C check that should succeed
	ran        []string
	failAdd    bool
}

func (f *fakeShell) run(name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	f.ran = append(f.ran, line)
	switch {
	case strings.Contains(line, "-6 rule show"):
		return []byte(f.rule6Show), nil
	case strings.Contains(line, "rule show"):
		return []byte(f.ruleShow), nil
	case strings.Contains(line, "-t mangle -C"):
		if f.mangleHave[line] {
			return nil, nil
		}
		return nil, fmt.Errorf("no such rule")
	}
	if f.failAdd {
		return nil, fmt.Errorf("add failed")
	}
	return nil, nil
}

func (f *fakeShell) install(t *testing.T) {
	t.Helper()
	prev := runCmd
	runCmd = f.run
	t.Cleanup(func() { runCmd = prev })
}

func healingServer() *Server {
	return &Server{InterfaceName: "awg0", IPv6Enabled: true, RouteViaXray: true, XrayTproxyPort: 12345}
}

// TestHealsMissingPolicyRule is the case seen in production: systemd-networkd
// reconfigured an interface and dropped the tunnel's routing policy rules,
// leaving the mangle rules marking packets that had nowhere to go.
func TestHealsMissingPolicyRule(t *testing.T) {
	f := &fakeShell{
		ruleShow:   "0:\tfrom all lookup local\n32766:\tfrom all lookup main\n",
		rule6Show:  "0:\tfrom all lookup local\n",
		mangleHave: map[string]bool{},
	}
	// The mangle rules are still in place, as networkd does not touch iptables.
	for _, r := range mangleChecks(tproxyParams(AWG, healingServer(), "awg0"), true) {
		f.mangleHave[r.bin+" -t mangle -C "+strings.Join(r.args, " ")] = true
	}
	f.install(t)

	repaired := EnsureTproxyRouting(AWG, healingServer())
	if len(repaired) != 2 {
		t.Fatalf("expected both policy rules restored, got %v", repaired)
	}
	joined := strings.Join(f.ran, "\n")
	if !strings.Contains(joined, "ip rule add fwmark 0x1/0x1 lookup 100") {
		t.Error("IPv4 policy rule was not re-added")
	}
	if !strings.Contains(joined, "ip -6 rule add fwmark 0x1/0x1 lookup 100") {
		t.Error("IPv6 policy rule was not re-added")
	}
	if strings.Contains(joined, "-t mangle -A") {
		t.Error("mangle rules were present but got added again")
	}
}

// TestNoRepairWhenHealthy: the check runs every 10 seconds, so it must do
// nothing at all when the wiring is intact — otherwise it would pile up
// duplicate rules.
func TestNoRepairWhenHealthy(t *testing.T) {
	srv := healingServer()
	f := &fakeShell{
		ruleShow:   "0:\tfrom all lookup local\n32765:\tfrom all fwmark 0x1/0x1 lookup 100\n",
		rule6Show:  "32765:\tfrom all fwmark 0x1/0x1 lookup 100\n",
		mangleHave: map[string]bool{},
	}
	for _, r := range mangleChecks(tproxyParams(AWG, srv, "awg0"), true) {
		f.mangleHave[r.bin+" -t mangle -C "+strings.Join(r.args, " ")] = true
	}
	f.install(t)

	if repaired := EnsureTproxyRouting(AWG, srv); len(repaired) != 0 {
		t.Fatalf("healthy wiring was 'repaired': %v", repaired)
	}
	for _, line := range f.ran {
		if strings.Contains(line, "rule add") || strings.Contains(line, "-A ") {
			t.Errorf("nothing should have been added, but ran: %s", line)
		}
	}
}

// TestSkipsTunnelsNotRoutedThroughXray keeps the check off the direct-mode path.
func TestSkipsTunnelsNotRoutedThroughXray(t *testing.T) {
	f := &fakeShell{}
	f.install(t)
	srv := healingServer()
	srv.RouteViaXray = false
	if repaired := EnsureTproxyRouting(AWG, srv); repaired != nil {
		t.Errorf("direct-mode tunnel touched: %v", repaired)
	}
	if len(f.ran) != 0 {
		t.Errorf("direct-mode tunnel ran commands: %v", f.ran)
	}
}

// TestUnreadableRuleTableIsLeftAlone: if `ip rule show` fails we cannot tell
// whether the rule exists, and adding blindly would duplicate it on every tick.
func TestUnreadableRuleTableIsLeftAlone(t *testing.T) {
	f := &fakeShell{ruleShow: "", rule6Show: ""}
	prev := runCmd
	runCmd = func(name string, args ...string) ([]byte, error) {
		line := name + " " + strings.Join(args, " ")
		f.ran = append(f.ran, line)
		if strings.Contains(line, "rule show") {
			return nil, fmt.Errorf("ip: command failed")
		}
		return nil, nil
	}
	t.Cleanup(func() { runCmd = prev })

	EnsureTproxyRouting(AWG, healingServer())
	for _, line := range f.ran {
		if strings.Contains(line, "rule add") {
			t.Errorf("added a policy rule despite not being able to read the table: %s", line)
		}
	}
}

// TestMangleRulesRestored covers the other half: a firewall flush that leaves
// the policy rules but drops the redirects.
func TestMangleRulesRestored(t *testing.T) {
	f := &fakeShell{
		ruleShow:   "32765:\tfrom all fwmark 0x1/0x1 lookup 100\n",
		rule6Show:  "32765:\tfrom all fwmark 0x1/0x1 lookup 100\n",
		mangleHave: map[string]bool{}, // nothing present
	}
	f.install(t)

	repaired := EnsureTproxyRouting(AWG, healingServer())
	if len(repaired) != 4 {
		t.Fatalf("expected 4 mangle rules restored (tcp/udp × v4/v6), got %v", repaired)
	}
	joined := strings.Join(f.ran, "\n")
	for _, want := range []string{
		"iptables -t mangle -A PREROUTING -i awg0 -p tcp",
		"iptables -t mangle -A PREROUTING -i awg0 -p udp",
		"ip6tables -t mangle -A PREROUTING -i awg0 -p tcp",
		"--on-port 12345",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}
