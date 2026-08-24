package mtproto

import (
	"strings"
	"testing"
)

// TestValidateSettingsRejectsWhatMtgWouldRefuse: mtg reads its config once, at
// startup, and exits on a bad value. Every case here is one the panel must
// catch while the operator can still see the field, instead of leaving them a
// proxy that silently will not start.
func TestValidateSettingsRejectsWhatMtgWouldRefuse(t *testing.T) {
	bad := map[string]string{
		"prefer-ip typo":       `{"preferIp":"ipv4"}`,
		"negative concurrency": `{"concurrency":-1}`,
		"ipv4 field holds v6":  `{"publicIpv4":"2001:db8::1"}`,
		"ipv6 field holds v4":  `{"publicIpv6":"1.2.3.4"}`,
		"not an address":       `{"publicIpv4":"example.com"}`,
		"skew not a duration":  `{"tolerateTimeSkewness":"5"}`,
		"raid not a duration":  `{"doppelganger":{"raidEach":"often"}}`,
		"negative repeats":     `{"doppelganger":{"repeatsPerRaid":-2}}`,
		"resolver scheme":      `{"dns":"ftp://1.1.1.1"}`,
		"resolver not an ip":   `{"dns":"udp://example.com"}`,
		"fronting port range":  `{"domainFronting":{"port":70000}}`,
		"fronting host quoted": `{"domainFronting":{"host":"a b"}}`,
		"doppelganger http":    `{"doppelganger":{"urls":["http://example.com/a.js"]}}`,
		"doppelganger garbage": `{"doppelganger":{"urls":["not a url"]}}`,
		"blocklist garbage":    `{"blocklist":{"urls":["nope"]}}`,
	}
	for name, settings := range bad {
		if err := ValidateSettings(settings); err == nil {
			t.Errorf("%s: accepted %s", name, settings)
		}
	}

	good := map[string]string{
		"empty":            `{}`,
		"only the domain":  `{"fakeTlsDomain":"www.cloudflare.com"}`,
		"full set":         `{"preferIp":"only-ipv4","concurrency":4096,"publicIpv4":"1.2.3.4","publicIpv6":"2001:db8::1","tolerateTimeSkewness":"10s","dns":"https://1.1.1.1","domainFronting":{"host":"127.0.0.1","port":9443,"proxyProtocol":true},"doppelganger":{"urls":["https://cdn.example.com/a.js"],"repeatsPerRaid":10,"raidEach":"6h","drs":true},"blocklist":{"enabled":true,"urls":["https://iplists.firehol.org/files/firehol_level1.netset"]}}`,
		"dot resolver":     `{"dns":"tls://1.1.1.1"}`,
		"plain resolver":   `{"dns":"9.9.9.9"}`,
		"local blocklist":  `{"blocklist":{"urls":["/etc/x-ui/blocklist.netset"]}}`,
		"blank list lines": `{"doppelganger":{"urls":["","   "]}}`,
		"not our shape":    `{"clients":[{"email":"a"}]}`,
	}
	for name, settings := range good {
		if err := ValidateSettings(settings); err != nil {
			t.Errorf("%s: rejected %s: %v", name, settings, err)
		}
	}
}

// TestValidateSettingsNamesTheField: an error the operator cannot act on is
// barely better than no error.
func TestValidateSettingsNamesTheField(t *testing.T) {
	cases := map[string]string{
		`{"preferIp":"ipv4"}`:                         "prefer-ip",
		`{"doppelganger":{"raidEach":"often"}}`:       "raid",
		`{"publicIpv6":"1.2.3.4"}`:                    "IPv6",
		`{"blocklist":{"urls":["nope"]}}`:             "blocklist",
		`{"dns":"ftp://1.1.1.1"}`:                     "DNS",
		`{"domainFronting":{"port":70000}}`:           "fronting port",
		`{"doppelganger":{"urls":["http://a/b.js"]}}`: "doppelganger",
	}
	for settings, want := range cases {
		err := ValidateSettings(settings)
		if err == nil {
			t.Errorf("%s was accepted", settings)
			continue
		}
		if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
			t.Errorf("error for %s should mention %q, got %q", settings, want, err)
		}
	}
}
