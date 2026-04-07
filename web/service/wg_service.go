package service

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/logger"
	"github.com/coinman-dev/3ax-ui/v2/shared/ipam"
	"github.com/coinman-dev/3ax-ui/v2/wg"
)

type WgService struct{}

// GetServer returns the WG server config, creating a default one if none exists.
func (s *WgService) GetServer() (*model.WgServer, error) {
	db := database.GetDB()
	var server model.WgServer
	err := db.FirstOrCreate(&server).Error
	if err != nil {
		return nil, err
	}

	needSave := false

	// Generate keys if missing
	if server.PrivateKey == "" {
		priv, pub, err := wg.GenerateKeyPair()
		if err != nil {
			return nil, fmt.Errorf("generate server keys: %w", err)
		}
		server.PrivateKey = priv
		server.PublicKey = pub
		needSave = true
	}

	// Auto-detect external interface if not set
	if server.ExternalInterface == "" {
		server.ExternalInterface = wg.DetectDefaultInterface()
		needSave = true
	}

	if needSave {
		if err := db.Save(&server).Error; err != nil {
			return nil, err
		}
	}

	return &server, nil
}

// SaveServer saves server settings and optionally applies them to the OS.
func (s *WgService) SaveServer(server *model.WgServer) error {
	db := database.GetDB()
	server.UpdatedAt = time.Now().UnixMilli()
	if err := db.Save(server).Error; err != nil {
		return err
	}

	// Sync listen port to the WG inbound record
	s.syncInboundPort(db, server.ListenPort)

	if server.Enable {
		return s.applyServerConfig(server)
	}
	return nil
}

// ResetToDefaults resets WG to the state right after installation.
func (s *WgService) ResetToDefaults() (*model.WgServer, error) {
	server, err := s.GetServer()
	if err != nil {
		return nil, err
	}

	if server.Enable {
		wg.StopNdppd()
		_ = wg.InterfaceDown(server.InterfaceName)
	}

	db := database.GetDB()

	if err := db.Where("server_id = ?", server.Id).Delete(&model.WgClient{}).Error; err != nil {
		return nil, err
	}

	if err := db.Where("protocol = ?", model.NativeWG).Delete(&model.Inbound{}).Error; err != nil {
		logger.Warning("Failed to delete WG inbounds on reset:", err)
	}

	wg.RemoveServerConfig(server.InterfaceName)

	priv, pub, err := wg.GenerateKeyPair()
	if err != nil {
		return nil, fmt.Errorf("generate server keys: %w", err)
	}

	if server.ExternalInterface == "" {
		server.ExternalInterface = wg.DetectDefaultInterface()
	}

	server.Enable = false
	server.ListenPort = 51821
	server.MTU = 1420
	server.PrivateKey = priv
	server.PublicKey = pub
	server.IPv4Address = "10.77.77.1/24"
	server.IPv4Pool = "10.77.77.0/24"
	// Preserve: IPv6Enabled, IPv6Address, IPv6Pool, IPv6Gateway,
	//           ExternalInterface, IPv6ExternalInterface, Endpoint
	server.DNS = "1.1.1.1,2606:4700:4700::1111"
	server.PostUp = ""
	server.PostDown = ""
	server.TrafficReset = "never"
	server.UpdatedAt = time.Now().UnixMilli()

	if err := db.Save(server).Error; err != nil {
		return nil, err
	}

	return server, nil
}

// ToggleServer enables or disables the WG interface.
func (s *WgService) ToggleServer(enable bool) error {
	server, err := s.GetServer()
	if err != nil {
		return err
	}

	db := database.GetDB()
	if err := db.Model(server).Update("enable", enable).Error; err != nil {
		return err
	}
	server.Enable = enable

	if enable {
		return s.applyServerConfig(server)
	}
	wg.StopNdppd()
	return wg.InterfaceDown(server.InterfaceName)
}

// WgStatus holds basic WireGuard server status info.
type WgStatus struct {
	Running     bool   `json:"running"`
	WgInstalled bool   `json:"wgInstalled"`
	WgVersion   string `json:"wgVersion"`
}

// GetServerStatus returns basic status info.
func (s *WgService) GetServerStatus() *WgStatus {
	server, _ := s.GetServer()
	ifaceName := "wg0"
	if server != nil {
		ifaceName = server.InterfaceName
	}
	return &WgStatus{
		Running:     wg.IsInterfaceUp(ifaceName),
		WgInstalled: wg.IsWgInstalled(),
		WgVersion:   wg.GetWgVersion(),
	}
}

// GetNetworkInterfaces returns non-loopback, non-tunnel UP interfaces with IP version info.
func (s *WgService) GetNetworkInterfaces() []NetworkInterface {
	ifaces, err := net.Interfaces()
	if err != nil {
		logger.Warning("Failed to list network interfaces:", err)
		return nil
	}

	var result []NetworkInterface
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if strings.HasPrefix(iface.Name, "awg") || strings.HasPrefix(iface.Name, "wg") {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil || len(addrs) == 0 {
			continue
		}

		ni := NetworkInterface{Name: iface.Name}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP
			if ip.IsLinkLocalUnicast() {
				continue
			}
			if ip.To4() != nil {
				ni.IPv4 = true
			} else {
				ni.IPv6 = true
			}
		}
		if ni.IPv4 || ni.IPv6 {
			result = append(result, ni)
		}
	}
	return result
}

// GetOnlineClients returns emails of WG clients online within the last 3 minutes.
func (s *WgService) GetOnlineClients() []string {
	db := database.GetDB()
	threshold := time.Now().Add(-3 * time.Minute).UnixMilli()
	var emails []string
	db.Model(&model.WgClient{}).
		Where("enable = ? AND last_online > ?", true, threshold).
		Pluck("email", &emails)
	return emails
}

// --- Clients ---

// GetClients returns all WG clients ordered by ID.
func (s *WgService) GetClients() ([]model.WgClient, error) {
	db := database.GetDB()
	var clients []model.WgClient
	if err := db.Order("id asc").Find(&clients).Error; err != nil {
		return nil, err
	}
	return clients, nil
}

// GetClient returns a single WG client by ID.
func (s *WgService) GetClient(id int) (*model.WgClient, error) {
	db := database.GetDB()
	var client model.WgClient
	if err := db.First(&client, id).Error; err != nil {
		return nil, err
	}
	return &client, nil
}

// AddClient creates a new WG client with auto-generated keys and allocated IPs.
func (s *WgService) AddClient(client *model.WgClient) error {
	server, err := s.GetServer()
	if err != nil {
		return err
	}

	if client.UUID == "" {
		client.UUID = uuid.New().String()
	}

	if _, err := uuid.Parse(client.UUID); err != nil {
		return fmt.Errorf("invalid UUID format for WG client ID: %s", client.UUID)
	}

	db := database.GetDB()
	var count int64
	db.Model(&model.WgClient{}).Where("uuid = ?", client.UUID).Count(&count)
	if count > 0 {
		return fmt.Errorf("WG client with this ID already exists")
	}

	priv, pub, err := wg.GenerateKeyPair()
	if err != nil {
		return fmt.Errorf("generate client keys: %w", err)
	}
	client.PrivateKey = priv
	client.PublicKey = pub

	psk, err := wg.GeneratePresharedKey()
	if err != nil {
		return fmt.Errorf("generate PSK: %w", err)
	}
	client.PresharedKey = psk

	existingClients, err := s.GetClients()
	if err != nil {
		return err
	}
	usedIPv4 := make([]string, 0, len(existingClients))
	usedIPv6 := make([]string, 0, len(existingClients))
	for _, c := range existingClients {
		usedIPv4 = append(usedIPv4, c.IPv4Address)
		if c.IPv6Address != "" {
			usedIPv6 = append(usedIPv6, c.IPv6Address)
		}
	}

	ipv4, err := ipam.AllocateIPv4(server.IPv4Pool, server.IPv4Address, usedIPv4)
	if err != nil {
		return fmt.Errorf("allocate IPv4: %w", err)
	}
	client.IPv4Address = ipv4

	if server.IPv6Enabled && server.IPv6Pool != "" {
		ipv6, err := ipam.AllocateIPv6(server.IPv6Pool, server.IPv6Address, usedIPv6)
		if err != nil {
			return fmt.Errorf("allocate IPv6: %w", err)
		}
		client.IPv6Address = ipv6
	}

	allowedIPs := client.IPv4Address
	if client.IPv6Address != "" {
		allowedIPs += ", " + client.IPv6Address
	}
	client.AllowedIPs = allowedIPs

	if client.ClientAllowedIPs == "" {
		client.ClientAllowedIPs = "0.0.0.0/0, ::/0"
	}

	client.ServerId = server.Id
	client.CreatedAt = time.Now().UnixMilli()

	if err := db.Create(client).Error; err != nil {
		return err
	}

	if !server.Enable {
		if err := db.Model(server).Update("enable", true).Error; err != nil {
			logger.Warning("Failed to auto-enable WG server:", err)
		}
		server.Enable = true
	}

	if err := s.applyServerConfig(server); err != nil {
		logger.Warning("Failed to apply WG config after adding client:", err)
	}

	return nil
}

// UpdateClient updates an existing WG client.
func (s *WgService) UpdateClient(client *model.WgClient) error {
	if client.UUID == "" {
		client.UUID = uuid.New().String()
	} else if _, err := uuid.Parse(client.UUID); err != nil {
		return fmt.Errorf("invalid UUID format for WG client ID: %s", client.UUID)
	}

	db := database.GetDB()
	client.UpdatedAt = time.Now().UnixMilli()
	if err := db.Save(client).Error; err != nil {
		return err
	}

	server, err := s.GetServer()
	if err != nil {
		return err
	}
	if server.Enable {
		if err := s.applyServerConfig(server); err != nil {
			logger.Warning("Failed to apply WG config after updating client:", err)
		}
	}
	return nil
}

// DeleteClient removes a WG client and cleans up NDP proxy if needed.
func (s *WgService) DeleteClient(id int) error {
	client, err := s.GetClient(id)
	if err != nil {
		return err
	}

	server, err := s.GetServer()
	if err != nil {
		return err
	}

	if server.IPv6Enabled && client.IPv6Address != "" {
		_ = wg.RemoveProxyNDP(client.IPv6Address, s.ipv6Iface(server))
	}

	db := database.GetDB()
	if err := db.Delete(&model.WgClient{}, id).Error; err != nil {
		return err
	}

	if server.Enable {
		if err := s.applyServerConfig(server); err != nil {
			logger.Warning("Failed to apply WG config after deleting client:", err)
		}
	}
	return nil
}

// DeleteAllClients stops the WG interface and removes all clients.
func (s *WgService) DeleteAllClients() error {
	server, err := s.GetServer()
	if err != nil {
		return nil
	}

	if server.Enable {
		wg.StopNdppd()
		_ = wg.InterfaceDown(server.InterfaceName)
	}

	db := database.GetDB()

	if err := db.Where("server_id = ?", server.Id).Delete(&model.WgClient{}).Error; err != nil {
		return err
	}

	if err := db.Model(server).Update("enable", false).Error; err != nil {
		return err
	}

	return nil
}

// ToggleClient enables or disables a WG client.
func (s *WgService) ToggleClient(id int, enable bool) error {
	client, err := s.GetClient(id)
	if err != nil {
		return err
	}
	client.Enable = enable
	return s.UpdateClient(client)
}

// GetClientConfig returns the text content of a client .conf file.
func (s *WgService) GetClientConfig(id int) (string, error) {
	client, err := s.GetClient(id)
	if err != nil {
		return "", err
	}
	server, err := s.GetServer()
	if err != nil {
		return "", err
	}
	return wg.GenerateClientConfig(server, client), nil
}

// ResetClientTraffic resets upload/download counters for a WG client.
func (s *WgService) ResetClientTraffic(id int) error {
	db := database.GetDB()
	return db.Model(&model.WgClient{}).Where("id = ?", id).Updates(map[string]any{
		"upload":   0,
		"download": 0,
	}).Error
}

// UpdateTrafficStats reads live peer stats, updates the database, and enforces limits.
func (s *WgService) UpdateTrafficStats() {
	server, err := s.GetServer()
	if err != nil || !server.Enable {
		return
	}

	if !wg.IsInterfaceUp(server.InterfaceName) {
		return
	}

	peers, err := wg.GetPeerStats(server.InterfaceName)
	if err != nil {
		return
	}

	db := database.GetDB()
	var clients []model.WgClient
	if err := db.Find(&clients).Error; err != nil {
		return
	}

	clientMap := make(map[string]*model.WgClient)
	for i := range clients {
		clientMap[clients[i].PublicKey] = &clients[i]
	}

	db.Transaction(func(tx *gorm.DB) error {
		for _, peer := range peers {
			client, ok := clientMap[peer.PublicKey]
			if !ok {
				continue
			}

			newUp := peer.TransferTx
			newDown := peer.TransferRx

			updates := map[string]any{}

			if newUp != client.Upload || newDown != client.Download {
				allTimeDelta := int64(0)
				if newUp > client.Upload {
					allTimeDelta += newUp - client.Upload
				}
				if newDown > client.Download {
					allTimeDelta += newDown - client.Download
				}

				updates["upload"] = newUp
				updates["download"] = newDown
				updates["all_time"] = client.AllTime + allTimeDelta
			}

			if peer.LatestHandshake > 0 {
				handshakeMs := peer.LatestHandshake * 1000
				if handshakeMs != client.LastOnline {
					updates["last_online"] = handshakeMs
				}
			}

			if peer.Endpoint != "" && peer.Endpoint != "(none)" {
				ep := peer.Endpoint
				if idx := strings.LastIndex(ep, ":"); idx > 0 {
					ep = ep[:idx]
				}
				ep = strings.TrimPrefix(strings.TrimSuffix(ep, "]"), "[")
				if ep != client.LastIP {
					updates["last_ip"] = ep
				}
			}

			if len(updates) > 0 {
				tx.Model(client).Updates(updates)
			}
		}
		return nil
	})

	needReconfig := s.autoRenewWgClients(db)
	if s.adjustWgDelayedStart(db) {
		needReconfig = true
	}
	if s.disableInvalidWgClients(db) {
		needReconfig = true
	}

	if needReconfig {
		if err := s.applyServerConfig(server); err != nil {
			logger.Warning("Failed to apply WG config after enforcement:", err)
		}
	}
}

func (s *WgService) autoRenewWgClients(db *gorm.DB) bool {
	now := time.Now().UnixMilli()
	var clients []model.WgClient

	if err := db.Where("reset > 0 AND expiry_time > 0 AND expiry_time <= ?", now).Find(&clients).Error; err != nil {
		logger.Warning("WG autoRenewClients error:", err)
		return false
	}

	if len(clients) == 0 {
		return false
	}

	needReconfig := false
	for _, c := range clients {
		newExpiry := c.ExpiryTime
		for newExpiry <= now {
			newExpiry += int64(c.Reset) * 86400000
		}

		updates := map[string]any{
			"expiry_time": newExpiry,
			"upload":      0,
			"download":    0,
		}
		if !c.Enable {
			updates["enable"] = true
			needReconfig = true
		}

		db.Model(&model.WgClient{}).Where("id = ?", c.Id).Updates(updates)
		logger.Infof("WG: client '%s' auto-renewed, new expiry: %v", c.Email, time.UnixMilli(newExpiry))
	}

	return needReconfig
}

func (s *WgService) disableInvalidWgClients(db *gorm.DB) bool {
	now := time.Now().UnixMilli()

	result := db.Model(&model.WgClient{}).
		Where("enable = ? AND ((total_gb > 0 AND upload + download >= total_gb) OR (expiry_time > 0 AND expiry_time <= ?))", true, now).
		Update("enable", false)

	if result.Error != nil {
		logger.Warning("WG disableInvalidClients error:", result.Error)
		return false
	}
	if result.RowsAffected > 0 {
		logger.Infof("WG: %d client(s) disabled (quota/expiry)", result.RowsAffected)
	}
	return result.RowsAffected > 0
}

func (s *WgService) adjustWgDelayedStart(db *gorm.DB) bool {
	now := time.Now().UnixMilli()
	var clients []model.WgClient

	if err := db.Where("enable = ? AND expiry_time < 0 AND (upload > 0 OR download > 0)", true).Find(&clients).Error; err != nil {
		logger.Warning("WG adjustDelayedStart error:", err)
		return false
	}

	if len(clients) == 0 {
		return false
	}

	for _, c := range clients {
		newExpiry := now + (-c.ExpiryTime)
		db.Model(&model.WgClient{}).Where("id = ?", c.Id).Update("expiry_time", newExpiry)
		logger.Infof("WG: client '%s' delayed start activated, expires at %v", c.Email, time.UnixMilli(newExpiry))
	}

	return false
}

// ResetAllClientTraffics resets traffic counters for all WG clients.
func (s *WgService) ResetAllClientTraffics() error {
	db := database.GetDB()
	now := time.Now().UnixMilli()

	db.Model(&model.WgClient{}).
		Where("enable = ? AND total_gb > 0 AND (expiry_time = 0 OR expiry_time > ?)", false, now).
		Update("enable", true)

	err := db.Model(&model.WgClient{}).Updates(map[string]any{
		"upload":   0,
		"download": 0,
	}).Error
	if err != nil {
		return err
	}

	server, sErr := s.GetServer()
	if sErr != nil {
		return sErr
	}
	if server.Enable {
		if err := s.applyServerConfig(server); err != nil {
			logger.Warning("Failed to apply WG config after traffic reset:", err)
		}
	}
	return nil
}

// syncInboundPort updates the port field on the WG inbound record.
func (s *WgService) syncInboundPort(db *gorm.DB, port int) {
	db.Model(&model.Inbound{}).Where("protocol = ?", model.NativeWG).Update("port", port)
}

// StartIfEnabled brings up the WG interface if it was enabled before shutdown.
func (s *WgService) StartIfEnabled() {
	server, err := s.GetServer()
	if err != nil {
		logger.Warning("WG startup check failed:", err)
		return
	}
	if !server.Enable {
		return
	}
	if wg.IsInterfaceUp(server.InterfaceName) {
		return
	}
	logger.Info("Restoring WireGuard Native interface after startup...")
	if err := s.applyServerConfig(server); err != nil {
		logger.Warning("Failed to restore WG on startup:", err)
	}
}

// applyServerConfig regenerates the config file and applies it.
func (s *WgService) applyServerConfig(server *model.WgServer) error {
	clients, err := s.GetClients()
	if err != nil {
		return err
	}

	configContent := wg.GenerateServerConfig(server, clients)

	if err := wg.WriteServerConfig(server.InterfaceName, configContent); err != nil {
		return err
	}

	if server.IPv6Enabled && server.IPv6Pool != "" {
		iface6 := s.ipv6Iface(server)
		if wg.IsNdppdInstalled() {
			if err := wg.ApplyNdppdConfig(iface6, server.InterfaceName, server.IPv6Pool); err != nil {
				logger.Warning("Failed to apply ndppd config:", err)
			}
		}
		s.applyManualNDP(server, clients)
	}

	if wg.IsInterfaceUp(server.InterfaceName) {
		return wg.SyncConfig(server.InterfaceName)
	}
	return wg.InterfaceUp(server.InterfaceName)
}

// ipv6Iface returns the external interface for IPv6.
func (s *WgService) ipv6Iface(server *model.WgServer) string {
	if server.IPv6ExternalInterface != "" {
		return server.IPv6ExternalInterface
	}
	if server.ExternalInterface != "" {
		return server.ExternalInterface
	}
	return wg.DetectDefaultInterface()
}

// applyManualNDP syncs NDP proxy entries for all WG clients.
func (s *WgService) applyManualNDP(server *model.WgServer, clients []model.WgClient) {
	iface6 := s.ipv6Iface(server)
	for _, c := range clients {
		if c.IPv6Address == "" {
			continue
		}
		if c.Enable {
			if err := wg.AddProxyNDP(c.IPv6Address, iface6); err != nil {
				logger.Warning("Failed to add NDP proxy for", c.IPv6Address, ":", err)
			}
		} else {
			if err := wg.RemoveProxyNDP(c.IPv6Address, iface6); err != nil {
				logger.Warning("Failed to remove NDP proxy for", c.IPv6Address, ":", err)
			}
		}
	}
}
