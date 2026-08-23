package tunnel

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coinman-dev/3ax-ui/v2/logger"
)

// PeerStatus holds runtime stats for one peer parsed from `<tool> show`.
type PeerStatus struct {
	PublicKey           string `json:"publicKey"`
	Endpoint            string `json:"endpoint"`
	LatestHandshake     int64  `json:"latestHandshake"` // unix timestamp
	TransferRx          int64  `json:"transferRx"`      // bytes received
	TransferTx          int64  `json:"transferTx"`      // bytes transmitted
	PersistentKeepalive int    `json:"persistentKeepalive"`
}

// ConfigPath returns the on-disk path of an interface's config file.
func ConfigPath(k Kind, interfaceName string) string {
	return filepath.Join(k.ConfigDir, interfaceName+".conf")
}

// WriteServerConfig writes the config to <ConfigDir>/<interface>.conf. The file
// holds the server private key, hence 0600 in a 0700 directory.
func WriteServerConfig(k Kind, interfaceName string, config string) error {
	if err := os.MkdirAll(k.ConfigDir, 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(ConfigPath(k, interfaceName), []byte(config), 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// RemoveServerConfig deletes an interface's config file.
func RemoveServerConfig(k Kind, interfaceName string) {
	if err := os.Remove(ConfigPath(k, interfaceName)); err != nil && !os.IsNotExist(err) {
		logger.Warningf("Failed to remove %s config file: %v", k.Title, err)
	}
}

// InterfaceUp brings the interface up via <quick> up.
func InterfaceUp(k Kind, interfaceName string) error {
	output, err := exec.Command(k.Quick, "up", ConfigPath(k, interfaceName)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s up failed: %s: %w", k.Quick, string(output), err)
	}
	logger.Info(k.Title, "interface", interfaceName, "is up")
	return nil
}

// InterfaceDown takes the interface down.
func InterfaceDown(k Kind, interfaceName string) error {
	output, err := exec.Command(k.Quick, "down", ConfigPath(k, interfaceName)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s down failed: %s: %w", k.Quick, string(output), err)
	}
	logger.Info(k.Title, "interface", interfaceName, "is down")
	return nil
}

// SyncConfig applies config changes without dropping existing connections.
// Note for AmneziaWG: syncconf does not re-apply obfuscation parameters to a
// live interface — a change there needs RestartInterface instead.
func SyncConfig(k Kind, interfaceName string) error {
	configPath := ConfigPath(k, interfaceName)

	// Strip the config down to the interface section syncconf accepts.
	stripped, err := exec.Command(k.Quick, "strip", configPath).Output()
	if err != nil {
		logger.Warningf("%s strip failed, restarting interface: %v", k.Quick, err)
		return RestartInterface(k, interfaceName)
	}

	syncCmd := exec.Command(k.Tool, "syncconf", interfaceName, "/dev/stdin")
	syncCmd.Stdin = strings.NewReader(string(stripped))
	output, err := syncCmd.CombinedOutput()
	if err != nil {
		logger.Warningf("%s syncconf failed, restarting interface: %s: %v", k.Tool, string(output), err)
		return RestartInterface(k, interfaceName)
	}
	return nil
}

// RestartInterface performs a full down+up cycle.
func RestartInterface(k Kind, interfaceName string) error {
	// Down may fail because the interface is not up — that is not an error here.
	_ = InterfaceDown(k, interfaceName)
	time.Sleep(500 * time.Millisecond)
	return InterfaceUp(k, interfaceName)
}

// IsInterfaceUp reports whether the interface exists in the system.
func IsInterfaceUp(k Kind, interfaceName string) bool {
	return exec.Command(k.Tool, "show", interfaceName).Run() == nil
}

// GetPeerStats parses `<tool> show <iface> dump` into per-peer traffic stats.
// Tab-separated; the first line describes the interface (and contains its
// private key, so it is skipped), the rest are peers:
// public-key preshared-key endpoint allowed-ips latest-handshake rx tx keepalive
func GetPeerStats(k Kind, interfaceName string) ([]PeerStatus, error) {
	output, err := exec.Command(k.Tool, "show", interfaceName, "dump").Output()
	if err != nil {
		return nil, fmt.Errorf("%s show dump failed: %w", k.Tool, err)
	}

	var peers []PeerStatus
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	if scanner.Scan() {
		// interface line — skipped
	}
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) < 8 {
			continue
		}
		handshake, _ := strconv.ParseInt(fields[4], 10, 64)
		rx, _ := strconv.ParseInt(fields[5], 10, 64)
		tx, _ := strconv.ParseInt(fields[6], 10, 64)
		keepalive, _ := strconv.Atoi(fields[7])

		peers = append(peers, PeerStatus{
			PublicKey:           fields[0],
			Endpoint:            fields[2],
			LatestHandshake:     handshake,
			TransferRx:          rx,
			TransferTx:          tx,
			PersistentKeepalive: keepalive,
		})
	}
	return peers, nil
}

// IsInstalled reports whether both CLI tools of this flavour are available.
func IsInstalled(k Kind) bool {
	_, err1 := exec.LookPath(k.Tool)
	_, err2 := exec.LookPath(k.Quick)
	return err1 == nil && err2 == nil
}

// Version returns a version string for display.
//
// History worth keeping: amneziawg-tools used to report the inherited
// wireguard-tools version ("v1.0.20210914") and never bump it (upstream issue
// #21), so the panel derived the generation from the kernel module's build date
// and printed "v2.0.<date>". Upstream has since fixed the tools — they now
// print their own version, e.g. "amneziawg-tools v3.1.20260812" — and the old
// workaround started lying the other way, showing "v2.0.…" for a 3.1 install.
// So: trust the tools when their banner belongs to this flavour, and fall back
// to the kernel module version otherwise.
func Version(k Kind) string {
	if v := toolVersion(k); v != "" {
		return v
	}
	if v := ModuleVersion(k); v != "" {
		return withVPrefix(v)
	}
	return "unknown"
}

// toolVersion reads `<tool> --version` and extracts the version token, or
// returns "" when the banner is the legacy cosmetic one.
func toolVersion(k Kind) string {
	out, err := exec.Command(k.Tool, "--version").Output()
	if err != nil {
		return ""
	}
	banner := strings.TrimSpace(string(out))
	// For AmneziaWG a wireguard-tools banner means the pre-fix cosmetic version.
	if k.Obfuscation && strings.Contains(strings.ToLower(banner), "wireguard-tools") {
		return ""
	}
	return versionToken(banner)
}

// versionToken picks the "vX.Y.Z" field out of a tools banner such as
// "amneziawg-tools v3.1.20260812 - https://amnezia.org".
func versionToken(banner string) string {
	for _, field := range strings.Fields(banner) {
		if len(field) > 1 && (field[0] == 'v' || field[0] == 'V') && field[1] >= '0' && field[1] <= '9' {
			return field
		}
	}
	return ""
}

// ModuleVersion returns the kernel module version (e.g. "3.1.20260812"),
// preferring sysfs and falling back to modinfo.
func ModuleVersion(k Kind) string {
	if b, err := os.ReadFile("/sys/module/" + k.KernelModule + "/version"); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v
		}
	}
	if out, err := exec.Command("modinfo", "-F", "version", k.KernelModule).Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}

// withVPrefix prefixes a version string with "v" for display, unless it already
// has one or is empty.
func withVPrefix(v string) string {
	if v == "" || v[0] == 'v' || v[0] == 'V' {
		return v
	}
	return "v" + v
}
