package wg

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/coinman-dev/3ax-ui/v2/logger"
	"github.com/coinman-dev/3ax-ui/v2/shared/ipam"
)

const ndppdConfigPath = "/etc/ndppd.conf"

const wgSectionBegin = "# --- BEGIN WG ---"
const wgSectionEnd = "# --- END WG ---"

// generateWgSection builds the WG-managed ndppd proxy block with markers.
func generateWgSection(externalIface, tunnelIface, ipv6Pool string) string {
	return fmt.Sprintf(`%s
proxy %s {
    router yes
    timeout 500
    ttl 30000
    rule %s {
        iface %s
    }
}
%s`, wgSectionBegin, externalIface, ipv6Pool, tunnelIface, wgSectionEnd)
}

// wgSectionRegex matches the WG-managed block including markers.
var wgSectionRegex = regexp.MustCompile(`(?s)` + regexp.QuoteMeta(wgSectionBegin) + `.*?` + regexp.QuoteMeta(wgSectionEnd))

// ApplyNdppdConfig updates only the WG section in ndppd.conf, preserving other rules.
// If the file doesn't exist or has no WG section, the section is appended.
func ApplyNdppdConfig(externalIface, tunnelIface, ipv6Pool string) error {
	newSection := generateWgSection(externalIface, tunnelIface, ipv6Pool)

	existing, err := os.ReadFile(ndppdConfigPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read ndppd config: %w", err)
	}

	var result string
	if len(existing) > 0 && wgSectionRegex.Match(existing) {
		result = wgSectionRegex.ReplaceAllString(string(existing), newSection)
	} else if len(existing) > 0 {
		result = strings.TrimRight(string(existing), "\n") + "\n\n" + newSection + "\n"
	} else {
		result = "route-ttl 30000\n\n" + newSection + "\n"
	}

	if err := os.WriteFile(ndppdConfigPath, []byte(result), 0644); err != nil {
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

// StopNdppd removes the WG section from ndppd.conf and restarts (or stops) ndppd.
func StopNdppd() {
	existing, err := os.ReadFile(ndppdConfigPath)
	if err != nil {
		_ = exec.Command("systemctl", "stop", "ndppd").Run()
		return
	}

	cleaned := wgSectionRegex.ReplaceAllString(string(existing), "")
	cleaned = strings.TrimSpace(cleaned)

	if cleaned == "" || cleaned == "route-ttl 30000" {
		_ = os.Remove(ndppdConfigPath)
		_ = exec.Command("systemctl", "stop", "ndppd").Run()
	} else {
		_ = os.WriteFile(ndppdConfigPath, []byte(cleaned+"\n"), 0644)
		_ = exec.Command("systemctl", "restart", "ndppd").Run()
	}
}

// AddProxyNDP adds a single IPv6 NDP proxy entry (fallback method without ndppd).
func AddProxyNDP(ipv6 string, externalIface string) error {
	ip := ipam.StripMask(ipv6)
	cmd := exec.Command("ip", "-6", "neigh", "add", "proxy", ip, "dev", externalIface)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "File exists") {
			return nil
		}
		return fmt.Errorf("add NDP proxy for %s: %s: %w", ip, string(output), err)
	}
	return nil
}

// RemoveProxyNDP removes a single IPv6 NDP proxy entry.
func RemoveProxyNDP(ipv6 string, externalIface string) error {
	ip := ipam.StripMask(ipv6)
	cmd := exec.Command("ip", "-6", "neigh", "del", "proxy", ip, "dev", externalIface)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "No such") {
			return nil
		}
		return fmt.Errorf("remove NDP proxy for %s: %s: %w", ip, string(output), err)
	}
	return nil
}

// IsNdppdInstalled checks if ndppd is available on the system.
func IsNdppdInstalled() bool {
	_, err := exec.LookPath("ndppd")
	return err == nil
}
