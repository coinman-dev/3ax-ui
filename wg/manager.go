package wg

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

const (
	DefaultConfigDir  = "/etc/wireguard"
	DefaultConfigFile = "wg0.conf"
)

// PeerStatus holds runtime stats for one peer parsed from `wg show`.
type PeerStatus struct {
	PublicKey           string `json:"publicKey"`
	Endpoint            string `json:"endpoint"`
	LatestHandshake     int64  `json:"latestHandshake"` // unix timestamp
	TransferRx          int64  `json:"transferRx"`      // bytes received
	TransferTx          int64  `json:"transferTx"`      // bytes transmitted
	PersistentKeepalive int    `json:"persistentKeepalive"`
}

// WriteServerConfig writes the config string to the wg config file.
func WriteServerConfig(interfaceName string, config string) error {
	dir := DefaultConfigDir
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	fileName := interfaceName + ".conf"
	path := filepath.Join(dir, fileName)

	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// RemoveServerConfig deletes the config file for the given interface from disk.
func RemoveServerConfig(interfaceName string) {
	path := filepath.Join(DefaultConfigDir, interfaceName+".conf")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		logger.Warning("Failed to remove WG config file:", err)
	}
}

// InterfaceUp brings the WireGuard interface up using wg-quick.
func InterfaceUp(interfaceName string) error {
	configPath := filepath.Join(DefaultConfigDir, interfaceName+".conf")
	cmd := exec.Command("wg-quick", "up", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("wg-quick up failed: %s: %w", string(output), err)
	}
	logger.Info("WireGuard interface", interfaceName, "is up")
	return nil
}

// InterfaceDown takes the WireGuard interface down.
func InterfaceDown(interfaceName string) error {
	configPath := filepath.Join(DefaultConfigDir, interfaceName+".conf")
	cmd := exec.Command("wg-quick", "down", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("wg-quick down failed: %s: %w", string(output), err)
	}
	logger.Info("WireGuard interface", interfaceName, "is down")
	return nil
}

// SyncConfig applies config changes without dropping existing connections.
func SyncConfig(interfaceName string) error {
	configPath := filepath.Join(DefaultConfigDir, interfaceName+".conf")

	// Strip the config to get only the wireguard section for syncconf
	cmd := exec.Command("wg-quick", "strip", configPath)
	stripped, err := cmd.Output()
	if err != nil {
		// Fallback: restart the interface
		logger.Warning("wg-quick strip failed, restarting interface:", err)
		return RestartInterface(interfaceName)
	}

	syncCmd := exec.Command("wg", "syncconf", interfaceName, "/dev/stdin")
	syncCmd.Stdin = strings.NewReader(string(stripped))
	output, err := syncCmd.CombinedOutput()
	if err != nil {
		logger.Warning("wg syncconf failed, restarting interface:", string(output), err)
		return RestartInterface(interfaceName)
	}
	return nil
}

// RestartInterface performs a full down+up cycle.
func RestartInterface(interfaceName string) error {
	// Ignore error on down (interface might not be up)
	_ = InterfaceDown(interfaceName)
	time.Sleep(500 * time.Millisecond)
	return InterfaceUp(interfaceName)
}

// IsInterfaceUp checks if the wg interface exists in the system.
func IsInterfaceUp(interfaceName string) bool {
	cmd := exec.Command("wg", "show", interfaceName)
	err := cmd.Run()
	return err == nil
}

// GetPeerStats parses `wg show <iface> dump` to get per-peer traffic stats.
// The dump format has tab-separated fields:
// Line 1 (interface): private-key listen-port fwmark
// Line 2+ (peers): public-key preshared-key endpoint allowed-ips latest-handshake transfer-rx transfer-tx persistent-keepalive
func GetPeerStats(interfaceName string) ([]PeerStatus, error) {
	cmd := exec.Command("wg", "show", interfaceName, "dump")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("wg show dump failed: %w", err)
	}

	var peers []PeerStatus
	scanner := bufio.NewScanner(strings.NewReader(string(output)))

	// Skip first line (interface info)
	if scanner.Scan() {
		// skip
	}

	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Split(line, "\t")
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

// IsWgInstalled checks if the wg and wg-quick binaries are available.
func IsWgInstalled() bool {
	_, err1 := exec.LookPath("wg")
	_, err2 := exec.LookPath("wg-quick")
	return err1 == nil && err2 == nil
}

// GetWgVersion returns the version string from `wg --version`.
func GetWgVersion() string {
	cmd := exec.Command("wg", "--version")
	output, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}
