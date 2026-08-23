// Package ndppd manages the ndppd (NDP proxy daemon) configuration that lets
// tunnel clients use routable IPv6 addresses from the host's subnet.
//
// AmneziaWG and native WireGuard each own a marked section of the same
// /etc/ndppd.conf: the file may also contain rules the operator wrote by hand,
// so a tunnel only ever rewrites the block between its own markers. Both
// tunnels need byte-identical handling, hence one implementation parameterised
// by the marker name.
package ndppd

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/coinman-dev/3ax-ui/v2/logger"
	"github.com/coinman-dev/3ax-ui/v2/shared/ipam"
)

const configPath = "/etc/ndppd.conf"

// Section owns one marked block of ndppd.conf.
type Section struct {
	begin string
	end   string
	re    *regexp.Regexp
}

// NewSection returns the handler for the block marked with the given name,
// e.g. NewSection("AWG") owns "# --- BEGIN AWG ---" … "# --- END AWG ---".
func NewSection(name string) *Section {
	begin := fmt.Sprintf("# --- BEGIN %s ---", name)
	end := fmt.Sprintf("# --- END %s ---", name)
	return &Section{
		begin: begin,
		end:   end,
		re:    regexp.MustCompile(`(?s)` + regexp.QuoteMeta(begin) + `.*?` + regexp.QuoteMeta(end)),
	}
}

// Render builds this section's proxy block, markers included.
func (s *Section) Render(externalIface, tunnelIface, ipv6Pool string) string {
	return fmt.Sprintf(`%s
proxy %s {
    router yes
    timeout 500
    ttl 30000
    rule %s {
        iface %s
    }
}
%s`, s.begin, externalIface, ipv6Pool, tunnelIface, s.end)
}

// Apply writes this section into ndppd.conf, leaving every other rule intact,
// and restarts ndppd. A missing file is created with the route-ttl header.
func (s *Section) Apply(externalIface, tunnelIface, ipv6Pool string) error {
	newSection := s.Render(externalIface, tunnelIface, ipv6Pool)

	existing, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read ndppd config: %w", err)
	}

	if err := os.WriteFile(configPath, []byte(s.merge(existing, newSection)), 0644); err != nil {
		return fmt.Errorf("write ndppd config: %w", err)
	}

	if err := exec.Command("systemctl", "restart", "ndppd").Run(); err != nil {
		logger.Warning("systemctl restart ndppd failed, trying service command:", err)
		if err2 := exec.Command("service", "ndppd", "restart").Run(); err2 != nil {
			return fmt.Errorf("restart ndppd: %w", err2)
		}
	}

	logger.Info("ndppd config applied for pool", ipv6Pool)
	return nil
}

// merge places newSection into the existing config: replacing this section if
// it is already there, appending it (keeping every other rule) if not, or
// starting a fresh file with the route-ttl header.
func (s *Section) merge(existing []byte, newSection string) string {
	switch {
	case len(existing) > 0 && s.re.Match(existing):
		return s.re.ReplaceAllString(string(existing), newSection)
	case len(existing) > 0:
		return strings.TrimRight(string(existing), "\n") + "\n\n" + newSection + "\n"
	default:
		return "route-ttl 30000\n\n" + newSection + "\n"
	}
}

// remove strips this section, returning the trimmed remainder.
func (s *Section) remove(existing []byte) string {
	return strings.TrimSpace(s.re.ReplaceAllString(string(existing), ""))
}

// Stop removes this section from ndppd.conf. The daemon is stopped only when
// nothing but the header is left — the other tunnel may still need it running.
func (s *Section) Stop() {
	existing, err := os.ReadFile(configPath)
	if err != nil {
		_ = exec.Command("systemctl", "stop", "ndppd").Run()
		return
	}

	cleaned := s.remove(existing)

	if cleaned == "" || cleaned == "route-ttl 30000" {
		_ = os.Remove(configPath)
		_ = exec.Command("systemctl", "stop", "ndppd").Run()
		return
	}
	_ = os.WriteFile(configPath, []byte(cleaned+"\n"), 0644)
	_ = exec.Command("systemctl", "restart", "ndppd").Run()
}

// AddProxy adds a single IPv6 NDP proxy entry (fallback when ndppd is absent).
func AddProxy(ipv6 string, externalIface string) error {
	ip := ipam.StripMask(ipv6)
	output, err := exec.Command("ip", "-6", "neigh", "add", "proxy", ip, "dev", externalIface).CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "File exists") {
			return nil
		}
		return fmt.Errorf("add NDP proxy for %s: %s: %w", ip, string(output), err)
	}
	return nil
}

// RemoveProxy removes a single IPv6 NDP proxy entry.
func RemoveProxy(ipv6 string, externalIface string) error {
	ip := ipam.StripMask(ipv6)
	output, err := exec.Command("ip", "-6", "neigh", "del", "proxy", ip, "dev", externalIface).CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "No such") {
			return nil
		}
		return fmt.Errorf("remove NDP proxy for %s: %s: %w", ip, string(output), err)
	}
	return nil
}

// IsInstalled reports whether ndppd is available on this system.
func IsInstalled() bool {
	_, err := exec.LookPath("ndppd")
	return err == nil
}
