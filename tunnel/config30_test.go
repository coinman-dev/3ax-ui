package tunnel

import (
	"strings"
	"testing"
)

// TestConfigCarriesV3Parameters checks the exact spelling and placement of the
// 3.0 keys against what amneziawg-tools parses, and that both ends of the
// tunnel get them: HeaderProtectionKey has to match, and a client that
// obfuscates differently from its server is half the point of the exercise.
func TestConfigCarriesV3Parameters(t *testing.T) {
	srv := goldenServer(AWG)
	srv.S1, srv.S2, srv.S3, srv.S4 = 30, 40, 20, 16
	srv.I1, srv.I2, srv.I3, srv.I4, srv.I5 = "<r 128>", "<r 64>", "<b 0xf1>", "<c>", "<t>"
	srv.HeaderProtectionKey = "sVAr5W0dTv8XKQGmZTMr6bhVJVWQjMQ+9c1w5R9tzXo="
	srv.ContentPaddingAddition = "8-48"
	srv.RekeyAfterTime = "100-130"
	srv.RekeyTimeout = "4-7"
	srv.RejectAfterTime = "165-190"
	srv.KeepaliveTimeout = "8-12"
	srv.MaxHandshakeAttempts = "14-20"
	srv.RandomTrailers = true
	srv.DisableCookies = true

	want := []string{
		"I1 = <r 128>",
		"I2 = <r 64>",
		"I3 = <b 0xf1>",
		"I4 = <c>",
		"I5 = <t>",
		"HeaderProtectionKey = sVAr5W0dTv8XKQGmZTMr6bhVJVWQjMQ+9c1w5R9tzXo=",
		"ContentPaddingAddition = 8-48",
		"RekeyAfterTime = 100-130",
		"RekeyTimeout = 4-7",
		"RejectAfterTime = 165-190",
		"KeepaliveTimeout = 8-12",
		"MaxHandshakeAttempts = 14-20",
		"RandomTrailers = on",
		"DisableCookies = on",
	}

	for _, tc := range []struct {
		name string
		conf string
	}{
		{"server", GenerateServerConfig(AWG, srv, goldenClients())},
		{"client", GenerateClientConfig(AWG, srv, goldenClients()[0])},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertOrderedLines(t, tc.conf, want)
			// The keys belong to [Interface]; in a [Peer] the tools would
			// reject them outright.
			iface, _, _ := strings.Cut(tc.conf, "[Peer]")
			for _, line := range want {
				if !strings.Contains(iface, line) {
					t.Errorf("%q is not in the [Interface] section", line)
				}
			}
		})
	}
}

// TestConfigOmitsUnsetV3Parameters: a server that never touched 3.0 must
// produce exactly what it produced before 3.0 existed, or upgrading the panel
// would silently rewrite every live tunnel.
func TestConfigOmitsUnsetV3Parameters(t *testing.T) {
	srv := goldenServer(AWG)
	conf := GenerateServerConfig(AWG, srv, goldenClients())
	for _, key := range []string{
		"HeaderProtectionKey", "ContentPaddingAddition", "RekeyAfterTime",
		"RekeyTimeout", "RejectAfterTime", "KeepaliveTimeout",
		"MaxHandshakeAttempts", "RandomTrailers", "DisableCookies",
		"I1", "I2", "I3", "I4", "I5",
	} {
		if strings.Contains(conf, key) {
			t.Errorf("unset %s was written to the config anyway", key)
		}
	}
}

// TestNativeWireGuardIgnoresV3: the obfuscation fields exist on the shared
// record, so a WireGuard server could carry them — its config must not.
func TestNativeWireGuardIgnoresV3(t *testing.T) {
	srv := goldenServer(WG)
	srv.HeaderProtectionKey = "sVAr5W0dTv8XKQGmZTMr6bhVJVWQjMQ+9c1w5R9tzXo="
	srv.RandomTrailers = true
	srv.RekeyAfterTime = "100-130"

	conf := GenerateServerConfig(WG, srv, goldenClients())
	for _, key := range []string{"HeaderProtectionKey", "RandomTrailers", "RekeyAfterTime"} {
		if strings.Contains(conf, key) {
			t.Errorf("%s leaked into a native WireGuard config", key)
		}
	}
}

// assertOrderedLines checks that every wanted line appears, in this order.
func assertOrderedLines(t *testing.T, conf string, want []string) {
	t.Helper()
	rest := conf
	for _, line := range want {
		idx := strings.Index(rest, line+"\n")
		if idx < 0 {
			if strings.Contains(conf, line) {
				t.Errorf("%q appears out of order", line)
			} else {
				t.Errorf("%q is missing from the config", line)
			}
			continue
		}
		rest = rest[idx+len(line):]
	}
}
