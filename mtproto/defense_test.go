package mtproto

import (
	"strings"
	"testing"
)

func fullDefenseInstance() Instance {
	return Instance{
		Clients: []ClientSecret{{Id: "u1", Secret: "eeAA", Email: "alice"}}, MultiUser: true,
		Listen: "0.0.0.0", Port: 8443,
		PreferIP: "prefer-ipv4", Concurrency: 4096,
		PublicIPv4: "1.2.3.4", PublicIPv6: "2001:db8::1",
		TolerateTimeSkewness: "10s", DNS: "https://1.1.1.1",
		FrontingHost: "127.0.0.1", FrontingPort: 9443, FrontingProxyProtocol: true,
		DoppelgangerURLs:    []string{"https://cdn.example.com/a.js", "https://cdn.example.com/b.css"},
		DoppelgangerRepeats: 10, DoppelgangerRaidEach: "6h", DoppelgangerDRS: true,
		BlocklistURLs: []string{"https://iplists.firehol.org/files/firehol_abusers_1d.netset"},
	}
}

// TestRenderConfigWritesDefences pins the anti-blocking half of the generated
// config: the exact mtg key names, and the values in the sections mtg expects
// them in. A key in the wrong section is silently ignored by mtg, which reads
// as "the setting does nothing" on a live server.
func TestRenderConfigWritesDefences(t *testing.T) {
	got := renderConfig(fullDefenseInstance(), 7000)
	for _, want := range []string{
		"prefer-ip = \"prefer-ipv4\"",
		"concurrency = 4096",
		"public-ipv4 = \"1.2.3.4\"",
		"public-ipv6 = \"2001:db8::1\"",
		"tolerate-time-skewness = \"10s\"",
		"[domain-fronting]",
		"host = \"127.0.0.1\"",
		"port = 9443",
		"proxy-protocol = true",
		"[network]",
		"dns = \"https://1.1.1.1\"",
		"[defense.doppelganger]",
		"urls = [\"https://cdn.example.com/a.js\", \"https://cdn.example.com/b.css\"]",
		"repeats-per-raid = 10",
		"raid-each = \"6h\"",
		"drs = true",
		"[defense.blocklist]",
		"urls = [\"https://iplists.firehol.org/files/firehol_abusers_1d.netset\"]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// mtg deprecated `ip` — it warns and ignores the value, so writing it would
	// silently leave domain fronting off.
	// Anchored to a line start: "prefer-ip = ..." also ends in "ip = ".
	if strings.Contains("\n"+got, "\nip = ") {
		t.Errorf("the deprecated fronting `ip` key was written:\n%s", got)
	}
}

// TestGlobalKeysPrecedeSecrets: TOML puts every key after a [section] header
// inside that table, so a global written below [secrets] would become a secret
// named e.g. "concurrency" — mtg then rejects the config and the proxy stays
// down.
func TestGlobalKeysPrecedeSecrets(t *testing.T) {
	got := renderConfig(fullDefenseInstance(), 7000)
	secrets := strings.Index(got, "[secrets]")
	if secrets < 0 {
		t.Fatal("multi-user config must have a [secrets] section")
	}
	for _, global := range []string{
		"api-bind-to", "bind-to", "prefer-ip", "concurrency",
		"public-ipv4", "public-ipv6", "tolerate-time-skewness",
	} {
		if idx := strings.Index(got, global); idx < 0 || idx > secrets {
			t.Errorf("global %q must appear before [secrets] (at %d, section at %d)", global, idx, secrets)
		}
	}
}

// TestDefencesStayDefaultWhenUnset: both defences are on by default in mtg, and
// a config that says nothing keeps them that way. Writing "enabled = true"
// would be noise; writing nothing when the operator asked for "off" would
// silently ignore them.
func TestDefencesStayDefaultWhenUnset(t *testing.T) {
	quiet := renderConfig(Instance{
		Clients: []ClientSecret{{Id: "u1", Secret: "ee", Email: "a"}},
		Listen:  "0.0.0.0", Port: 443,
	}, 7000)
	for _, unwanted := range []string{"[defense.", "concurrency", "public-ipv", "dns =", "tolerate-time-skewness"} {
		if strings.Contains(quiet, unwanted) {
			t.Errorf("unset option %q was written:\n%s", unwanted, quiet)
		}
	}

	off := fullDefenseInstance()
	off.AntiReplayDisabled = true
	off.BlocklistDisabled = true
	got := renderConfig(off, 7000)
	if !strings.Contains(got, "[defense.anti-replay]\nenabled = false") {
		t.Errorf("anti-replay was not turned off:\n%s", got)
	}
	if !strings.Contains(got, "[defense.blocklist]\nenabled = false") {
		t.Errorf("blocklist was not turned off:\n%s", got)
	}
	// A disabled blocklist has no use for a URL list.
	if strings.Contains(got, "firehol") {
		t.Errorf("a disabled blocklist should not carry urls:\n%s", got)
	}
}

// TestNetworkSectionCombinesDnsAndProxy: both live in [network], and a second
// header would make the first one's keys unreachable.
func TestNetworkSectionCombinesDnsAndProxy(t *testing.T) {
	inst := fullDefenseInstance()
	inst.RouteThroughXray = true
	inst.XrayRoutePort = 50000
	got := renderConfig(inst, 7000)
	if n := strings.Count(got, "[network]"); n != 1 {
		t.Fatalf("expected exactly one [network] section, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "dns = \"https://1.1.1.1\"") ||
		!strings.Contains(got, "proxies = [\"socks5://127.0.0.1:50000\"]") {
		t.Errorf("dns and proxy must share the section:\n%s", got)
	}
}
