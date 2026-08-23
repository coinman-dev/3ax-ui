package mtproto

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// ValidateSettings rejects an mtproto inbound's settings before they are saved.
//
// mtg reads its config once at startup: a malformed duration or an unknown
// prefer-ip value makes the sidecar exit immediately, and all the panel can
// show afterwards is a proxy that will not start. Catching it here means the
// operator sees which field is wrong while they are still looking at it.
func ValidateSettings(settings string) error {
	var parsed struct {
		PreferIP             string `json:"preferIp"`
		Concurrency          int    `json:"concurrency"`
		PublicIPv4           string `json:"publicIpv4"`
		PublicIPv6           string `json:"publicIpv6"`
		TolerateTimeSkewness string `json:"tolerateTimeSkewness"`
		DNS                  string `json:"dns"`
		DomainFronting       struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		} `json:"domainFronting"`
		Doppelganger struct {
			URLs           []string `json:"urls"`
			RepeatsPerRaid int      `json:"repeatsPerRaid"`
			RaidEach       string   `json:"raidEach"`
		} `json:"doppelganger"`
		Blocklist struct {
			URLs []string `json:"urls"`
		} `json:"blocklist"`
	}
	if err := json.Unmarshal([]byte(settings), &parsed); err != nil {
		return nil // not our shape; the caller's own decode reports it
	}

	switch parsed.PreferIP {
	case "", "prefer-ipv4", "prefer-ipv6", "only-ipv4", "only-ipv6":
	default:
		return fmt.Errorf("prefer-ip %q must be one of prefer-ipv4, prefer-ipv6, only-ipv4, only-ipv6", parsed.PreferIP)
	}
	if parsed.Concurrency < 0 {
		return fmt.Errorf("max connections must not be negative")
	}
	if err := validateOptionalIP(parsed.PublicIPv4, false); err != nil {
		return fmt.Errorf("public IPv4: %w", err)
	}
	if err := validateOptionalIP(parsed.PublicIPv6, true); err != nil {
		return fmt.Errorf("public IPv6: %w", err)
	}
	if err := validateDuration(parsed.TolerateTimeSkewness); err != nil {
		return fmt.Errorf("time skew tolerance: %w", err)
	}
	if err := validateDuration(parsed.Doppelganger.RaidEach); err != nil {
		return fmt.Errorf("doppelganger raid interval: %w", err)
	}
	if parsed.Doppelganger.RepeatsPerRaid < 0 {
		return fmt.Errorf("doppelganger repeats per raid must not be negative")
	}
	if err := validateResolver(parsed.DNS); err != nil {
		return fmt.Errorf("DNS resolver: %w", err)
	}
	if parsed.DomainFronting.Port < 0 || parsed.DomainFronting.Port > 65535 {
		return fmt.Errorf("domain fronting port %d is out of range", parsed.DomainFronting.Port)
	}
	if strings.ContainsAny(parsed.DomainFronting.Host, " \t\"") {
		return fmt.Errorf("domain fronting host %q must be a hostname or an address", parsed.DomainFronting.Host)
	}
	// Doppelganger crawls these itself and mtg requires HTTPS; a blocklist is
	// fetched over either scheme, or read from an absolute local path.
	for _, u := range nonEmpty(parsed.Doppelganger.URLs) {
		if err := validateHTTPURL(u, true); err != nil {
			return fmt.Errorf("doppelganger URL: %w", err)
		}
	}
	for _, u := range nonEmpty(parsed.Blocklist.URLs) {
		if strings.HasPrefix(u, "/") {
			continue
		}
		if err := validateHTTPURL(u, false); err != nil {
			return fmt.Errorf("blocklist URL: %w", err)
		}
	}
	return nil
}

// validateDuration accepts what mtg's config parser accepts: a Go duration such
// as "5s", "6h" or "1h30m". Empty means "leave mtg's default".
func validateDuration(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if _, err := time.ParseDuration(v); err != nil {
		return fmt.Errorf("%q is not a duration like 5s, 10m or 6h", v)
	}
	return nil
}

// validateOptionalIP checks a literal address of the expected family.
func validateOptionalIP(v string, wantV6 bool) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	ip := net.ParseIP(v)
	if ip == nil {
		return fmt.Errorf("%q is not an IP address", v)
	}
	if isV4 := ip.To4() != nil; isV4 == wantV6 {
		return fmt.Errorf("%q is the wrong address family", v)
	}
	return nil
}

// validateResolver checks mtg's `dns` value: a DoH/DoT URL, or a plain resolver
// address ("1.1.1.1" / "udp://1.1.1.1").
func validateResolver(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if scheme, rest, ok := strings.Cut(v, "://"); ok {
		switch scheme {
		case "https", "tls":
			return validateHTTPURL(strings.Replace(v, "tls://", "https://", 1), true)
		case "udp":
			return validateOptionalIPAny(rest)
		default:
			return fmt.Errorf("scheme %q must be https, tls or udp", scheme)
		}
	}
	return validateOptionalIPAny(v)
}

func validateOptionalIPAny(v string) error {
	if net.ParseIP(strings.TrimSpace(v)) == nil {
		return fmt.Errorf("%q is not an IP address", v)
	}
	return nil
}

// validateHTTPURL checks an absolute http(s) URL with a host.
func validateHTTPURL(v string, httpsOnly bool) error {
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q is not a URL", v)
	}
	if httpsOnly && u.Scheme != "https" {
		return fmt.Errorf("%q must start with https://", v)
	}
	if !httpsOnly && u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("%q must start with http:// or https://", v)
	}
	return nil
}
