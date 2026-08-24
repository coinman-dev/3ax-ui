package sub

import (
	"testing"

	"github.com/coinman-dev/3ax-ui/v2/database/model"
)

// TestResolveInboundAddress guards what a client is actually handed.
//
// Once an inbound moves behind the nginx front-end its listen address becomes
// 127.0.0.1, and a link built from that reads
// vless://…@127.0.0.1:443 — which every client dutifully tries to connect to,
// on itself.
func TestResolveInboundAddress(t *testing.T) {
	s := &SubService{address: "net-ru.modulator.net"}

	cases := []struct {
		name   string
		listen string
		want   string
	}{
		{"behind nginx", "127.0.0.1", "net-ru.modulator.net"},
		{"behind nginx over IPv6", "::1", "net-ru.modulator.net"},
		{"localhost by name", "localhost", "net-ru.modulator.net"},
		{"all interfaces", "", "net-ru.modulator.net"},
		{"all interfaces, spelled out", "0.0.0.0", "net-ru.modulator.net"},
		{"all interfaces, IPv6", "::", "net-ru.modulator.net"},
		{"padded", "  127.0.0.1  ", "net-ru.modulator.net"},
		// A real address the operator chose is still the right answer.
		{"one interface of several", "91.142.78.153", "91.142.78.153"},
		{"a second domain", "vpn.example.net", "vpn.example.net"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s.resolveInboundAddress(&model.Inbound{Listen: tc.listen})
			if got != tc.want {
				t.Errorf("listen %q produced the address %q, want %q", tc.listen, got, tc.want)
			}
		})
	}
}
