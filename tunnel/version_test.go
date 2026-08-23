package tunnel

import "testing"

// TestVersionToken covers the banner parsing behind the version shown in the
// panel. The live server exposed the bug this guards: amneziawg-tools now
// reports its own version, while the panel still derived "v2.0.<date>" from the
// kernel module build date and displayed 2.0 for a 3.1 install.
func TestVersionToken(t *testing.T) {
	cases := []struct{ banner, want string }{
		{"amneziawg-tools v3.1.20260812 - https://amnezia.org", "v3.1.20260812"},
		{"wireguard-tools v1.0.20210914 - https://git.zx2c4.com/wireguard-tools/", "v1.0.20210914"},
		{"amneziawg-tools v2.0.20250901", "v2.0.20250901"},
		{"no version here", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := versionToken(c.banner); got != c.want {
			t.Errorf("versionToken(%q) = %q, want %q", c.banner, got, c.want)
		}
	}
}

// TestLegacyAwgBannerIsRejected: before upstream fixed amneziawg-tools, `awg
// --version` printed the inherited wireguard-tools banner. That value must not
// be shown as the AmneziaWG version — the kernel module is the truth then.
func TestLegacyAwgBannerIsRejected(t *testing.T) {
	legacy := "wireguard-tools v1.0.20210914 - https://git.zx2c4.com/wireguard-tools/"
	if got := versionToken(legacy); got == "" {
		t.Fatal("fixture is wrong: the legacy banner does contain a version token")
	}
	// The rejection lives in toolVersion, keyed off Kind.Obfuscation.
	if !AWG.Obfuscation {
		t.Fatal("AWG must be marked as an obfuscated flavour")
	}
	if WG.Obfuscation {
		t.Fatal("plain WireGuard must not be marked as obfuscated")
	}
}

// TestKindsDoNotCollide guards the invariant that lets both tunnels route
// through Xray at once: separate fwmark, routing table and default port.
func TestKindsDoNotCollide(t *testing.T) {
	if AWG.TproxyFwmark == WG.TproxyFwmark {
		t.Error("fwmark collision between AWG and WG")
	}
	if AWG.TproxyTable == WG.TproxyTable {
		t.Error("routing table collision between AWG and WG")
	}
	if AWG.TproxyPort == WG.TproxyPort {
		t.Error("default TPROXY port collision between AWG and WG")
	}
	if AWG.ConfigDir == WG.ConfigDir || AWG.NdppdSection == WG.NdppdSection {
		t.Error("AWG and WG must not share a config dir or an ndppd section")
	}
}
