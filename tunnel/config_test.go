package tunnel

import (
	"os"
	"path/filepath"
	"testing"
)

// The fixtures below mirror the live AmneziaWG server on the production panel:
// IPv6 enabled with a routed /112, three clients of which one has a forwarded
// port and one is disabled.
func goldenServer(k Kind) *Server {
	s := &Server{
		Id: 1, Enable: true, InterfaceName: k.DefaultIface, ListenPort: 55200, MTU: 1420,
		PrivateKey: "SERVER_PRIV", PublicKey: "SERVER_PUB",
		IPv4Address: "10.66.66.1/24", IPv4Pool: "10.66.66.0/24",
		IPv6Enabled: true, IPv6Address: "2a00:7c80:0:1d8:a00::1/112",
		IPv6Pool: "2a00:7c80:0:1d8:a00::/112", IPv6Gateway: "2a00:7c80:0:1d8::1",
		DnsIpv4: "1.1.1.1", DnsIpv6: "2606:4700:4700::1111",
		ExternalInterface: "eth0", IPv6ExternalInterface: "eth1",
		Endpoint: "vpn.example.net", TrafficReset: "never",
		RouteViaXray: false, XrayInboundTag: k.Name + "-tproxy-in", XrayTproxyPort: k.TproxyPort,
	}
	if k.Obfuscation {
		s.Jc, s.Jmin, s.Jmax = 4, 50, 1000
		s.H1, s.H2, s.H3, s.H4 = "1", "2", "3", "4"
	}
	return s
}

func goldenClients() []Client {
	return []Client{
		{
			Id: 1, ServerId: 1, UUID: "uuid-1", Name: "alice", Email: "alice", Enable: true,
			PrivateKey: "C1_PRIV", PublicKey: "C1_PUB", PresharedKey: "C1_PSK",
			IPv4Address: "10.66.66.2/32", IPv6Address: "2a00:7c80:0:1d8:a00::2/128",
			AllowedIPs:       "10.66.66.2/32, 2a00:7c80:0:1d8:a00::2/128",
			ClientAllowedIPs: "0.0.0.0/0,::/0", PersistentKeepalive: 25,
		},
		{
			Id: 2, ServerId: 1, UUID: "uuid-2", Name: "bob", Email: "bob", Enable: true,
			PrivateKey: "C2_PRIV", PublicKey: "C2_PUB",
			IPv4Address: "10.66.66.3/32", IPv6Address: "2a00:7c80:0:1d8:a00::3/128",
			AllowedIPs: "10.66.66.3/32", ClientAllowedIPs: "0.0.0.0/0",
			ForwardedPorts: "37015", PersistentKeepalive: 25,
		},
		{
			Id: 3, ServerId: 1, UUID: "uuid-3", Name: "carol", Email: "carol", Enable: false,
			PrivateKey: "C3_PRIV", PublicKey: "C3_PUB",
			IPv4Address: "10.66.66.4/32", IPv6Address: "2a00:7c80:0:1d8:a00::4/128",
			AllowedIPs: "10.66.66.4/32", ClientAllowedIPs: "0.0.0.0/0",
		},
	}
}

// TestGeneratorsMatchPreMergeGoldens is the safety net for merging the awg and
// wg packages into this one: every generated file must be byte-identical to
// what the two separate generators produced before the merge. A single changed
// iptables rule or a dropped obfuscation line would otherwise reach a live
// server unnoticed. See testdata/README.md — these goldens are frozen.
func TestGeneratorsMatchPreMergeGoldens(t *testing.T) {
	for _, k := range []Kind{AWG, WG} {
		t.Run(k.Name, func(t *testing.T) {
			clients := goldenClients()

			srv := goldenServer(k)
			check(t, k.Name+"_server.conf", GenerateServerConfig(k, srv, clients))
			check(t, k.Name+"_client.conf", GenerateClientConfig(k, srv, clients[0]))
			check(t, k.Name+"_postup_direct.txt", GenerateDefaultPostUp(k, srv, clients))
			check(t, k.Name+"_postdown_direct.txt", GenerateDefaultPostDown(k, srv, clients))

			srv.RouteViaXray = true
			check(t, k.Name+"_postup_xray.txt", GenerateDefaultPostUp(k, srv, clients))
			check(t, k.Name+"_postdown_xray.txt", GenerateDefaultPostDown(k, srv, clients))
			check(t, k.Name+"_server_xray.conf", GenerateServerConfig(k, srv, clients))

			srv.IPv6Enabled = false
			srv.RouteViaXray = false
			check(t, k.Name+"_server_v4only.conf", GenerateServerConfig(k, srv, clients))
			check(t, k.Name+"_postup_v4only.txt", GenerateDefaultPostUp(k, srv, clients))
		})
	}
}

func check(t *testing.T, name, got string) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	if got == string(want) {
		return
	}
	t.Errorf("%s differs from the pre-merge output", name)
	gotLines, wantLines := splitLines(got), splitLines(string(want))
	for i := range max(len(gotLines), len(wantLines)) {
		g, w := at(gotLines, i), at(wantLines, i)
		if g != w {
			t.Errorf("  строка %d:\n    было:  %s\n    стало: %s", i+1, trunc(w), trunc(g))
		}
	}
}

func splitLines(s string) []string {
	var out []string
	for line := range splitSeq(s) {
		out = append(out, line)
	}
	return out
}

func splitSeq(s string) func(func(string) bool) {
	return func(yield func(string) bool) {
		start := 0
		for i := range len(s) {
			if s[i] == '\n' {
				if !yield(s[start:i]) {
					return
				}
				start = i + 1
			}
		}
		if start < len(s) {
			yield(s[start:])
		}
	}
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "(нет строки)"
}

func trunc(s string) string {
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
