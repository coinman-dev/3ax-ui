package awg

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/coinman-dev/3ax-ui/v2/logger"
)

const ndppdConfigPath = "/etc/ndppd.conf"

const awgSectionBegin = "# --- BEGIN AWG ---"
const awgSectionEnd = "# --- END AWG ---"

// generateAwgSection builds the AWG-managed ndppd proxy block with markers.
func generateAwgSection(externalIface, tunnelIface, ipv6Pool string) string {
	return fmt.Sprintf(`%s
proxy %s {
    router yes
    timeout 500
    ttl 30000
    rule %s {
        iface %s
    }
}
%s`, awgSectionBegin, externalIface, ipv6Pool, tunnelIface, awgSectionEnd)
}

// awgSectionRegex matches the AWG-managed block including markers.
var awgSectionRegex = regexp.MustCompile(`(?s)` + regexp.QuoteMeta(awgSectionBegin) + `.*?` + regexp.QuoteMeta(awgSectionEnd))

// ApplyNdppdConfig updates only the AWG section in ndppd.conf, preserving other rules.
// If the file doesn't exist or has no AWG section, the section is appended.
func ApplyNdppdConfig(externalIface, tunnelIface, ipv6Pool string) error {
	newSection := generateAwgSection(externalIface, tunnelIface, ipv6Pool)

	existing, err := os.ReadFile(ndppdConfigPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read ndppd config: %w", err)
	}

	var result string
	if len(existing) > 0 && awgSectionRegex.Match(existing) {
		// Replace existing AWG section
		result = awgSectionRegex.ReplaceAllString(string(existing), newSection)
	} else if len(existing) > 0 {
		// Append AWG section to existing config
		result = strings.TrimRight(string(existing), "\n") + "\n\n" + newSection + "\n"
	} else {
		// New file: add route-ttl header + AWG section
		result = "route-ttl 30000\n\n" + newSection + "\n"
	}

	if err := os.WriteFile(ndppdConfigPath, []byte(result), 0644); err != nil {
		return fmt.Errorf("write ndppd config: %w", err)
	}

	// Try systemctl restart first, fall back to service command
	if err := exec.Command("systemctl", "restart", "ndppd").Run(); err != nil {
		logger.Warning("systemctl restart ndppd failed, trying service command:", err)
		if err2 := exec.Command("service", "ndppd", "restart").Run(); err2 != nil {
			return fmt.Errorf("restart ndppd: %w", err2)
		}
	}

	logger.Info("ndppd config applied for pool", ipv6Pool)
	return nil
}

// StopNdppd removes the AWG section from ndppd.conf and restarts (or stops) ndppd.
func StopNdppd() {
	existing, err := os.ReadFile(ndppdConfigPath)
	if err != nil {
		// No config file — just stop the service
		_ = exec.Command("systemctl", "stop", "ndppd").Run()
		return
	}

	cleaned := awgSectionRegex.ReplaceAllString(string(existing), "")
	cleaned = strings.TrimSpace(cleaned)

	if cleaned == "" || cleaned == "route-ttl 30000" {
		// Nothing left — remove file and stop service
		_ = os.Remove(ndppdConfigPath)
		_ = exec.Command("systemctl", "stop", "ndppd").Run()
	} else {
		// Other rules remain — write back and restart
		_ = os.WriteFile(ndppdConfigPath, []byte(cleaned+"\n"), 0644)
		_ = exec.Command("systemctl", "restart", "ndppd").Run()
	}
}

// AddProxyNDP adds a single IPv6 NDP proxy entry (fallback method without ndppd).
func AddProxyNDP(ipv6 string, externalIface string) error {
	ip := stripMask(ipv6)
	cmd := exec.Command("ip", "-6", "neigh", "add", "proxy", ip, "dev", externalIface)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Ignore "File exists" error
		if strings.Contains(string(output), "File exists") {
			return nil
		}
		return fmt.Errorf("add NDP proxy for %s: %s: %w", ip, string(output), err)
	}
	return nil
}

// RemoveProxyNDP removes a single IPv6 NDP proxy entry.
func RemoveProxyNDP(ipv6 string, externalIface string) error {
	ip := stripMask(ipv6)
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
