package awg

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
	DefaultConfigDir  = "/etc/amnezia/amneziawg"
	DefaultConfigFile = "awg0.conf"
)

// PeerStatus holds runtime stats for one peer parsed from `awg show`.
type PeerStatus struct {
	PublicKey           string `json:"publicKey"`
	Endpoint            string `json:"endpoint"`
	LatestHandshake     int64  `json:"latestHandshake"` // unix timestamp
	TransferRx          int64  `json:"transferRx"`      // bytes received
	TransferTx          int64  `json:"transferTx"`      // bytes transmitted
	PersistentKeepalive int    `json:"persistentKeepalive"`
}

// WriteServerConfig writes the config string to the awg config file.
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
		logger.Warning("Failed to remove AWG config file:", err)
	}
}

// InterfaceUp brings the AmneziaWG interface up using awg-quick.
func InterfaceUp(interfaceName string) error {
	configPath := filepath.Join(DefaultConfigDir, interfaceName+".conf")
	cmd := exec.Command("awg-quick", "up", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("awg-quick up failed: %s: %w", string(output), err)
	}
	logger.Info("AmneziaWG interface", interfaceName, "is up")
	return nil
}

// InterfaceDown takes the AmneziaWG interface down.
func InterfaceDown(interfaceName string) error {
	configPath := filepath.Join(DefaultConfigDir, interfaceName+".conf")
	cmd := exec.Command("awg-quick", "down", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("awg-quick down failed: %s: %w", string(output), err)
	}
	logger.Info("AmneziaWG interface", interfaceName, "is down")
	return nil
}

// SyncConfig applies config changes without dropping existing connections.
func SyncConfig(interfaceName string) error {
	configPath := filepath.Join(DefaultConfigDir, interfaceName+".conf")

	// Strip the config to get only the interface section for syncconf
	cmd := exec.Command("awg-quick", "strip", configPath)
	stripped, err := cmd.Output()
	if err != nil {
		// Fallback: restart the interface
		logger.Warning("awg-quick strip failed, restarting interface:", err)
		return RestartInterface(interfaceName)
	}

	syncCmd := exec.Command("awg", "syncconf", interfaceName, "/dev/stdin")
	syncCmd.Stdin = strings.NewReader(string(stripped))
	output, err := syncCmd.CombinedOutput()
	if err != nil {
		logger.Warning("awg syncconf failed, restarting interface:", string(output), err)
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

// IsInterfaceUp checks if the awg interface exists in the system.
func IsInterfaceUp(interfaceName string) bool {
	cmd := exec.Command("awg", "show", interfaceName)
	err := cmd.Run()
	return err == nil
}

// GetPeerStats parses `awg show <iface> dump` to get per-peer traffic stats.
// The dump format has tab-separated fields:
// Line 1 (interface): private-key listen-port fwmark
// Line 2+ (peers): public-key preshared-key endpoint allowed-ips latest-handshake transfer-rx transfer-tx persistent-keepalive
func GetPeerStats(interfaceName string) ([]PeerStatus, error) {
	cmd := exec.Command("awg", "show", interfaceName, "dump")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("awg show dump failed: %w", err)
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

// IsAwgInstalled checks if the awg and awg-quick binaries are available.
func IsAwgInstalled() bool {
	_, err1 := exec.LookPath("awg")
	_, err2 := exec.LookPath("awg-quick")
	return err1 == nil && err2 == nil
}

// GetAwgVersion returns the version string from `awg --version`.
func GetAwgVersion() string {
	cmd := exec.Command("awg", "--version")
	output, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}
