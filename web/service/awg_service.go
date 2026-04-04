package service

import (
	"fmt"
	"net"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v2/awg"
	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
)

type AwgService struct{}

// GetServer returns the AWG server config, creating a default one if none exists.
func (s *AwgService) GetServer() (*model.AwgServer, error) {
	db := database.GetDB()
	var server model.AwgServer
	err := db.FirstOrCreate(&server).Error
	if err != nil {
		return nil, err
	}

	needSave := false

	// Generate keys if missing
	if server.PrivateKey == "" {
		priv, pub, err := awg.GenerateKeyPair()
		if err != nil {
			return nil, fmt.Errorf("generate server keys: %w", err)
		}
		server.PrivateKey = priv
		server.PublicKey = pub
		needSave = true
	}

	// Auto-detect external interface if not set
	if server.ExternalInterface == "" {
		server.ExternalInterface = awg.DetectDefaultInterface()
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
func (s *AwgService) SaveServer(server *model.AwgServer) error {
	db := database.GetDB()
	server.UpdatedAt = time.Now().UnixMilli()
	if err := db.Save(server).Error; err != nil {
		return err
	}

	if server.Enable {
		return s.applyServerConfig(server)
	}
	return nil
}

// ResetToDefaults resets all AWG server settings to defaults (as if freshly installed),
// regenerates keys, detects the correct network interface, and removes all clients.
func (s *AwgService) ResetToDefaults() (*model.AwgServer, error) {
	server, err := s.GetServer()
	if err != nil {
		return nil, err
	}

	// Stop interface if running
	if server.Enable {
		awg.StopNdppd()
		_ = awg.InterfaceDown(server.InterfaceName)
	}

	db := database.GetDB()

	// Delete all clients
	if err := db.Where("server_id = ?", server.Id).Delete(&model.AwgClient{}).Error; err != nil {
		return nil, err
	}

	// Generate new keys
	priv, pub, err := awg.GenerateKeyPair()
	if err != nil {
		return nil, fmt.Errorf("generate server keys: %w", err)
	}

	// Detect interface
	detectedIface := awg.DetectDefaultInterface()

	// Reset to defaults
	server.Enable = false
	server.InterfaceName = "awg0"
	server.ListenPort = 51820
	server.MTU = 1420
	server.PrivateKey = priv
	server.PublicKey = pub
	server.IPv4Address = "10.66.66.1/24"
	server.IPv4Pool = "10.66.66.0/24"
	server.IPv6Enabled = false
	server.IPv6Address = ""
	server.IPv6Pool = ""
	server.IPv6Gateway = ""
	server.Jc = 4
	server.Jmin = 50
	server.Jmax = 1000
	server.S1 = 0
	server.S2 = 0
	server.H1 = 1
	server.H2 = 2
	server.H3 = 3
	server.H4 = 4
	server.DNS = "1.1.1.1,2606:4700:4700::1111"
	server.ExternalInterface = detectedIface
	server.IPv6ExternalInterface = ""
	server.PostUp = ""
	server.PostDown = ""
	server.Endpoint = ""
	server.TrafficReset = "never"
	server.UpdatedAt = time.Now().UnixMilli()

	if err := db.Save(server).Error; err != nil {
		return nil, err
	}

	return server, nil
}

// ToggleServer enables or disables the AWG interface.
// Only updates the "enable" column to avoid overwriting other settings.
func (s *AwgService) ToggleServer(enable bool) error {
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
	// Disable
	awg.StopNdppd()
	return awg.InterfaceDown(server.InterfaceName)
}

// GetServerStatus returns basic status info.
type AwgStatus struct {
	Running      bool   `json:"running"`
	AwgInstalled bool   `json:"awgInstalled"`
	AwgVersion   string `json:"awgVersion"`
}

func (s *AwgService) GetServerStatus() *AwgStatus {
	server, _ := s.GetServer()
	ifaceName := "awg0"
	if server != nil {
		ifaceName = server.InterfaceName
	}
	return &AwgStatus{
		Running:      awg.IsInterfaceUp(ifaceName),
		AwgInstalled: awg.IsAwgInstalled(),
		AwgVersion:   awg.GetAwgVersion(),
	}
}

// NetworkInterface describes a system network interface with its IP capabilities.
type NetworkInterface struct {
	Name string `json:"name"`
	IPv4 bool   `json:"ipv4"`
	IPv6 bool   `json:"ipv6"`
}

// GetNetworkInterfaces returns non-loopback, non-tunnel UP interfaces with IP version info.
func (s *AwgService) GetNetworkInterfaces() []NetworkInterface {
	ifaces, err := net.Interfaces()
	if err != nil {
		logger.Warning("Failed to list network interfaces:", err)
		return nil
	}

	var result []NetworkInterface
	for _, iface := range ifaces {
		// Skip loopback, down, and AWG/WG tunnel interfaces
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
				continue // skip fe80:: and 169.254.x.x
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

// GetOnlineClients returns emails of AWG clients online within the last 3 minutes.
func (s *AwgService) GetOnlineClients() []string {
	db := database.GetDB()
	threshold := time.Now().Add(-3 * time.Minute).UnixMilli()
	var emails []string
	db.Model(&model.AwgClient{}).
		Where("enable = ? AND last_online > ?", true, threshold).
		Pluck("email", &emails)
	return emails
}

// --- Clients ---

// GetClients returns all clients for the server, enriched with live traffic stats.
func (s *AwgService) GetClients() ([]model.AwgClient, error) {
	db := database.GetDB()
	var clients []model.AwgClient
	if err := db.Order("id asc").Find(&clients).Error; err != nil {
		return nil, err
	}
	return clients, nil
}

// GetClient returns a single client by ID.
func (s *AwgService) GetClient(id int) (*model.AwgClient, error) {
	db := database.GetDB()
	var client model.AwgClient
	if err := db.First(&client, id).Error; err != nil {
		return nil, err
	}
	return &client, nil
}

// AddClient creates a new client with auto-generated keys and allocated IPs.
func (s *AwgService) AddClient(client *model.AwgClient) error {
	server, err := s.GetServer()
	if err != nil {
		return err
	}

	// Generate keys
	priv, pub, err := awg.GenerateKeyPair()
	if err != nil {
		return fmt.Errorf("generate client keys: %w", err)
	}
	client.PrivateKey = priv
	client.PublicKey = pub

	psk, err := awg.GeneratePresharedKey()
	if err != nil {
		return fmt.Errorf("generate PSK: %w", err)
	}
	client.PresharedKey = psk

	// Allocate IPv4
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

	ipv4, err := awg.AllocateIPv4(server.IPv4Pool, server.IPv4Address, usedIPv4)
	if err != nil {
		return fmt.Errorf("allocate IPv4: %w", err)
	}
	client.IPv4Address = ipv4

	// Allocate IPv6 if enabled
	if server.IPv6Enabled && server.IPv6Pool != "" {
		ipv6, err := awg.AllocateIPv6(server.IPv6Pool, server.IPv6Address, usedIPv6)
		if err != nil {
			return fmt.Errorf("allocate IPv6: %w", err)
		}
		client.IPv6Address = ipv6
	}

	// Build server-side AllowedIPs
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

	db := database.GetDB()
	if err := db.Create(client).Error; err != nil {
		return err
	}

	// Auto-enable server when first client is added
	if !server.Enable {
		if err := db.Model(server).Update("enable", true).Error; err != nil {
			logger.Warning("Failed to auto-enable AWG server:", err)
		}
		server.Enable = true
	}

	// Apply config to running server
	if err := s.applyServerConfig(server); err != nil {
		logger.Warning("Failed to apply AWG config after adding client:", err)
	}

	return nil
}

// UpdateClient updates an existing client.
func (s *AwgService) UpdateClient(client *model.AwgClient) error {
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
			logger.Warning("Failed to apply AWG config after updating client:", err)
		}
	}
	return nil
}

// DeleteClient removes a client and cleans up NDP proxy if needed.
func (s *AwgService) DeleteClient(id int) error {
	client, err := s.GetClient(id)
	if err != nil {
		return err
	}

	server, err := s.GetServer()
	if err != nil {
		return err
	}

	// Remove NDP proxy entry
	if server.IPv6Enabled && client.IPv6Address != "" {
		_ = awg.RemoveProxyNDP(client.IPv6Address, s.ipv6Iface(server))
	}

	db := database.GetDB()
	if err := db.Delete(&model.AwgClient{}, id).Error; err != nil {
		return err
	}

	if server.Enable {
		if err := s.applyServerConfig(server); err != nil {
			logger.Warning("Failed to apply AWG config after deleting client:", err)
		}
	}
	return nil
}

// DeleteAllClients stops the AWG interface and removes all clients and the server record.
func (s *AwgService) DeleteAllClients() error {
	server, err := s.GetServer()
	if err != nil {
		// No server record — nothing to clean up
		return nil
	}

	// Bring down the interface if it's running
	if server.Enable {
		awg.StopNdppd()
		_ = awg.InterfaceDown(server.InterfaceName)
	}

	db := database.GetDB()

	// Delete all AWG clients
	if err := db.Where("server_id = ?", server.Id).Delete(&model.AwgClient{}).Error; err != nil {
		return err
	}

	// Disable the server but keep all settings (IPv6, interfaces, etc.)
	if err := db.Model(server).Update("enable", false).Error; err != nil {
		return err
	}

	return nil
}

// ToggleClient enables or disables a client.
func (s *AwgService) ToggleClient(id int, enable bool) error {
	client, err := s.GetClient(id)
	if err != nil {
		return err
	}
	client.Enable = enable
	return s.UpdateClient(client)
}

// GetClientConfig returns the text content of a client .conf file.
func (s *AwgService) GetClientConfig(id int) (string, error) {
	client, err := s.GetClient(id)
	if err != nil {
		return "", err
	}
	server, err := s.GetServer()
	if err != nil {
		return "", err
	}
	return awg.GenerateClientConfig(server, client), nil
}

// ResetClientTraffic resets upload/download counters for a client.
func (s *AwgService) ResetClientTraffic(id int) error {
	db := database.GetDB()
	return db.Model(&model.AwgClient{}).Where("id = ?", id).Updates(map[string]any{
		"upload":   0,
		"download": 0,
	}).Error
}

// UpdateTrafficStats reads live peer stats, updates the database, and enforces limits.
func (s *AwgService) UpdateTrafficStats() {
	server, err := s.GetServer()
	if err != nil || !server.Enable {
		return
	}

	if !awg.IsInterfaceUp(server.InterfaceName) {
		return
	}

	peers, err := awg.GetPeerStats(server.InterfaceName)
	if err != nil {
		return
	}

	db := database.GetDB()
	var clients []model.AwgClient
	if err := db.Find(&clients).Error; err != nil {
		return
	}

	// Build pubkey -> client map
	clientMap := make(map[string]*model.AwgClient)
	for i := range clients {
		clientMap[clients[i].PublicKey] = &clients[i]
	}

	for _, peer := range peers {
		client, ok := clientMap[peer.PublicKey]
		if !ok {
			continue
		}

		// Calculate delta (peer stats are cumulative since interface up)
		newUp := peer.TransferTx // from server perspective: TX to peer = client's upload perspective is reversed
		newDown := peer.TransferRx

		updates := map[string]any{}

		// Update traffic if changed
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

		// Update last online from handshake timestamp (seconds → milliseconds)
		if peer.LatestHandshake > 0 {
			handshakeMs := peer.LatestHandshake * 1000
			if handshakeMs != client.LastOnline {
				updates["last_online"] = handshakeMs
			}
		}

		// Update last known endpoint IP
		if peer.Endpoint != "" && peer.Endpoint != "(none)" {
			// Strip port from endpoint (e.g. "1.2.3.4:51820" → "1.2.3.4")
			ep := peer.Endpoint
			if idx := strings.LastIndex(ep, ":"); idx > 0 {
				ep = ep[:idx]
			}
			// Strip brackets from IPv6 (e.g. "[::1]" → "::1")
			ep = strings.TrimPrefix(strings.TrimSuffix(ep, "]"), "[")
			if ep != client.LastIP {
				updates["last_ip"] = ep
			}
		}

		if len(updates) > 0 {
			db.Model(client).Updates(updates)
		}
	}

	// Auto-renew, activate delayed-start, and enforce limits
	needReconfig := s.autoRenewClients(db)
	if s.adjustDelayedStart(db) {
		needReconfig = true
	}
	if s.disableInvalidClients(db) {
		needReconfig = true
	}

	if needReconfig {
		if err := s.applyServerConfig(server); err != nil {
			logger.Warning("Failed to apply AWG config after enforcement:", err)
		}
	}
}

// autoRenewClients extends expiry for clients with reset > 0 whose expiry has passed.
// Resets their traffic and re-enables them if needed.
// Returns true if any clients were re-enabled (config reapply needed).
func (s *AwgService) autoRenewClients(db *gorm.DB) bool {
	now := time.Now().UnixMilli()
	var clients []model.AwgClient

	if err := db.Where("reset > 0 AND expiry_time > 0 AND expiry_time <= ?", now).Find(&clients).Error; err != nil {
		logger.Warning("AWG autoRenewClients error:", err)
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

		db.Model(&model.AwgClient{}).Where("id = ?", c.Id).Updates(updates)
		logger.Infof("AWG: client '%s' auto-renewed, new expiry: %v", c.Email, time.UnixMilli(newExpiry))
	}

	return needReconfig
}

// disableInvalidClients disables AWG clients that exceeded traffic quota or expired.
// Returns true if any clients were disabled (config reapply needed).
func (s *AwgService) disableInvalidClients(db *gorm.DB) bool {
	now := time.Now().UnixMilli()

	result := db.Model(&model.AwgClient{}).
		Where("enable = ? AND ((total_gb > 0 AND upload + download >= total_gb) OR (expiry_time > 0 AND expiry_time <= ?))", true, now).
		Update("enable", false)

	if result.Error != nil {
		logger.Warning("AWG disableInvalidClients error:", result.Error)
		return false
	}
	if result.RowsAffected > 0 {
		logger.Infof("AWG: %d client(s) disabled (quota/expiry)", result.RowsAffected)
	}
	return result.RowsAffected > 0
}

// adjustDelayedStart converts negative expiryTime (delayed start in days) to an absolute
// timestamp when the client first generates traffic.
// Returns true if any clients were updated (config reapply needed).
func (s *AwgService) adjustDelayedStart(db *gorm.DB) bool {
	now := time.Now().UnixMilli()
	var clients []model.AwgClient

	// Find enabled clients with negative expiryTime (delayed start) that have traffic
	if err := db.Where("enable = ? AND expiry_time < 0 AND (upload > 0 OR download > 0)", true).Find(&clients).Error; err != nil {
		logger.Warning("AWG adjustDelayedStart error:", err)
		return false
	}

	if len(clients) == 0 {
		return false
	}

	for _, c := range clients {
		// Convert negative days to absolute expiry: now + abs(expiryTime)
		// expiryTime is stored as -86400000 * days (negative milliseconds)
		newExpiry := now + (-c.ExpiryTime)
		db.Model(&model.AwgClient{}).Where("id = ?", c.Id).Update("expiry_time", newExpiry)
		logger.Infof("AWG: client '%s' delayed start activated, expires at %v", c.Email, time.UnixMilli(newExpiry))
	}

	return false // no reconfig needed, just updated expiry times
}

// ResetAllClientTraffics resets upload/download counters for all AWG clients
// and re-enables clients that were disabled due to traffic quota (not expiry).
func (s *AwgService) ResetAllClientTraffics() error {
	db := database.GetDB()
	now := time.Now().UnixMilli()

	// Re-enable clients disabled by traffic quota (but not by expiry)
	db.Model(&model.AwgClient{}).
		Where("enable = ? AND total_gb > 0 AND (expiry_time = 0 OR expiry_time > ?)", false, now).
		Update("enable", true)

	// Reset traffic counters for all clients
	err := db.Model(&model.AwgClient{}).Updates(map[string]any{
		"upload":   0,
		"download": 0,
	}).Error
	if err != nil {
		return err
	}

	// Reapply config to add re-enabled peers back
	server, sErr := s.GetServer()
	if sErr != nil {
		return sErr
	}
	if server.Enable {
		if err := s.applyServerConfig(server); err != nil {
			logger.Warning("Failed to apply AWG config after traffic reset:", err)
		}
	}
	return nil
}

// applyServerConfig regenerates the config file and applies it.
func (s *AwgService) applyServerConfig(server *model.AwgServer) error {
	clients, err := s.GetClients()
	if err != nil {
		return err
	}

	configContent := awg.GenerateServerConfig(server, clients)

	if err := awg.WriteServerConfig(server.InterfaceName, configContent); err != nil {
		return err
	}

	// Apply NDP proxy for IPv6
	if server.IPv6Enabled && server.IPv6Pool != "" {
		iface6 := s.ipv6Iface(server)
		if awg.IsNdppdInstalled() {
			if err := awg.ApplyNdppdConfig(iface6, server.InterfaceName, server.IPv6Pool); err != nil {
				logger.Warning("Failed to apply ndppd config:", err)
				// Fallback to per-client NDP proxy
				s.applyManualNDP(server, clients)
			}
		} else {
			s.applyManualNDP(server, clients)
		}
	}

	// Sync or restart interface
	if awg.IsInterfaceUp(server.InterfaceName) {
		return awg.SyncConfig(server.InterfaceName)
	}
	return awg.InterfaceUp(server.InterfaceName)
}

// ipv6Iface returns the external interface for IPv6, falling back to the IPv4 one.
func (s *AwgService) ipv6Iface(server *model.AwgServer) string {
	if server.IPv6ExternalInterface != "" {
		return server.IPv6ExternalInterface
	}
	if server.ExternalInterface != "" {
		return server.ExternalInterface
	}
	return awg.DetectDefaultInterface()
}

// applyManualNDP adds per-client NDP proxy entries.
func (s *AwgService) applyManualNDP(server *model.AwgServer, clients []model.AwgClient) {
	iface6 := s.ipv6Iface(server)
	for _, c := range clients {
		if c.Enable && c.IPv6Address != "" {
			if err := awg.AddProxyNDP(c.IPv6Address, iface6); err != nil {
				logger.Warning("Failed to add NDP proxy for", c.IPv6Address, ":", err)
			}
		}
	}
}
