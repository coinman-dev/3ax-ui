// Package axlegacy retires the awg-quick / wg-quick tunnels a 3AX-UI 1.x panel
// left up: they outlive it and hold the UDP ports the AmneziaWG inbounds need.
package axlegacy

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

var (
	awgConfDir = "/etc/amnezia/amneziawg"
	wgConfDir  = "/etc/wireguard"
	ndppdConf  = "/etc/ndppd.conf"
	runHost    = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	}
)

var interfaceName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

// 1.x marked its ndppd.conf blocks "# --- BEGIN AWG ---" ... "# --- END WG ---".
var ndppdSection = regexp.MustCompile(`(?s)# --- BEGIN (AWG|WG) ---.*?# --- END (AWG|WG) ---\n?`)

// RetireHost runs once after the 1.x database migration recorded what to
// retire, and before the AmneziaWG job binds those ports again.
func RetireHost() {
	if runtime.GOOS != "linux" {
		return
	}
	db := database.GetDB()
	var setting model.Setting
	if err := db.Where("key = ?", database.AxLegacyHostCleanupKey).First(&setting).Error; err != nil {
		return
	}
	var ifaces []database.AxLegacyInterface
	if err := json.Unmarshal([]byte(setting.Value), &ifaces); err != nil {
		logger.Warning("3AX-UI 1.x cleanup: unreadable interface list, skipped:", err)
	}
	for _, iface := range ifaces {
		retireInterface(iface)
	}
	stripNdppd()
	if err := db.Delete(&setting).Error; err != nil {
		logger.Warning("3AX-UI 1.x cleanup: could not clear the cleanup marker:", err)
	}
}

func retireInterface(iface database.AxLegacyInterface) {
	if !interfaceName.MatchString(iface.Name) || strings.Trim(iface.Name, ".") == "" {
		logger.Warningf("3AX-UI 1.x cleanup: %q is not an interface name, skipped", iface.Name)
		return
	}
	dir, quick := awgConfDir, "awg-quick"
	if iface.Kind == "wg" {
		dir, quick = wgConfDir, "wg-quick"
	}
	conf := filepath.Join(dir, iface.Name+".conf")
	if _, err := os.Stat(conf); err != nil {
		// No config left: drop the interface itself if a reboot has not already.
		_, _ = runHost("ip", "link", "del", iface.Name)
		return
	}
	// "down" also runs the config's PostDown, which removes 1.x's iptables,
	// TPROXY and NDP proxy rules for this interface.
	if out, err := runHost(quick, "down", conf); err != nil {
		logger.Warningf("3AX-UI 1.x cleanup: %s down %s: %v: %s", quick, iface.Name, err, strings.TrimSpace(string(out)))
		_, _ = runHost("ip", "link", "del", iface.Name)
	}
	if err := os.Rename(conf, conf+".3ax-1x-bak"); err != nil {
		logger.Warning("3AX-UI 1.x cleanup: could not move the old config aside:", err)
	}
	logger.Infof("3AX-UI 1.x cleanup: interface %s retired", iface.Name)
}

func stripNdppd() {
	raw, err := os.ReadFile(ndppdConf)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !ndppdSection.Match(raw)) {
		return
	}
	if err != nil {
		logger.Warning("3AX-UI 1.x cleanup: reading ndppd.conf:", err)
		return
	}
	rest := strings.TrimSpace(ndppdSection.ReplaceAllString(string(raw), ""))
	if rest == "" || rest == "route-ttl 30000" {
		// Emptied, not removed: the sandboxed unit cannot unlink files in /etc.
		if err := os.WriteFile(ndppdConf, nil, 0o644); err != nil {
			logger.Warning("3AX-UI 1.x cleanup: emptying ndppd.conf:", err)
		}
		_, _ = runHost("systemctl", "stop", "ndppd")
		return
	}
	if err := os.WriteFile(ndppdConf, []byte(rest+"\n"), 0o644); err != nil {
		logger.Warning("3AX-UI 1.x cleanup: writing ndppd.conf:", err)
		return
	}
	_, _ = runHost("systemctl", "restart", "ndppd")
}
