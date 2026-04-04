package model

// AwgServer stores AmneziaWG server interface configuration.
type AwgServer struct {
	Id int `json:"id" gorm:"primaryKey;autoIncrement"`

	Enable        bool   `json:"enable" gorm:"default:false"`
	InterfaceName string `json:"interfaceName" gorm:"default:'awg0'"`
	ListenPort    int    `json:"listenPort" gorm:"default:51820"`
	MTU           int    `json:"mtu" gorm:"default:1420"`

	// Server keys
	PrivateKey string `json:"privateKey"`
	PublicKey  string `json:"publicKey"`

	// IPv4 tunnel network
	IPv4Address string `json:"ipv4Address" gorm:"default:'10.66.66.1/24'"`
	IPv4Pool    string `json:"ipv4Pool" gorm:"default:'10.66.66.0/24'"`

	// IPv6 — native public addresses
	IPv6Enabled bool   `json:"ipv6Enabled" gorm:"default:false"`
	IPv6Address string `json:"ipv6Address"` // server address on awg0, e.g. "2a01:xxx::1/112"
	IPv6Pool    string `json:"ipv6Pool"`    // pool for clients, e.g. "2a01:xxx::/112"
	IPv6Gateway string `json:"ipv6Gateway"` // upstream gateway for NDP

	// AmneziaWG obfuscation parameters
	Jc   int `json:"jc" gorm:"default:4"`
	Jmin int `json:"jmin" gorm:"default:50"`
	Jmax int `json:"jmax" gorm:"default:1000"`
	S1   int `json:"s1" gorm:"default:0"`
	S2   int `json:"s2" gorm:"default:0"`
	H1   int `json:"h1" gorm:"default:1"`
	H2   int `json:"h2" gorm:"default:2"`
	H3   int `json:"h3" gorm:"default:3"`
	H4   int `json:"h4" gorm:"default:4"`

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

// AwgClient stores an AmneziaWG client (peer) configuration.
type AwgClient struct {
	Id       int `json:"id" gorm:"primaryKey;autoIncrement"`
	ServerId int `json:"serverId" gorm:"index"`

	Name    string `json:"name"`
	Email   string `json:"email" gorm:"uniqueIndex"`
	Enable  bool   `json:"enable" gorm:"default:true"`
	Comment string `json:"comment"`

	// Client keys
	PrivateKey   string `json:"privateKey"`
	PublicKey    string `json:"publicKey"`
	PresharedKey string `json:"presharedKey"`

	// Allocated addresses
	IPv4Address string `json:"ipv4Address"` // e.g. "10.66.66.2/32"
	IPv6Address string `json:"ipv6Address"` // e.g. "2a01:xxx::2/128"

	// AllowedIPs on server side (what to route to this client)
	AllowedIPs string `json:"allowedIPs"`

	// AllowedIPs on client side (what to route through tunnel)
	ClientAllowedIPs string `json:"clientAllowedIPs" gorm:"default:'0.0.0.0/0,::/0'"`

	PersistentKeepalive int `json:"persistentKeepalive" gorm:"default:25"`

	// Traffic stats
	Upload   int64 `json:"upload" gorm:"default:0"`
	Download int64 `json:"download" gorm:"default:0"`
	TotalGB  int64 `json:"totalGB" gorm:"default:0"` // limit in bytes, 0 = unlimited
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
