package service

import (
	"fmt"
	"net"

	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/util/random"
	"gorm.io/gorm"
)

const (
	minTunnelListenPort     = 10000
	maxTunnelListenPort     = 65535
	randomPortSearchAttempt = 256
	legacyAwgListenPort     = 51820
	legacyWgListenPort      = 51821
)

func pickRandomTunnelListenPort(excluded ...int) (int, error) {
	blocked := make(map[int]struct{}, len(excluded))
	for _, port := range excluded {
		if port > 0 {
			blocked[port] = struct{}{}
		}
	}

	span := maxTunnelListenPort - minTunnelListenPort + 1
	for range randomPortSearchAttempt {
		port := minTunnelListenPort + random.Num(span)
		if _, exists := blocked[port]; exists {
			continue
		}
		if isUDPPortAvailable(port) {
			return port, nil
		}
	}

	for port := minTunnelListenPort; port <= maxTunnelListenPort; port++ {
		if _, exists := blocked[port]; exists {
			continue
		}
		if isUDPPortAvailable(port) {
			return port, nil
		}
	}

	return 0, fmt.Errorf("no available UDP port found in range %d-%d", minTunnelListenPort, maxTunnelListenPort)
}

func isUDPPortAvailable(port int) bool {
	conn, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// otherTunnelListenPort returns the listen port of the *other* flavour, so a
// freshly generated port never lands on the one already in use.
func otherTunnelListenPort(db *gorm.DB, kind string) int {
	other := model.TunnelKindWg
	if kind == model.TunnelKindWg {
		other = model.TunnelKindAwg
	}
	var server model.TunnelServer
	if err := db.Select("listen_port").Where("kind = ?", other).First(&server).Error; err != nil {
		return 0
	}
	return server.ListenPort
}
