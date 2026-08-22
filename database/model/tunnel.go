package model

// TunnelServer and TunnelClient are the merged home of the two tunnel flavours
// the panel manages. AmneziaWG and native WireGuard were kept in four tables
// (awg_servers/awg_clients/wg_servers/wg_clients) whose columns turned out to
// be identical except for AmneziaWG's twelve obfuscation fields, which the
// WireGuard rows simply leave at zero.
//
// Kind ("awg" / "wg") lives on the server; a client inherits its flavour
// through ServerId. The unique index on Kind preserves today's rule of one
// server per flavour — lift it deliberately if multiple tunnels per flavour
// ever become a feature.

// TunnelServer is one tunnel interface with its settings.
type TunnelServer struct {
	// Kind selects the flavour: TunnelKindAwg or TunnelKindWg.
	Kind string `json:"kind" gorm:"uniqueIndex;not null"`

	Id int `json:"id" gorm:"primaryKey;autoIncrement"`

	Enable        bool   `json:"enable" gorm:"default:false"`
	InterfaceName string `json:"interfaceName"`
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

	// AmneziaWG obfuscation parameters (1.x core set: Jc/Jmin/Jmax/S1/S2/H1-H4).
	Jc   int `json:"jc"`
	Jmin int `json:"jmin"`
	Jmax int `json:"jmax"`
	S1   int `json:"s1"`
	S2   int `json:"s2"`
	// AmneziaWG 2.0 additions. S3/S4 add padding to cookie/transport packets;
	// I1 is a CPS signature packet sent before handshakes. H1-H4 are stored as
	// strings so each holds either a single 1.x value ("1") or a 2.0 range
	// ("100000-800000"). Empty S3/S4/I1 ⇒ the server stays on classic 1.x output.
	S3 int    `json:"s3"`
	S4 int    `json:"s4"`
	H1 string `json:"h1"`
	H2 string `json:"h2"`
	H3 string `json:"h3"`
	H4 string `json:"h4"`
	I1 string `json:"i1"`

	// DNS pushed to clients, split by family. Composed into one DNS line in the
	// client config; the IPv6 entry is used only when IPv6 is enabled.
	DnsIpv4 string `json:"dnsIpv4" gorm:"default:'1.1.1.1'"`
	DnsIpv6 string `json:"dnsIpv6" gorm:"default:'2606:4700:4700::1111'"`

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

	// Route tunnel traffic into Xray via a dokodemo-door TPROXY inbound.
	// When enabled, PostUp installs mangle/TPROXY rules and policy routing
	// that redirect awg0 ingress to a loopback Xray inbound tagged
	// XrayInboundTag, and skips the default MASQUERADE. Routing decisions
	// (which outbound to chain to) are then made by Xray's routing rules.
	RouteViaXray   bool   `json:"routeViaXray" gorm:"default:false"`
	XrayInboundTag string `json:"xrayInboundTag"`
	XrayTproxyPort int    `json:"xrayTproxyPort"`

	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime:milli"`
	UpdatedAt int64 `json:"updatedAt" gorm:"autoUpdateTime:milli"`
}

// TunnelClient is one peer of a TunnelServer.
//
// Note for whoever switches the services over: Enable deliberately has no gorm
// default. The legacy models used default:true, which makes gorm drop an
// explicit false on Create — a client added as disabled came out enabled. The
// flip side is that new clients must now have Enable set explicitly by the
// service instead of inheriting it from the schema.
type TunnelClient struct {
	Id       int `json:"id" gorm:"primaryKey;autoIncrement"`
	ServerId int `json:"serverId" gorm:"index;uniqueIndex:idx_tunnel_client_server_email,priority:1"`

	UUID    string `json:"uuid" gorm:"uniqueIndex"`
	Name    string `json:"name"`
	Email   string `json:"email" gorm:"uniqueIndex:idx_tunnel_client_server_email,priority:2"`
	Enable  bool   `json:"enable" gorm:"index:idx_tunnel_enable_last_online,priority:1"` // no gorm default: default:true makes Create() drop an explicit false
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

	// Comma/semicolon-separated list of ports (or ranges like 8000-8100) to DNAT
	// from the server's external interface to this client. Empty = no forwarding.
	ForwardedPorts string `json:"forwardedPorts" gorm:"default:''"`

	PersistentKeepalive int `json:"persistentKeepalive" gorm:"default:25"`

	// Traffic stats. Upload/Download accumulate as lifetime totals (resettable via
	// reset-traffic) and AllTime is the absolute lifetime — same model as VLESS.
	Upload   int64 `json:"upload" gorm:"default:0"`
	Download int64 `json:"download" gorm:"default:0"`
	TotalGB  int64 `json:"totalGB" gorm:"default:0"` // traffic limit in bytes (0 = unlimited); UI stores bytes, compare against Upload+Download
	AllTime  int64 `json:"allTime" gorm:"default:0"`

	// Raw kernel per-peer transfer counters seen at the last poll. They are the
	// baseline for computing lifetime deltas, so Upload/Download survive an
	// interface bounce (the kernel resets per-peer counters to zero on bounce).
	// Internal bookkeeping — not exposed via JSON.
	LastPeerUp   int64 `json:"-" gorm:"default:0"`
	LastPeerDown int64 `json:"-" gorm:"default:0"`

	ExpiryTime int64 `json:"expiryTime" gorm:"default:0"` // 0 = never
	Reset      int   `json:"reset" gorm:"default:0"`      // auto-renew interval in days, 0 = disabled

	LimitIp    int    `json:"limitIp" gorm:"default:0"`                                                   // max simultaneous IPs, 0 = unlimited
	TgId       int64  `json:"tgId" gorm:"default:0"`                                                      // Telegram chat ID for notifications
	LastOnline int64  `json:"lastOnline" gorm:"default:0;index:idx_tunnel_enable_last_online,priority:2"` // last handshake timestamp (ms)
	LastIP     string `json:"lastIp" gorm:"default:''"`                                                   // last known endpoint IP

	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime:milli"`
	UpdatedAt int64 `json:"updatedAt" gorm:"autoUpdateTime:milli"`
}

const (
	TunnelKindAwg = "awg"
	TunnelKindWg  = "wg"
)
