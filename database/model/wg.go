package model

// WgServer stores native WireGuard server interface configuration.
type WgServer struct {
	Id int `json:"id" gorm:"primaryKey;autoIncrement"`

	Enable        bool   `json:"enable" gorm:"default:false"`
	InterfaceName string `json:"interfaceName" gorm:"default:'wg0'"`
	ListenPort    int    `json:"listenPort" gorm:"default:51821"`
	MTU           int    `json:"mtu" gorm:"default:1420"`

	// Server keys
	PrivateKey string `json:"privateKey"`
	PublicKey  string `json:"publicKey"`

	// IPv4 tunnel network
	IPv4Address string `json:"ipv4Address" gorm:"default:'10.77.77.1/24'"`
	IPv4Pool    string `json:"ipv4Pool" gorm:"default:'10.77.77.0/24'"`

	// IPv6 — native public addresses
	IPv6Enabled bool   `json:"ipv6Enabled" gorm:"default:false"`
	IPv6Address string `json:"ipv6Address"` // server address on wg0, e.g. "2a01:xxx::1/112"
	IPv6Pool    string `json:"ipv6Pool"`    // pool for clients, e.g. "2a01:xxx::/112"
	IPv6Gateway string `json:"ipv6Gateway"` // upstream gateway for NDP

	// DNS pushed to clients
	DNS string `json:"dns" gorm:"default:'1.1.1.1,2606:4700:4700::1111'"`

	// External interface for NAT (IPv4)
	ExternalInterface string `json:"externalInterface" gorm:"default:''"`

	// External interface for NDP proxy / IPv6 forwarding (may differ from IPv4)
	IPv6ExternalInterface string `json:"ipv6ExternalInterface" gorm:"default:''"`

	// PostUp / PostDown scripts (auto-generated but overridable)
	PostUp   string `json:"postUp"`
	PostDown string `json:"postDown"`

	// Endpoint that clients connect to (server public IP/domain)
	Endpoint string `json:"endpoint"`

	// Periodic traffic reset: never, daily, weekly, monthly
	TrafficReset string `json:"trafficReset" gorm:"default:'never'"`

	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime:milli"`
	UpdatedAt int64 `json:"updatedAt" gorm:"autoUpdateTime:milli"`
}

// WgClient stores a native WireGuard client (peer) configuration.
type WgClient struct {
	Id       int `json:"id" gorm:"primaryKey;autoIncrement"`
	ServerId int `json:"serverId" gorm:"index"`

	UUID    string `json:"uuid" gorm:"index"`
	Name    string `json:"name"`
	Email   string `json:"email" gorm:"uniqueIndex"`
	Enable  bool   `json:"enable" gorm:"default:true"`
	Comment string `json:"comment"`

	// Client keys
	PrivateKey   string `json:"privateKey"`
	PublicKey    string `json:"publicKey"`
	PresharedKey string `json:"presharedKey"`

	// Allocated addresses
	IPv4Address string `json:"ipv4Address"` // e.g. "10.77.77.2/32"
	IPv6Address string `json:"ipv6Address"` // e.g. "2a01:xxx::2/128"

	// AllowedIPs on server side (what to route to this client)
	AllowedIPs string `json:"allowedIPs"`

	// AllowedIPs on client side (what to route through tunnel)
	ClientAllowedIPs string `json:"clientAllowedIPs" gorm:"default:'0.0.0.0/0,::/0'"`

	PersistentKeepalive int `json:"persistentKeepalive" gorm:"default:25"`

	// Traffic stats
	Upload   int64 `json:"upload" gorm:"default:0"`
	Download int64 `json:"download" gorm:"default:0"`
	TotalGB  int64 `json:"totalGB" gorm:"default:0"` // traffic limit in bytes (0 = unlimited)
	AllTime  int64 `json:"allTime" gorm:"default:0"`

	ExpiryTime int64 `json:"expiryTime" gorm:"default:0"` // 0 = never
	Reset      int   `json:"reset" gorm:"default:0"`      // auto-renew interval in days, 0 = disabled

	LimitIp    int    `json:"limitIp" gorm:"default:0"`    // max simultaneous IPs, 0 = unlimited
	TgId       int64  `json:"tgId" gorm:"default:0"`       // Telegram chat ID for notifications
	LastOnline int64  `json:"lastOnline" gorm:"default:0"` // last handshake timestamp (ms)
	LastIP     string `json:"lastIp" gorm:"default:''"`    // last known endpoint IP

	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime:milli"`
	UpdatedAt int64 `json:"updatedAt" gorm:"autoUpdateTime:milli"`
}
