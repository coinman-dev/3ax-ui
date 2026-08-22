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
	"github.com/coinman-dev/3ax-ui/v2/shared/crypto"
	"github.com/coinman-dev/3ax-ui/v2/shared/ipam"
	"github.com/coinman-dev/3ax-ui/v2/shared/portfwd"
	"github.com/coinman-dev/3ax-ui/v2/tunnel"
)

// The panel manages two tunnel flavours that differ only in a handful of
// constants and in whether they carry Amnezia obfuscation. They used to be two
// near-identical services over two sets of tables; now there is one
// implementation, parameterised by the flavour.
//
// AwgService and WgService remain distinct types so that every existing
// zero-value use (`service.AwgService{}`, struct fields, jobs) keeps working —
// the flavour comes from the type parameter, not from a field that a zero value
// would leave empty.
type kindProvider interface{ tunnelKind() tunnel.Kind }

type awgKind struct{}

func (awgKind) tunnelKind() tunnel.Kind { return tunnel.AWG }

type wgKind struct{}

func (wgKind) tunnelKind() tunnel.Kind { return tunnel.WG }

// TunnelService manages one tunnel flavour: its server record, its clients and
// the interface on the host.
type TunnelService[K kindProvider] struct{}

type (
	AwgService = TunnelService[awgKind]
	WgService  = TunnelService[wgKind]
)

// kind is the tunnel flavour this service manages.
func (s *TunnelService[K]) kind() tunnel.Kind { var k K; return k.tunnelKind() }

// serverScope narrows a query to this flavour's server row. Both flavours now
// live in tunnel_servers, told apart by the kind column.
func (s *TunnelService[K]) serverScope(tx *gorm.DB) *gorm.DB {
	return tx.Where("kind = ?", s.kind().Name)
}

// clientScope narrows a query to the clients of this flavour's server. Without
// it a query would reach across into the other tunnel's clients, which used to
// be impossible when each flavour had its own table.
func (s *TunnelService[K]) clientScope(tx *gorm.DB) *gorm.DB {
	return tx.Where("server_id IN (?)",
		tx.Session(&gorm.Session{NewDB: true}).Model(&model.TunnelServer{}).
			Select("id").Where("kind = ?", s.kind().Name))
}

// GetServer returns the tunnel server config, creating a default one if none exists.
func (s *TunnelService[K]) GetServer() (*model.TunnelServer, error) {
	db := database.GetDB()
	var server model.TunnelServer
	err := s.serverScope(db).Attrs(model.TunnelServer{Kind: s.kind().Name}).FirstOrCreate(&server).Error
	if err != nil {
		return nil, err
	}

	needSave := false
	isInitialRecord := server.PrivateKey == "" && server.PublicKey == ""

	// Generate keys if missing
	if server.PrivateKey == "" {
		priv, pub, err := crypto.GenerateKeyPair()
		if err != nil {
			return nil, fmt.Errorf("generate server keys: %w", err)
		}
		server.PrivateKey = priv
		server.PublicKey = pub
		needSave = true
	}

	// Fresh auto-created records may still inherit the legacy fixed DB default.
	if server.ListenPort <= 0 || (isInitialRecord && server.ListenPort == s.kind().LegacyListenPort) {
		port, err := pickRandomTunnelListenPort(otherTunnelListenPort(db, s.kind().Name))
		if err != nil {
			return nil, fmt.Errorf("select %s listen port: %w", s.kind().Title, err)
		}
		server.ListenPort = port
		needSave = true
	}

	// Auto-detect external interface if not set
	if server.ExternalInterface == "" {
		server.ExternalInterface = tunnel.DetectDefaultInterface()
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
func (s *TunnelService[K]) SaveServer(server *model.TunnelServer) error {
	db := database.GetDB()

	// Reject malformed obfuscation before persisting/applying so a bad manual
	// entry can't tear the interface down on apply.
	if err := tunnel.ValidateObfuscation(server); err != nil {
		return err
	}

	if server.ListenPort <= 0 {
		port, err := pickRandomTunnelListenPort(otherTunnelListenPort(db, s.kind().Name))
		if err != nil {
			return fmt.Errorf("select %s listen port: %w", s.kind().Title, err)
		}
		server.ListenPort = port
	}

	// Detect Xray-integration changes so we can force a full interface
	// bring-down/up (syncconf does not re-execute PostUp/PostDown) and ask
	// Xray to restart so it picks up the dokodemo-door inbound additions.
	xrayDirty := false
	obfDirty := false
	var prev model.TunnelServer
	if err := db.First(&prev, server.Id).Error; err == nil {
		if prev.RouteViaXray != server.RouteViaXray ||
			prev.XrayInboundTag != server.XrayInboundTag ||
			prev.XrayTproxyPort != server.XrayTproxyPort {
			xrayDirty = true
		}
		// Obfuscation params are applied to the interface at creation time
		// (awg-quick up → setconf); awg syncconf does not reliably re-apply
		// them to a live interface, so any change needs a full bounce.
		if prev.Jc != server.Jc || prev.Jmin != server.Jmin || prev.Jmax != server.Jmax ||
			prev.S1 != server.S1 || prev.S2 != server.S2 || prev.S3 != server.S3 || prev.S4 != server.S4 ||
			prev.H1 != server.H1 || prev.H2 != server.H2 || prev.H3 != server.H3 || prev.H4 != server.H4 ||
			prev.I1 != server.I1 {
			obfDirty = true
		}
	}

	server.UpdatedAt = time.Now().UnixMilli()
	if err := db.Save(server).Error; err != nil {
		return err
	}

	// Sync listen port to the tunnel inbound record so the inbounds page shows the real port
	s.syncInboundPort(db, server.ListenPort)

	if xrayDirty {
		(&XrayService{}).SetToNeedRestart()
	}

	if server.Enable {
		if (xrayDirty || obfDirty) && tunnel.IsInterfaceUp(s.kind(), server.InterfaceName) {
			// Re-execute PostDown/PostUp and re-apply interface-level params
			// (including obfuscation) by bouncing the interface — syncconf
			// alone does not re-apply them on a live interface.
			if err := tunnel.InterfaceDown(s.kind(), server.InterfaceName); err != nil {
				logger.Warningf("%s bounce: InterfaceDown failed:: %v", s.kind().Title, err)
			}
		}
		return s.applyServerConfig(server)
	}
	return nil
}

// ResetToDefaults resets tunnel to the state right after installation:
// regenerates keys, resets obfuscation/port/MTU to defaults, removes all
// clients and the tunnel inbound, deletes the conf file — but preserves
// network settings that were configured during install (IPv6, endpoint,
// external interfaces).
func (s *TunnelService[K]) ResetToDefaults() (*model.TunnelServer, error) {
	server, err := s.GetServer()
	if err != nil {
		return nil, err
	}

	// Stop interface if running
	if server.Enable {
		tunnel.StopNdppd(s.kind())
		_ = tunnel.InterfaceDown(s.kind(), server.InterfaceName)
	}

	db := database.GetDB()

	// Delete all tunnel clients
	if err := db.Where("server_id = ?", server.Id).Delete(&model.TunnelClient{}).Error; err != nil {
		return nil, err
	}

	// Delete tunnel inbound(s)
	if err := db.Where("protocol = ?", s.kind().InboundProtocol).Delete(&model.Inbound{}).Error; err != nil {
		logger.Warningf("Failed to delete %s inbounds on reset:: %v", s.kind().Title, err)
	}

	// Remove config file from disk
	tunnel.RemoveServerConfig(s.kind(), server.InterfaceName)

	// Generate new keys
	priv, pub, err := crypto.GenerateKeyPair()
	if err != nil {
		return nil, fmt.Errorf("generate server keys: %w", err)
	}

	// Detect interface if not already set
	if server.ExternalInterface == "" {
		server.ExternalInterface = tunnel.DetectDefaultInterface()
	}

	port, err := pickRandomTunnelListenPort(otherTunnelListenPort(db, s.kind().Name))
	if err != nil {
		return nil, fmt.Errorf("select %s listen port: %w", s.kind().Title, err)
	}

	// Reset operational settings to defaults, but keep network config
	server.Enable = false
	server.ListenPort = port
	server.MTU = 1420
	server.PrivateKey = priv
	server.PublicKey = pub
	server.IPv4Address = s.kind().DefaultIPv4Address
	server.IPv4Pool = s.kind().DefaultIPv4Pool
	// Preserve: IPv6Enabled, IPv6Address, IPv6Pool, IPv6Gateway,
	//           ExternalInterface, IPv6ExternalInterface, Endpoint
	server.Jc = 4
	server.Jmin = 50
	server.Jmax = 1000
	server.S1 = 0
	server.S2 = 0
	server.S3 = 0
	server.S4 = 0
	server.H1 = "1"
	server.H2 = "2"
	server.H3 = "3"
	server.H4 = "4"
	server.I1 = ""
	server.DnsIpv4 = "1.1.1.1"
	server.DnsIpv6 = "2606:4700:4700::1111"
	server.PostUp = ""
	server.PostDown = ""
	server.TrafficReset = "never"
	hadRouteViaXray := server.RouteViaXray
	server.RouteViaXray = false
	server.XrayInboundTag = s.kind().Name + "-tproxy-in"
	server.XrayTproxyPort = s.kind().TproxyPort
	server.UpdatedAt = time.Now().UnixMilli()

	if err := db.Save(server).Error; err != nil {
		return nil, err
	}

	// If a synthetic Xray inbound was active before reset, ask Xray to
	// restart so the now-removed dokodemo-door listener goes away.
	if hadRouteViaXray {
		(&XrayService{}).SetToNeedRestart()
	}

	return server, nil
}

// GenerateObfuscation returns a freshly generated AmneziaWG 2.0 obfuscation
// parameter set for the given preset ("default" or "mobile") WITHOUT persisting
// it. The UI fills the server form with these values; saving then applies them
// (which regenerates client configs and re-applies the interface — existing
// clients must re-import their config to keep working).
func (s *TunnelService[K]) GenerateObfuscation(preset string) tunnel.Obfuscation20 {
	return tunnel.GenerateObfuscation20(preset)
}

// ToggleServer enables or disables the tunnel interface.
// Only updates the "enable" column to avoid overwriting other settings.
func (s *TunnelService[K]) ToggleServer(enable bool) error {
	server, err := s.GetServer()
	if err != nil {
		return err
	}

	db := database.GetDB()
	if err := db.Model(server).Update("enable", enable).Error; err != nil {
		return err
	}
	server.Enable = enable

	// If this tunnel feeds Xray, toggling its enabled state changes
	// which dokodemo-door inbounds Xray should expose. Schedule a
	// restart so the synthetic inbound appears/disappears in step.
	if server.RouteViaXray {
		(&XrayService{}).SetToNeedRestart()
	}

	if enable {
		return s.applyServerConfig(server)
	}
	// Disable
	tunnel.StopNdppd(s.kind())
	return tunnel.InterfaceDown(s.kind(), server.InterfaceName)
}

// TunnelStatus is the flavour-neutral status of a tunnel. The controllers
// re-shape it into the per-flavour JSON the frontend already expects
// (awgInstalled/awgVersion vs wgInstalled/wgVersion).
type TunnelStatus struct {
	Running   bool   `json:"running"`
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
}

// GetServerStatus returns basic status info.
func (s *TunnelService[K]) GetServerStatus() *TunnelStatus {
	server, err := s.GetServer()
	running := false
	if err == nil {
		running = tunnel.IsInterfaceUp(s.kind(), server.InterfaceName)
	}
	return &TunnelStatus{
		Running:   running,
		Installed: tunnel.IsInstalled(s.kind()),
		Version:   tunnel.Version(s.kind()),
	}
}

// NetworkInterface describes a system network interface with its IP capabilities.
type NetworkInterface struct {
	Name        string `json:"name"`
	IPv4        bool   `json:"ipv4"`
	IPv6        bool   `json:"ipv6"`
	DefaultIPv4 bool   `json:"defaultIPv4"`
	DefaultIPv6 bool   `json:"defaultIPv6"`
}

// GetNetworkInterfaces returns non-loopback, non-tunnel UP interfaces with IP version info.
func (s *TunnelService[K]) GetNetworkInterfaces() []NetworkInterface {
	ifaces, err := net.Interfaces()
	if err != nil {
		logger.Warning("Failed to list network interfaces:", err)
		return nil
	}
	defaultIPv4Iface, defaultIPv6Iface := detectDefaultRouteInterfaces()

	var result []NetworkInterface
	for _, iface := range ifaces {
		// Skip loopback, down, and tunnel/WG tunnel interfaces
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

		ni := NetworkInterface{
			Name:        iface.Name,
			DefaultIPv4: iface.Name == defaultIPv4Iface,
			DefaultIPv6: iface.Name == defaultIPv6Iface,
		}
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

// GetOnlineClients returns the uuids of tunnel clients online within onlineWindow.
func (s *TunnelService[K]) GetOnlineClients() []string {
	db := database.GetDB()
	threshold := time.Now().Add(-onlineWindow).UnixMilli()
	var uuids []string
	db.Model(&model.TunnelClient{}).Where(s.clientScope(db)).
		Where("enable = ? AND last_online > ?", true, threshold).
		Pluck("uuid", &uuids)
	return uuids
}

// --- Clients ---

// GetClients returns all clients for the server, enriched with live traffic stats.
func (s *TunnelService[K]) GetClients() ([]model.TunnelClient, error) {
	db := database.GetDB()
	var clients []model.TunnelClient
	if err := s.clientScope(db).Order("id asc").Find(&clients).Error; err != nil {
		return nil, err
	}
	return clients, nil
}

// GetClient returns a single client by ID.
func (s *TunnelService[K]) GetClient(id int) (*model.TunnelClient, error) {
	db := database.GetDB()
	var client model.TunnelClient
	if err := s.clientScope(db).First(&client, id).Error; err != nil {
		return nil, err
	}
	return &client, nil
}

// GetClientByUUID returns a single client by UUID.
func (s *TunnelService[K]) GetClientByUUID(clientUUID string) (*model.TunnelClient, error) {
	db := database.GetDB()
	var client model.TunnelClient
	if err := s.clientScope(db).Where("uuid = ?", clientUUID).First(&client).Error; err != nil {
		return nil, err
	}
	return &client, nil
}

// AddClient creates a new client with auto-generated keys and allocated IPs.
func (s *TunnelService[K]) AddClient(client *model.TunnelClient) error {
	server, err := s.GetServer()
	if err != nil {
		return err
	}

	// Generate UUID if not provided
	if client.UUID == "" {
		client.UUID = uuid.New().String()
	}

	// Validate UUID format
	if _, err := uuid.Parse(client.UUID); err != nil {
		return fmt.Errorf("invalid UUID format for %s client ID: %s", s.kind().Title, client.UUID)
	}

	// Check UUID uniqueness
	db := database.GetDB()
	var count int64
	db.Model(&model.TunnelClient{}).Where(s.clientScope(db)).Where("uuid = ?", client.UUID).Count(&count)
	if count > 0 {
		return fmt.Errorf("%s client with this ID already exists", s.kind().Title)
	}

	// Generate keys
	priv, pub, err := crypto.GenerateKeyPair()
	if err != nil {
		return fmt.Errorf("generate client keys: %w", err)
	}
	client.PrivateKey = priv
	client.PublicKey = pub

	psk, err := crypto.GeneratePresharedKey()
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

	ipv4, err := ipam.AllocateIPv4(server.IPv4Pool, server.IPv4Address, usedIPv4)
	if err != nil {
		return fmt.Errorf("allocate IPv4: %w", err)
	}
	client.IPv4Address = ipv4

	// Allocate IPv6 if enabled
	if server.IPv6Enabled && server.IPv6Pool != "" {
		ipv6, err := ipam.AllocateIPv6(server.IPv6Pool, server.IPv6Address, usedIPv6)
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
	client.ForwardedPorts = portfwd.Normalize(client.ForwardedPorts)

	if err := db.Create(client).Error; err != nil {
		return err
	}

	// Auto-enable server when first client is added
	if !server.Enable {
		if err := db.Model(server).Update("enable", true).Error; err != nil {
			logger.Warningf("Failed to auto-enable %s server:: %v", s.kind().Title, err)
		}
		server.Enable = true
	}

	// Capture pre-apply state to decide whether live iptables changes are needed.
	// If the interface is brought up by applyServerConfig, PostUp handles all rules.
	wasUp := tunnel.IsInterfaceUp(s.kind(), server.InterfaceName)
	if err := s.applyServerConfig(server); err != nil {
		logger.Warningf("Failed to apply %s config after adding client:: %v", s.kind().Title, err)
	}
	if wasUp {
		s.applyForwardingDiff(server, nil, client)
	}

	return nil
}

// UpdateClient updates an existing client.
func (s *TunnelService[K]) UpdateClient(client *model.TunnelClient) error {
	// Ensure UUID is always set and valid
	if client.UUID == "" {
		client.UUID = uuid.New().String()
	} else if _, err := uuid.Parse(client.UUID); err != nil {
		return fmt.Errorf("invalid UUID format for %s client ID: %s", s.kind().Title, client.UUID)
	}

	db := database.GetDB()

	// Capture old state so we can diff iptables port-forwarding rules.
	var old model.TunnelClient
	hasOld := db.First(&old, client.Id).Error == nil

	client.UpdatedAt = time.Now().UnixMilli()
	client.ForwardedPorts = portfwd.Normalize(client.ForwardedPorts)
	if err := db.Save(client).Error; err != nil {
		return err
	}

	server, err := s.GetServer()
	if err != nil {
		return err
	}
	if server.Enable {
		wasUp := tunnel.IsInterfaceUp(s.kind(), server.InterfaceName)
		if err := s.applyServerConfig(server); err != nil {
			logger.Warningf("Failed to apply %s config after updating client:: %v", s.kind().Title, err)
		}
		if wasUp {
			var oldPtr *model.TunnelClient
			if hasOld {
				oldPtr = &old
			}
			s.applyForwardingDiff(server, oldPtr, client)
		}
	}
	return nil
}

// UpdateClientByUUID updates an existing client located by UUID.
func (s *TunnelService[K]) UpdateClientByUUID(clientUUID string, client *model.TunnelClient) error {
	existing, err := s.GetClientByUUID(clientUUID)
	if err != nil {
		return err
	}
	client.Id = existing.Id
	client.UUID = existing.UUID
	return s.UpdateClient(client)
}

// DeleteClient removes a client and cleans up NDP proxy if needed.
func (s *TunnelService[K]) DeleteClient(id int) error {
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
		_ = tunnel.RemoveProxyNDP(client.IPv6Address, s.ipv6Iface(server))
	}

	db := database.GetDB()
	if err := s.clientScope(db).Delete(&model.TunnelClient{}, id).Error; err != nil {
		return err
	}

	if server.Enable {
		wasUp := tunnel.IsInterfaceUp(s.kind(), server.InterfaceName)
		if err := s.applyServerConfig(server); err != nil {
			logger.Warningf("Failed to apply %s config after deleting client:: %v", s.kind().Title, err)
		}
		if wasUp {
			s.applyForwardingDiff(server, client, nil)
		}
	}
	return nil
}

// DeleteClientByUUID removes a client identified by UUID.
func (s *TunnelService[K]) DeleteClientByUUID(clientUUID string) error {
	client, err := s.GetClientByUUID(clientUUID)
	if err != nil {
		return err
	}
	return s.DeleteClient(client.Id)
}

// DeleteAllClients stops the tunnel interface and removes all clients and the server record.
func (s *TunnelService[K]) DeleteAllClients() error {
	server, err := s.GetServer()
	if err != nil {
		// No server record — nothing to clean up
		return nil
	}

	// Bring down the interface if it's running
	if server.Enable {
		tunnel.StopNdppd(s.kind())
		_ = tunnel.InterfaceDown(s.kind(), server.InterfaceName)
	}

	db := database.GetDB()

	// Delete all tunnel clients
	if err := db.Where("server_id = ?", server.Id).Delete(&model.TunnelClient{}).Error; err != nil {
		return err
	}

	// Disable the server but keep all settings (IPv6, interfaces, etc.)
	if err := db.Model(server).Update("enable", false).Error; err != nil {
		return err
	}

	return nil
}

// ToggleClient enables or disables a client.
func (s *TunnelService[K]) ToggleClient(id int, enable bool) error {
	client, err := s.GetClient(id)
	if err != nil {
		return err
	}
	client.Enable = enable
	return s.UpdateClient(client)
}

// ToggleClientByUUID enables or disables a client identified by UUID.
func (s *TunnelService[K]) ToggleClientByUUID(clientUUID string, enable bool) error {
	client, err := s.GetClientByUUID(clientUUID)
	if err != nil {
		return err
	}
	client.Enable = enable
	return s.UpdateClient(client)
}

// GetClientConfig returns the text content of a client .conf file.
func (s *TunnelService[K]) GetClientConfig(id int) (string, error) {
	client, err := s.GetClient(id)
	if err != nil {
		return "", err
	}
	server, err := s.GetServer()
	if err != nil {
		return "", err
	}
	return tunnel.GenerateClientConfig(s.kind(), server, *client), nil
}

// GetClientConfigByUUID returns config text for a client identified by UUID.
func (s *TunnelService[K]) GetClientConfigByUUID(clientUUID string) (string, error) {
	client, err := s.GetClientByUUID(clientUUID)
	if err != nil {
		return "", err
	}
	server, err := s.GetServer()
	if err != nil {
		return "", err
	}
	return tunnel.GenerateClientConfig(s.kind(), server, *client), nil
}

// ResetClientTraffic resets upload/download counters for a client.
func (s *TunnelService[K]) ResetClientTraffic(id int) error {
	db := database.GetDB()
	return db.Model(&model.TunnelClient{}).Where(s.clientScope(db)).Where("id = ?", id).Updates(map[string]any{
		"upload":   0,
		"download": 0,
	}).Error
}

// ResetClientTrafficByUUID resets traffic counters for a client identified by UUID.
func (s *TunnelService[K]) ResetClientTrafficByUUID(clientUUID string) error {
	db := database.GetDB()
	return db.Model(&model.TunnelClient{}).Where(s.clientScope(db)).Where("uuid = ?", clientUUID).Updates(map[string]any{
		"upload":   0,
		"download": 0,
	}).Error
}

// UpdateTrafficStats reads live peer stats, updates the database, and enforces limits.
func (s *TunnelService[K]) UpdateTrafficStats() {
	server, err := s.GetServer()
	if err != nil || !server.Enable {
		return
	}

	if !tunnel.IsInterfaceUp(s.kind(), server.InterfaceName) {
		return
	}

	peers, err := tunnel.GetPeerStats(s.kind(), server.InterfaceName)
	if err != nil {
		return
	}

	db := database.GetDB()
	var clients []model.TunnelClient
	if err := s.clientScope(db).Find(&clients).Error; err != nil {
		return
	}

	// Build pubkey -> client map
	clientMap := make(map[string]*model.TunnelClient)
	for i := range clients {
		clientMap[clients[i].PublicKey] = &clients[i]
	}

	db.Transaction(func(tx *gorm.DB) error {
		for _, peer := range peers {
			client, ok := clientMap[peer.PublicKey]
			if !ok {
				continue
			}

			// Peer stats are cumulative since the interface came up and reset to
			// zero on a bounce. Accumulate the delta against the last seen raw
			// value so Upload/Download are lifetime totals that survive a bounce
			// (a drop below the baseline means the counter reset → the new value
			// is itself the delta).
			newUp := peer.TransferTx
			newDown := peer.TransferRx

			updates := map[string]any{}

			if newUp != client.LastPeerUp || newDown != client.LastPeerDown {
				deltaUp := newUp - client.LastPeerUp
				if deltaUp < 0 {
					deltaUp = newUp
				}
				deltaDown := newDown - client.LastPeerDown
				if deltaDown < 0 {
					deltaDown = newDown
				}
				updates["upload"] = client.Upload + deltaUp
				updates["download"] = client.Download + deltaDown
				updates["all_time"] = client.AllTime + deltaUp + deltaDown
				updates["last_peer_up"] = newUp
				updates["last_peer_down"] = newDown
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
				tx.Model(client).Updates(updates)
			}
		}
		return nil
	})

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
			logger.Warningf("Failed to apply %s config after enforcement:: %v", s.kind().Title, err)
		}
	}
}

// autoRenewClients extends expiry for clients with reset > 0 whose expiry has passed.
// Resets their traffic and re-enables them if needed.
// Returns true if any clients were re-enabled (config reapply needed).
func (s *TunnelService[K]) autoRenewClients(db *gorm.DB) bool {
	now := time.Now().UnixMilli()
	var clients []model.TunnelClient

	if err := s.clientScope(db).Where("reset > 0 AND expiry_time > 0 AND expiry_time <= ?", now).Find(&clients).Error; err != nil {
		logger.Warningf("%s autoRenewClients error:: %v", s.kind().Title, err)
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

		db.Model(&model.TunnelClient{}).Where(s.clientScope(db)).Where("id = ?", c.Id).Updates(updates)
		logger.Infof("%s: client '%s' auto-renewed, new expiry: %v", s.kind().Title, c.Email, time.UnixMilli(newExpiry))
	}

	return needReconfig
}

// disableInvalidClients disables tunnel clients that exceeded traffic quota or expired.
// Returns true if any clients were disabled (config reapply needed).
func (s *TunnelService[K]) disableInvalidClients(db *gorm.DB) bool {
	now := time.Now().UnixMilli()

	result := db.Model(&model.TunnelClient{}).Where(s.clientScope(db)).
		Where("enable = ? AND ((total_gb > 0 AND upload + download >= total_gb) OR (expiry_time > 0 AND expiry_time <= ?))", true, now).
		Update("enable", false)

	if result.Error != nil {
		logger.Warningf("%s disableInvalidClients error:: %v", s.kind().Title, result.Error)
		return false
	}
	if result.RowsAffected > 0 {
		logger.Infof("%s: %d client(s) disabled (quota/expiry)", s.kind().Title, result.RowsAffected)
	}
	return result.RowsAffected > 0
}

// adjustDelayedStart converts negative expiryTime (delayed start in days) to an absolute
// timestamp when the client first generates traffic.
// Returns true if any clients were updated (config reapply needed).
func (s *TunnelService[K]) adjustDelayedStart(db *gorm.DB) bool {
	now := time.Now().UnixMilli()
	var clients []model.TunnelClient

	// Find enabled clients with negative expiryTime (delayed start) that have traffic
	if err := s.clientScope(db).Where("enable = ? AND expiry_time < 0 AND (upload > 0 OR download > 0)", true).Find(&clients).Error; err != nil {
		logger.Warningf("%s adjustDelayedStart error:: %v", s.kind().Title, err)
		return false
	}

	if len(clients) == 0 {
		return false
	}

	for _, c := range clients {
		// Convert negative days to absolute expiry: now + abs(expiryTime)
		// expiryTime is stored as -86400000 * days (negative milliseconds)
		newExpiry := now + (-c.ExpiryTime)
		db.Model(&model.TunnelClient{}).Where(s.clientScope(db)).Where("id = ?", c.Id).Update("expiry_time", newExpiry)
		logger.Infof("%s: client '%s' delayed start activated, expires at %v", s.kind().Title, c.Email, time.UnixMilli(newExpiry))
	}

	return false // no reconfig needed, just updated expiry times
}

// ResetAllClientTraffics resets upload/download counters for all tunnel clients
// and re-enables clients that were disabled due to traffic quota (not expiry).
func (s *TunnelService[K]) ResetAllClientTraffics() error {
	db := database.GetDB()
	now := time.Now().UnixMilli()

	// Re-enable clients disabled by traffic quota (but not by expiry)
	db.Model(&model.TunnelClient{}).Where(s.clientScope(db)).
		Where("enable = ? AND total_gb > 0 AND (expiry_time = 0 OR expiry_time > ?)", false, now).
		Update("enable", true)

	// Reset traffic counters for all clients
	err := db.Model(&model.TunnelClient{}).Where(s.clientScope(db)).Updates(map[string]any{
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
			logger.Warningf("Failed to apply %s config after traffic reset:: %v", s.kind().Title, err)
		}
	}
	return nil
}

// DelDepletedClients deletes non-renewing (reset = 0) tunnel clients that are over
// their traffic quota or past their expiry, then reapplies the server config.
func (s *TunnelService[K]) DelDepletedClients() error {
	db := database.GetDB()
	now := time.Now().UnixMilli()
	var depleted []model.TunnelClient
	if err := s.clientScope(db).Where("reset = 0 AND ((total_gb > 0 AND upload + download >= total_gb) OR (expiry_time > 0 AND expiry_time <= ?))", now).Find(&depleted).Error; err != nil {
		return err
	}
	for _, c := range depleted {
		if err := s.DeleteClient(c.Id); err != nil {
			logger.Warningf("%s DelDepletedClients: deleting client %q failed: %v", s.kind().Title, c.Email, err)
		}
	}
	return nil
}

// syncInboundPort updates the port field on the tunnel inbound record to match the %s listen port.
func (s *TunnelService[K]) syncInboundPort(db *gorm.DB, port int) {
	db.Model(&model.Inbound{}).Where("protocol = ?", s.kind().InboundProtocol).Update("port", port)
}

// StartIfEnabled brings up the tunnel interface if it was enabled before shutdown.
// Called once during panel startup to restore tunnel state after a reboot.
func (s *TunnelService[K]) StartIfEnabled() {
	server, err := s.GetServer()
	if err != nil {
		logger.Warningf("%s startup check failed:: %v", s.kind().Title, err)
		return
	}
	if !server.Enable {
		return
	}
	if tunnel.IsInterfaceUp(s.kind(), server.InterfaceName) {
		return
	}
	logger.Info("Restoring AmneziaWG interface after startup...")
	if err := s.applyServerConfig(server); err != nil {
		logger.Warningf("Failed to restore %s on startup:: %v", s.kind().Title, err)
	}
}

// applyServerConfig regenerates the config file and applies it.
func (s *TunnelService[K]) applyServerConfig(server *model.TunnelServer) error {
	clients, err := s.GetClients()
	if err != nil {
		return err
	}

	configContent := tunnel.GenerateServerConfig(s.kind(), server, clients)

	if err := tunnel.WriteServerConfig(s.kind(), server.InterfaceName, configContent); err != nil {
		return err
	}

	// Apply NDP proxy for IPv6
	if server.IPv6Enabled && server.IPv6Pool != "" {
		iface6 := s.ipv6Iface(server)
		if tunnel.IsNdppdInstalled() {
			if err := tunnel.ApplyNdppdConfig(s.kind(), iface6, server.InterfaceName, server.IPv6Pool); err != nil {
				logger.Warning("Failed to apply ndppd config:", err)
			}
		}
		// Always sync manual NDP proxy entries: add for enabled, remove for disabled.
		// This is needed even with ndppd because PostUp adds entries via `ip -6 neigh`
		// and SyncConfig doesn't execute PostDown to clean them up.
		s.applyManualNDP(server, clients)
	}

	// Sync or restart interface
	if tunnel.IsInterfaceUp(s.kind(), server.InterfaceName) {
		return tunnel.SyncConfig(s.kind(), server.InterfaceName)
	}
	return tunnel.InterfaceUp(s.kind(), server.InterfaceName)
}

// ipv6Iface returns the external interface for IPv6, falling back to the IPv4 one.
func (s *TunnelService[K]) ipv6Iface(server *model.TunnelServer) string {
	if server.IPv6ExternalInterface != "" {
		return server.IPv6ExternalInterface
	}
	if server.ExternalInterface != "" {
		return server.ExternalInterface
	}
	return tunnel.DetectDefaultInterface()
}

// applyForwardingDiff updates iptables port-forwarding rules to reflect a
// per-client change. Caller must check that the interface is up — otherwise
// PostUp will reapply rules from the database on the next bring-up.
// old=nil means "no previous rules" (add); new=nil means "remove only".
func (s *TunnelService[K]) applyForwardingDiff(server *model.TunnelServer, old, new *model.TunnelClient) {
	iface := server.ExternalInterface
	if iface == "" {
		iface = tunnel.DetectDefaultInterface()
	}
	name := server.InterfaceName
	if name == "" {
		name = "awg0"
	}

	if old != nil && old.Enable && old.ForwardedPorts != "" && old.IPv4Address != "" {
		rules := portfwd.Rules(iface, name, old.IPv4Address, old.UUID, portfwd.Parse(old.ForwardedPorts))
		portfwd.Revoke(rules)
	}
	if new != nil && new.Enable && new.ForwardedPorts != "" && new.IPv4Address != "" {
		rules := portfwd.Rules(iface, name, new.IPv4Address, new.UUID, portfwd.Parse(new.ForwardedPorts))
		portfwd.Apply(rules)
	}
}

// applyManualNDP syncs NDP proxy entries: adds for enabled clients, removes for disabled ones.
func (s *TunnelService[K]) applyManualNDP(server *model.TunnelServer, clients []model.TunnelClient) {
	iface6 := s.ipv6Iface(server)
	for _, c := range clients {
		if c.IPv6Address == "" {
			continue
		}
		if c.Enable {
			if err := tunnel.AddProxyNDP(c.IPv6Address, iface6); err != nil {
				logger.Warning("Failed to add NDP proxy for", c.IPv6Address, ":", err)
			}
		} else {
			if err := tunnel.RemoveProxyNDP(c.IPv6Address, iface6); err != nil {
				logger.Warning("Failed to remove NDP proxy for", c.IPv6Address, ":", err)
			}
		}
	}
}
