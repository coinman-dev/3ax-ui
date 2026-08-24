package nginx

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeTables records every netfilter command and answers the checks the code
// makes. Nothing here touches a real table.
type fakeTables struct {
	ran []string
	// has answers `-C`: a rule the code asks about that is in here already
	// exists, everything else does not.
	has map[string]bool
	// fail makes the first command containing this substring return an error.
	fail string
}

func (f *fakeTables) run(name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	f.ran = append(f.ran, line)
	if f.fail != "" && strings.Contains(line, f.fail) {
		return []byte("boom"), fmt.Errorf("failed")
	}
	if slices.Contains(args, "-C") {
		if f.has[line] {
			return nil, nil
		}
		return nil, fmt.Errorf("no such rule")
	}
	return nil, nil
}

func (f *fakeTables) install(t *testing.T) {
	t.Helper()
	if f.has == nil {
		f.has = map[string]bool{}
	}
	prev := runFirewall
	runFirewall = f.run
	t.Cleanup(func() { runFirewall = prev })
}

// added returns the rules appended to our chain by the given binary, in order,
// with the "iptables -A THREEAX-IN " prefix stripped.
func (f *fakeTables) added(bin string) []string {
	prefix := bin + " -A " + Chain + " "
	var out []string
	for _, line := range f.ran {
		if strings.HasPrefix(line, prefix) {
			out = append(out, strings.TrimPrefix(line, prefix))
		}
	}
	return out
}

// sshConfig points SSHPorts at a directory of the test's own making.
func sshConfig(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sshd_config.d"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	prev := sshConfigDir
	sshConfigDir = dir
	t.Cleanup(func() { sshConfigDir = prev })
}

// TestSSHIsNeverClosed is the rule the whole file exists to keep. A caller that
// forgets SSH entirely must still end up with it open — a panel that can lock
// the operator out of their own server over a UI toggle is not a feature.
func TestSSHIsNeverClosed(t *testing.T) {
	sshConfig(t, map[string]string{"sshd_config": "Port 2222\n"})
	f := &fakeTables{}
	f.install(t)

	if err := ApplyFirewall(Firewall{TCP: []int{443}}); err != nil {
		t.Fatalf("ApplyFirewall: %v", err)
	}

	rules := f.added("iptables")
	if len(rules) == 0 {
		t.Fatal("no rules were written at all")
	}
	want := "-p tcp --dport 2222 -j RETURN"
	if !slices.Contains(rules, want) {
		t.Fatalf("SSH was not let through; chain is:\n  %s", strings.Join(rules, "\n  "))
	}
	// And it has to come before the DROP, or it is not let through at all.
	drop := slices.IndexFunc(rules, func(r string) bool { return strings.HasSuffix(r, "-j DROP") })
	if drop < 0 {
		t.Fatal("the chain never drops anything, so nothing is closed")
	}
	if slices.Index(rules, want) > drop {
		t.Error("SSH is allowed only after the DROP, which never happens")
	}
	if drop != len(rules)-1 {
		t.Errorf("DROP is at %d of %d rules, it has to be last", drop, len(rules)-1)
	}
}

func TestSSHPorts(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  []int
	}{
		{
			name:  "nothing configured means the default",
			files: map[string]string{"sshd_config": "#Port 22\nPermitRootLogin no\n"},
			want:  []int{22},
		},
		{
			name:  "no config at all still means the default",
			files: map[string]string{},
			want:  []int{22},
		},
		{
			name:  "a moved port",
			files: map[string]string{"sshd_config": "Port 2222\n"},
			want:  []int{2222},
		},
		{
			name: "the drop-in directory counts too",
			files: map[string]string{
				"sshd_config":                      "Port 22\n",
				"sshd_config.d/50-cloud-init.conf": "Port 2222\n",
			},
			want: []int{22, 2222},
		},
		{
			name:  "ListenAddress carries a port",
			files: map[string]string{"sshd_config": "ListenAddress 0.0.0.0:2022\nListenAddress ::\n"},
			want:  []int{2022},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sshConfig(t, tc.files)
			if got := SSHPorts(); !slices.Equal(got, tc.want) {
				t.Errorf("SSHPorts() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestChainLetsTheMachineKeepWorking guards the rules that are not about our
// ports at all: drop these and the server breaks in ways that look like
// anything but a firewall.
func TestChainLetsTheMachineKeepWorking(t *testing.T) {
	sshConfig(t, map[string]string{"sshd_config": "Port 22\n"})
	f := &fakeTables{}
	f.install(t)

	if err := ApplyFirewall(Firewall{TCP: []int{443}, UDP: []int{55200}}); err != nil {
		t.Fatalf("ApplyFirewall: %v", err)
	}

	v4 := f.added("iptables")
	v6 := f.added("ip6tables")
	if len(v6) == 0 {
		t.Skip("no ip6tables on this machine, so the IPv6 half was not exercised")
	}

	for _, want := range []string{
		"-i lo -j RETURN",
		"-m conntrack --ctstate RELATED,ESTABLISHED -j RETURN",
		"-p tcp --dport 443 -j RETURN",
		"-p udp --dport 55200 -j RETURN",
	} {
		if !slices.Contains(v4, want) {
			t.Errorf("missing from the IPv4 chain: %s", want)
		}
		if !slices.Contains(v6, want) {
			t.Errorf("missing from the IPv6 chain: %s", want)
		}
	}
	if !slices.Contains(v4, "-p icmp -j RETURN") {
		t.Error("IPv4 drops ICMP, which breaks path MTU discovery")
	}
	// Neighbour discovery lives in ICMPv6. Drop it and IPv6 simply stops.
	if !slices.Contains(v6, "-p ipv6-icmp -j RETURN") {
		t.Error("IPv6 drops ICMPv6, which breaks IPv6 entirely")
	}
	if slices.Contains(v6, "-p icmp -j RETURN") {
		t.Error("the IPv6 chain matches on ICMP, which ip6tables does not have")
	}
}

// TestHookGoesInFirstAndOnlyOnce: a DROP placed after somebody else's ACCEPT is
// never reached, and a jump added twice on every reconcile would pile up.
func TestHookGoesInFirstAndOnlyOnce(t *testing.T) {
	sshConfig(t, map[string]string{"sshd_config": "Port 22\n"})
	f := &fakeTables{}
	f.install(t)

	if err := ApplyFirewall(Firewall{TCP: []int{443}}); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	joined := strings.Join(f.ran, "\n")
	if !strings.Contains(joined, "iptables -I INPUT 1 -j "+Chain) {
		t.Fatalf("the chain was not hooked into INPUT first:\n%s", joined)
	}

	// Second run: the hook is there now, so it must not be added again.
	f.has["iptables -C INPUT -j "+Chain] = true
	f.has["ip6tables -C INPUT -j "+Chain] = true
	f.ran = nil
	if err := ApplyFirewall(Firewall{TCP: []int{443}}); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if strings.Contains(strings.Join(f.ran, "\n"), "-I INPUT") {
		t.Error("the jump was added a second time")
	}
	// The chain itself is still rebuilt, so the rules follow the settings.
	if !strings.Contains(strings.Join(f.ran, "\n"), "iptables -F "+Chain) {
		t.Error("the chain was not rebuilt on the second run")
	}
}

// TestRebuildFailsBeforeItCloses: if a rule cannot be written, the chain must
// not end up hooked. A half-built chain that drops everything is exactly the
// lockout this file is meant to prevent.
func TestRebuildFailsBeforeItCloses(t *testing.T) {
	sshConfig(t, map[string]string{"sshd_config": "Port 22\n"})
	f := &fakeTables{fail: "conntrack"}
	f.install(t)

	if err := ApplyFirewall(Firewall{TCP: []int{443}}); err == nil {
		t.Fatal("a failed rule was reported as success")
	}
	if strings.Contains(strings.Join(f.ran, "\n"), "-I INPUT") {
		t.Error("the chain was hooked into INPUT even though building it failed")
	}
}

// TestRemoveTakesTheChainOut and does not mind it being gone already: the
// operator asked for the ports to be open, and afterwards they are.
func TestRemoveTakesTheChainOut(t *testing.T) {
	f := &fakeTables{}
	f.install(t)

	if err := RemoveFirewall(); err != nil {
		t.Fatalf("RemoveFirewall: %v", err)
	}
	joined := strings.Join(f.ran, "\n")
	for _, want := range []string{"-D INPUT -j " + Chain, "-F " + Chain, "-X " + Chain} {
		if !strings.Contains(joined, want) {
			t.Errorf("remove never ran: %s", want)
		}
	}
}

// TestRulesAreAcceptedByIptables builds the chain for real and deletes it
// again. The unit tests above check what we mean to write; this checks that
// iptables agrees it is writable — a missing conntrack or comment match shows
// up here and nowhere else.
//
// The chain is never hooked into INPUT, so it affects no packet. Skipped
// unless there is an iptables to talk to and the rights to use it.
func TestRulesAreAcceptedByIptables(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to touch netfilter")
	}
	for _, fam := range families() {
		if !fam.usable {
			continue
		}
		t.Run(fam.bin, func(t *testing.T) {
			chain := Chain + "-SYNTAX"
			run := func(args ...string) ([]byte, error) { return runFirewall(fam.bin, args...) }
			if out, err := run("-N", chain); err != nil {
				t.Fatalf("create %s: %v: %s", chain, err, out)
			}
			t.Cleanup(func() {
				_, _ = run("-F", chain)
				_, _ = run("-X", chain)
			})
			for _, rule := range fam.rules([]int{22, 443}, []int{55200}) {
				if out, err := run(append([]string{"-A", chain}, rule...)...); err != nil {
					t.Errorf("%s: %v: %s", strings.Join(rule, " "), err, strings.TrimSpace(string(out)))
				}
			}
		})
	}
}
