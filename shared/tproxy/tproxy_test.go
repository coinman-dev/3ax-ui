package tproxy

import (
	"strings"
	"testing"
)

// awgParams / wgParams mirror what awg/config.go and wg/config.go pass in.
func awgParams() Params {
	return Params{Interface: "awg0", Fwmark: "0x1", Table: "100", DefaultPort: 12345, IPv6: true}
}

func wgParams() Params {
	return Params{Interface: "wg0", Fwmark: "0x2", Table: "101", DefaultPort: 12346, IPv6: false}
}

// TestPostUpLinesGolden pins the exact rules, in order, that the two tunnel
// packages emitted before this logic was shared. A silent change here would
// send tunnel traffic to the wrong place or leave rules behind.
func TestPostUpLinesGolden(t *testing.T) {
	want := []string{
		"while ip rule del fwmark 0x1/0x1 lookup 100 2>/dev/null; do :; done",
		"ip rule add fwmark 0x1/0x1 lookup 100",
		"ip route replace local default dev lo table 100",
		"iptables -t mangle -A PREROUTING -i awg0 -p tcp -j TPROXY --on-ip 127.0.0.1 --on-port 12345 --tproxy-mark 0x1/0x1",
		"iptables -t mangle -A PREROUTING -i awg0 -p udp -j TPROXY --on-ip 127.0.0.1 --on-port 12345 --tproxy-mark 0x1/0x1",
		"while ip -6 rule del fwmark 0x1/0x1 lookup 100 2>/dev/null; do :; done",
		"ip -6 rule add fwmark 0x1/0x1 lookup 100",
		"ip -6 route replace local default dev lo table 100",
		"ip6tables -t mangle -A PREROUTING -i awg0 -p tcp -j TPROXY --on-ip ::1 --on-port 12345 --tproxy-mark 0x1/0x1",
		"ip6tables -t mangle -A PREROUTING -i awg0 -p udp -j TPROXY --on-ip ::1 --on-port 12345 --tproxy-mark 0x1/0x1",
	}
	assertLines(t, PostUpLines(awgParams()), want)
}

// TestPostDownLinesOrder locks the teardown order: mangle rules first, policy
// route and rule last, so nothing is marked after its route is gone.
func TestPostDownLinesGolden(t *testing.T) {
	want := []string{
		"iptables -t mangle -D PREROUTING -i awg0 -p tcp -j TPROXY --on-ip 127.0.0.1 --on-port 12345 --tproxy-mark 0x1/0x1",
		"iptables -t mangle -D PREROUTING -i awg0 -p udp -j TPROXY --on-ip 127.0.0.1 --on-port 12345 --tproxy-mark 0x1/0x1",
		"ip route del local default dev lo table 100",
		"while ip rule del fwmark 0x1/0x1 lookup 100 2>/dev/null; do :; done",
		"ip6tables -t mangle -D PREROUTING -i awg0 -p tcp -j TPROXY --on-ip ::1 --on-port 12345 --tproxy-mark 0x1/0x1",
		"ip6tables -t mangle -D PREROUTING -i awg0 -p udp -j TPROXY --on-ip ::1 --on-port 12345 --tproxy-mark 0x1/0x1",
		"ip -6 route del local default dev lo table 100",
		"while ip -6 rule del fwmark 0x1/0x1 lookup 100 2>/dev/null; do :; done",
	}
	assertLines(t, PostDownLines(awgParams()), want)
}

// TestIPv4OnlyAndNamespacing checks the WG side: its own fwmark/table/port and
// no IPv6 rules when the tunnel is v4-only. Overlapping marks would make the
// two tunnels fight over the same policy route.
func TestIPv4OnlyAndNamespacing(t *testing.T) {
	lines := PostUpLines(wgParams())
	if len(lines) != 5 {
		t.Fatalf("v4-only tunnel emitted %d rules, want 5: %v", len(lines), lines)
	}
	joined := strings.Join(lines, "\n")
	for _, forbidden := range []string{"ip -6", "ip6tables", "::1"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("IPv6 rule %q emitted for a v4-only tunnel", forbidden)
		}
	}
	for _, want := range []string{"0x2/0x2", "lookup 101", "--on-port 12346", "-i wg0"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "0x1/0x1") || strings.Contains(joined, "lookup 100") {
		t.Error("WG rules must not reuse the AWG fwmark/table")
	}
}

// TestExplicitPortWins covers the configured-port path.
func TestExplicitPortWins(t *testing.T) {
	p := awgParams()
	p.Port = 23456
	joined := strings.Join(PostUpLines(p), "\n")
	if !strings.Contains(joined, "--on-port 23456") || strings.Contains(joined, "--on-port 12345") {
		t.Errorf("configured port ignored:\n%s", joined)
	}
}

func assertLines(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d rules, want %d:\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rule %d:\n got: %s\nwant: %s", i, got[i], want[i])
		}
	}
}

// TestPostUpIsIdempotent is the fix for a live failure: `ip route add` returns
// EEXIST when the route is already there, PostUp runs under `set -e`, and
// wg-quick answers a single "RTNETLINK answers: File exists" by tearing the
// whole interface down. The route survives a PostDown that did not finish, and
// the healing job puts it back on its own — so "already there" is the normal
// case, not the exception.
//
// `ip rule add` has the opposite fault: it never fails, so repeated bring-ups
// pile up identical rules. Three had accumulated on the server where this was
// found.
func TestPostUpIsIdempotent(t *testing.T) {
	for _, p := range []Params{awgParams(), wgParams()} {
		for _, line := range PostUpLines(p) {
			if strings.HasPrefix(line, "ip route add") || strings.HasPrefix(line, "ip -6 route add") {
				t.Errorf("%q fails with EEXIST on a second bring-up; use replace", line)
			}
		}
		joined := strings.Join(PostUpLines(p), "\n")
		// Every add of a policy rule has to be preceded by a drain of the same
		// rule, or the duplicates come back.
		adds := strings.Count(joined, "rule add fwmark")
		drains := strings.Count(joined, "rule del fwmark")
		if adds != drains {
			t.Errorf("%d policy-rule adds against %d drains:\n%s", adds, drains, joined)
		}
	}
}
