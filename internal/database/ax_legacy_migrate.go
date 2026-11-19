package database

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/random"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// axLegacySeeder marks a 3AX-UI 1.x database as converted. The 1.x tables are
// left in place untouched; the pre-migration copy is the way back.
const axLegacySeeder = "3AXLegacy1x"

// AxLegacyHostCleanupKey is the setting that lists the interfaces 1.x ran with
// awg-quick / wg-quick; internal/axlegacy takes them down once, then drops it.
const AxLegacyHostCleanupKey = "axLegacyHostCleanup"

// AxLegacyInterface is one 1.x tunnel interface: kind "awg" or "wg".
type AxLegacyInterface struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type axTunnelServer struct {
	Id                     int
	Kind                   string
	Enable                 bool
	InterfaceName          string
	ListenPort             int
	MTU                    int
	PrivateKey             string
	PublicKey              string
	IPv4Address            string `gorm:"column:ipv4_address"`
	IPv6Enabled            bool   `gorm:"column:ipv6_enabled"`
	IPv6Address            string `gorm:"column:ipv6_address"`
	IPv6Pool               string `gorm:"column:ipv6_pool"`
	Jc, Jmin, Jmax         int
	S1, S2, S3, S4         int
	H1, H2, H3, H4         string
	I1, I2, I3, I4, I5     string
	HeaderProtectionKey    string
	ContentPaddingAddition string
	RekeyAfterTime         string
	RekeyTimeout           string
	RejectAfterTime        string
	KeepaliveTimeout       string
	MaxHandshakeAttempts   string
	RandomTrailers         bool
	DisableCookies         bool
	DnsIpv4                string `gorm:"column:dns_ipv4"`
	DnsIpv6                string `gorm:"column:dns_ipv6"`
	ExternalInterface      string
	IPv6ExternalInterface  string `gorm:"column:ipv6_external_interface"`
	Endpoint               string
	RouteViaXray           bool
	XrayInboundTag         string
}

type axTunnelClient struct {
	ServerId            int
	Name                string
	Email               string
	Enable              bool
	Comment             string
	PrivateKey          string
	PublicKey           string
	PresharedKey        string
	IPv4Address         string `gorm:"column:ipv4_address"`
	IPv6Address         string `gorm:"column:ipv6_address"`
	AllowedIPs          string `gorm:"column:allowed_ips"`
	ForwardedPorts      string
	PersistentKeepalive int
	Upload, Download    int64
	TotalGB             int64 `gorm:"column:total_gb"`
	ExpiryTime          int64
	Reset               int
	LimitIp             int
	TgId                int64
	LastOnline          int64
	CreatedAt           int64
	UpdatedAt           int64
}

type axMtprotoClient struct {
	InboundId        int
	Email            string
	Enable           bool
	Secret           string
	Comment          string
	SubId            string
	Upload, Download int64
	TotalGB          int64 `gorm:"column:total_gb"`
	ExpiryTime       int64
	Reset            int
	LimitIp          int
	TgId             int64
	LastOnline       int64
	CreatedAt        int64
	UpdatedAt        int64
}

// migrateAx1xDatabase converts what only 3AX-UI 1.x stored into this base's
// shapes, before upstream's seeders read inbound JSON into the clients table.
func migrateAx1xDatabase() error {
	seeded, err := isTableEmpty("history_of_seeders")
	if err != nil || seeded {
		// A fresh database: no 1.x data, and upstream must still see an empty history.
		return err
	}
	var done int64
	if err := db.Model(&model.HistoryOfSeeders{}).Where("seeder_name = ?", axLegacySeeder).Count(&done).Error; err != nil {
		return err
	}
	if done > 0 {
		return nil
	}
	if !isAx1xDatabase() {
		return db.Create(&model.HistoryOfSeeders{SeederName: axLegacySeeder}).Error
	}
	if !IsPostgres() {
		if err := backupAx1xDatabase(); err != nil {
			return fmt.Errorf("backing up the 3AX-UI 1.x database before migrating it: %w", err)
		}
	}
	return db.Transaction(func(tx *gorm.DB) error {
		m := axMigration{tx: tx, used: map[string]string{}, tagRenames: map[string]string{}}
		if err := m.run(); err != nil {
			return err
		}
		return tx.Create(&model.HistoryOfSeeders{SeederName: axLegacySeeder}).Error
	})
}

func isAx1xDatabase() bool {
	if db.Migrator().HasTable("tunnel_servers") || db.Migrator().HasTable("mtproto_clients") {
		return true
	}
	var n int64
	db.Model(&model.Inbound{}).Where("protocol = ?", "nativewg").Count(&n)
	return n > 0
}

func backupAx1xDatabase() error {
	var files []struct {
		Name string
		File string
	}
	if err := db.Raw("PRAGMA database_list").Scan(&files).Error; err != nil {
		return err
	}
	for _, f := range files {
		if f.Name == "main" && f.File != "" {
			path := fmt.Sprintf("%s.3ax-1x-backup-%d", f.File, time.Now().Unix())
			log.Printf("3AX-UI 1.x database found; copying it to %s before migrating", path)
			return db.Exec("VACUUM INTO ?", path).Error
		}
	}
	return fmt.Errorf("no file behind the main database")
}

type axMigration struct {
	tx         *gorm.DB
	used       map[string]string // lower-cased email -> the source that owns it
	tagRenames map[string]string // 1.x TPROXY inbound tag -> the tunnel inbound's tag
	userID     int
	interfaces []AxLegacyInterface
}

func (m *axMigration) run() error {
	var legacy []model.Inbound
	if err := m.tx.Where("protocol IN ?", []string{"amneziawg", "nativewg", "mtproto"}).Find(&legacy).Error; err != nil {
		return err
	}
	legacyIDs := make([]int, 0, len(legacy))
	for _, ib := range legacy {
		legacyIDs = append(legacyIDs, ib.Id)
	}
	if err := m.claimExistingEmails(legacyIDs); err != nil {
		return err
	}
	var firstUser model.User
	if err := m.tx.Order("id").First(&firstUser).Error; err == nil {
		m.userID = firstUser.Id
	}
	if m.tx.Migrator().HasTable("tunnel_servers") {
		for _, kind := range []string{"awg", "wg"} {
			if err := m.migrateTunnel(kind, legacy); err != nil {
				return err
			}
		}
	}
	// A tunnel inbound with no 1.x server behind it never had a config to run.
	for _, ib := range legacy {
		if ib.Protocol != "amneziawg" && ib.Protocol != "nativewg" {
			continue
		}
		var cur model.Inbound
		if err := m.tx.First(&cur, ib.Id).Error; err != nil {
			return err
		}
		if string(cur.Protocol) == "nativewg" || !strings.Contains(cur.Settings, `"server"`) {
			log.Printf("3AX-UI 1.x migration: removing %s inbound %d (%s): no server settings behind it", ib.Protocol, ib.Id, ib.Remark)
			if err := m.tx.Where("inbound_id = ?", ib.Id).Delete(&xray.ClientTraffic{}).Error; err != nil {
				return err
			}
			if err := m.tx.Delete(&model.Inbound{}, ib.Id).Error; err != nil {
				return err
			}
		}
	}
	if m.tx.Migrator().HasTable("mtproto_clients") {
		for _, ib := range legacy {
			if ib.Protocol == "mtproto" {
				if err := m.migrateMtproto(ib); err != nil {
					return err
				}
			}
		}
	}
	if err := m.renameRoutingTags(); err != nil {
		return err
	}
	return m.recordHostCleanup()
}

func (m *axMigration) recordHostCleanup() error {
	if len(m.interfaces) == 0 {
		return nil
	}
	raw, err := json.Marshal(m.interfaces)
	if err != nil {
		return err
	}
	return m.tx.Create(&model.Setting{Key: AxLegacyHostCleanupKey, Value: string(raw)}).Error
}

// claimExistingEmails reserves every client name outside the 1.x tunnel and
// MTProto inbounds, so a clashing 1.x name is renamed instead of merged.
func (m *axMigration) claimExistingEmails(legacyIDs []int) error {
	var emails []string
	q := m.tx.Model(&xray.ClientTraffic{})
	if len(legacyIDs) > 0 {
		q = q.Where("inbound_id NOT IN ?", legacyIDs)
	}
	if err := q.Pluck("email", &emails).Error; err != nil {
		return err
	}
	var others []model.Inbound
	q = m.tx.Model(&model.Inbound{})
	if len(legacyIDs) > 0 {
		q = q.Where("id NOT IN ?", legacyIDs)
	}
	if err := q.Find(&others).Error; err != nil {
		return err
	}
	for _, ib := range others {
		var s struct {
			Clients []struct {
				Email string `json:"email"`
			} `json:"clients"`
		}
		if json.Unmarshal([]byte(ib.Settings), &s) == nil {
			for _, c := range s.Clients {
				emails = append(emails, c.Email)
			}
		}
	}
	for _, e := range emails {
		if e = strings.TrimSpace(e); e != "" {
			m.used[strings.ToLower(e)] = "existing"
		}
	}
	return nil
}

// claimEmail keeps a name when it is free; a repeat inside the same source
// gets a number, a clash with another source gets the source's suffix.
func (m *axMigration) claimEmail(base, source string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = source
	}
	free := func(e string) bool {
		_, taken := m.used[strings.ToLower(e)]
		return !taken
	}
	pick := base
	if !free(pick) {
		stem := base
		if m.used[strings.ToLower(base)] != source {
			stem = base + "-" + source
		}
		pick = stem
		for n := 2; !free(pick); n++ {
			pick = fmt.Sprintf("%s-%d", stem, n)
		}
		log.Printf("3AX-UI 1.x migration: client %q renamed to %q (name already used)", base, pick)
	}
	m.used[strings.ToLower(pick)] = source
	return pick
}

func (m *axMigration) migrateTunnel(kind string, legacy []model.Inbound) error {
	var servers []axTunnelServer
	if err := m.tx.Table("tunnel_servers").Where("kind = ?", kind).Find(&servers).Error; err != nil {
		return err
	}
	if len(servers) == 0 {
		return nil
	}
	srv := servers[0]
	iface := strings.TrimSpace(srv.InterfaceName)
	if iface == "" {
		iface = kind + "0"
	}
	m.interfaces = append(m.interfaces, AxLegacyInterface{Kind: kind, Name: iface})
	if kind == "wg" {
		srv = plainWireGuard(srv)
	}
	var tunnelClients []axTunnelClient
	if err := m.tx.Table("tunnel_clients").Where("server_id = ?", srv.Id).Order("id").Find(&tunnelClients).Error; err != nil {
		return err
	}

	protocol := map[string]string{"awg": "amneziawg", "wg": "nativewg"}[kind]
	var row *model.Inbound
	for i := range legacy {
		if string(legacy[i].Protocol) == protocol {
			row = &legacy[i]
			break
		}
	}
	if row == nil && !srv.Enable && len(tunnelClients) == 0 {
		return nil
	}

	clients := make([]model.Client, 0, len(tunnelClients))
	traffic := make([]xray.ClientTraffic, 0, len(tunnelClients))
	for _, tc := range tunnelClients {
		c := tunnelClientToClient(tc)
		c.Email = m.claimEmail(tc.Email, kind)
		clients = append(clients, c)
		traffic = append(traffic, xray.ClientTraffic{
			Email: c.Email, Enable: tc.Enable, Up: tc.Upload, Down: tc.Download,
			Total: tc.TotalGB, ExpiryTime: tc.ExpiryTime, Reset: tc.Reset, LastOnline: tc.LastOnline,
		})
	}
	settings, err := tunnelInboundSettings(srv, clients)
	if err != nil {
		return err
	}
	shareStrategy, shareAddr := "node", ""
	if host := endpointHost(srv.Endpoint); host != "" {
		shareStrategy, shareAddr = "custom", host
	}

	if row != nil {
		if err := m.tx.Where("inbound_id = ?", row.Id).Delete(&xray.ClientTraffic{}).Error; err != nil {
			return err
		}
		if err := m.tx.Model(&model.Inbound{}).Where("id = ?", row.Id).Updates(map[string]any{
			"protocol": string(model.AmneziaWG), "port": srv.ListenPort, "settings": settings, "enable": srv.Enable,
			"share_addr_strategy": shareStrategy, "share_addr": shareAddr,
		}).Error; err != nil {
			return err
		}
	} else {
		remark := map[string]string{"awg": "AmneziaWG", "wg": "WireGuard"}[kind]
		created := model.Inbound{
			UserId: m.userID, Remark: remark, Enable: srv.Enable, Port: srv.ListenPort,
			Protocol: model.AmneziaWG, Settings: settings, Tag: m.freeTag(fmt.Sprintf("inbound-%d", srv.ListenPort)),
			TrafficReset: "never", ShareAddrStrategy: shareStrategy, ShareAddr: shareAddr,
		}
		if err := m.tx.Create(&created).Error; err != nil {
			return err
		}
		row = &created
	}
	for i := range traffic {
		traffic[i].InboundId = row.Id
		if err := m.tx.Create(&traffic[i]).Error; err != nil {
			return err
		}
	}
	if srv.XrayInboundTag != "" {
		m.tagRenames[srv.XrayInboundTag] = row.Tag
	}
	log.Printf("3AX-UI 1.x migration: %s server -> AmneziaWG inbound %d (%s) with %d clients", kind, row.Id, row.Tag, len(clients))
	return nil
}

// plainWireGuard clears every AmneziaWG knob: with all of them at zero or
// blank the server speaks plain WireGuard, so 1.x WireGuard clients keep working.
func plainWireGuard(s axTunnelServer) axTunnelServer {
	s.Jc, s.Jmin, s.Jmax = 0, 0, 0
	s.S1, s.S2, s.S3, s.S4 = 0, 0, 0, 0
	s.H1, s.H2, s.H3, s.H4 = "", "", "", ""
	s.I1, s.I2, s.I3, s.I4, s.I5 = "", "", "", "", ""
	s.HeaderProtectionKey, s.ContentPaddingAddition = "", ""
	s.RekeyAfterTime, s.RekeyTimeout, s.RejectAfterTime, s.KeepaliveTimeout, s.MaxHandshakeAttempts = "", "", "", "", ""
	s.RandomTrailers, s.DisableCookies = false, false
	return s
}

func tunnelClientToClient(tc axTunnelClient) model.Client {
	var allowed []string
	for _, a := range []string{tc.IPv4Address, tc.IPv6Address} {
		if a = strings.TrimSpace(a); a != "" {
			allowed = append(allowed, a)
		}
	}
	if len(allowed) == 0 {
		for _, a := range strings.Split(tc.AllowedIPs, ",") {
			if a = strings.TrimSpace(a); a != "" {
				allowed = append(allowed, a)
			}
		}
	}
	comment := tc.Comment
	if comment == "" && tc.Name != "" && tc.Name != tc.Email {
		comment = tc.Name
	}
	c := model.Client{
		Enable: tc.Enable, PrivateKey: tc.PrivateKey, PublicKey: tc.PublicKey, PreSharedKey: tc.PresharedKey,
		AllowedIPs: allowed, TotalGB: tc.TotalGB, ExpiryTime: tc.ExpiryTime, Reset: tc.Reset, LimitIP: tc.LimitIp,
		TgID: tc.TgId, SubID: random.NumLower(16), Comment: comment, CreatedAt: tc.CreatedAt, UpdatedAt: tc.UpdatedAt,
	}
	if tc.PersistentKeepalive > 0 {
		ka := model.KeepAliveValue(fmt.Sprint(tc.PersistentKeepalive))
		c.KeepAlive = &ka
	}
	if fp := strings.TrimSpace(tc.ForwardedPorts); fp != "" {
		c.ForwardedPorts = &fp
	}
	return c
}

func tunnelInboundSettings(s axTunnelServer, clients []model.Client) (string, error) {
	subnetIP, subnetCIDR := "10.66.66.0", 24
	if p, err := netip.ParsePrefix(strings.TrimSpace(s.IPv4Address)); err == nil && p.Addr().Is4() {
		subnetIP, subnetCIDR = p.Masked().Addr().String(), p.Bits()
	}
	ipv6Subnet := strings.TrimSpace(s.IPv6Pool)
	if ipv6Subnet == "" {
		if p, err := netip.ParsePrefix(strings.TrimSpace(s.IPv6Address)); err == nil && p.Addr().Is6() {
			ipv6Subnet = p.Masked().String()
		}
	}
	server := amneziawg.ServerSettings{
		PrivateKey: s.PrivateKey, PublicKey: s.PublicKey, SubnetIP: subnetIP, SubnetCIDR: subnetCIDR, MTU: s.MTU,
		PrimaryDNS: s.DnsIpv4, SecondaryDNS: s.DnsIpv6, ExternalInterface: s.ExternalInterface,
		IPv6Enabled: s.IPv6Enabled, IPv6Subnet: ipv6Subnet, IPv6ExternalInterface: s.IPv6ExternalInterface,
		RouteThroughXray: s.RouteViaXray, KernelMode: true,
		Jc: s.Jc, Jmin: s.Jmin, Jmax: s.Jmax, S1: s.S1, S2: s.S2, S3: s.S3, S4: s.S4,
		H1: s.H1, H2: s.H2, H3: s.H3, H4: s.H4, I1: s.I1, I2: s.I2, I3: s.I3, I4: s.I4, I5: s.I5,
		HeaderProtectionKey: s.HeaderProtectionKey, ContentPaddingAddition: s.ContentPaddingAddition,
		RekeyAfterTime: s.RekeyAfterTime, RekeyTimeout: s.RekeyTimeout, RejectAfterTime: s.RejectAfterTime,
		KeepaliveTimeout: s.KeepaliveTimeout, MaxHandshakeAttempts: s.MaxHandshakeAttempts,
		RandomTrailers: s.RandomTrailers, DisableCookies: s.DisableCookies,
	}
	raw, err := json.Marshal(server)
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(map[string]any{"server": json.RawMessage(raw), "clients": clients})
	return string(out), err
}

func endpointHost(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(endpoint); err == nil {
		return host
	}
	return strings.Trim(endpoint, "[]")
}

func (m *axMigration) freeTag(tag string) string {
	pick := tag
	for n := 2; ; n++ {
		var count int64
		m.tx.Model(&model.Inbound{}).Where("tag = ?", pick).Count(&count)
		if count == 0 {
			return pick
		}
		pick = fmt.Sprintf("%s-%d", tag, n)
	}
}

func (m *axMigration) migrateMtproto(ib model.Inbound) error {
	var rows []axMtprotoClient
	if err := m.tx.Table("mtproto_clients").Where("inbound_id = ?", ib.Id).Order("id").Find(&rows).Error; err != nil {
		return err
	}
	settings := map[string]any{}
	if strings.TrimSpace(ib.Settings) != "" {
		if err := json.Unmarshal([]byte(ib.Settings), &settings); err != nil {
			log.Printf("3AX-UI 1.x migration: MTProto inbound %d has unreadable settings, left as is: %v", ib.Id, err)
			return nil
		}
	}
	delete(settings, "secret")
	clients := make([]model.Client, 0, len(rows))
	if err := m.tx.Where("inbound_id = ?", ib.Id).Delete(&xray.ClientTraffic{}).Error; err != nil {
		return err
	}
	for _, r := range rows {
		subID := r.SubId
		if subID == "" {
			subID = random.NumLower(16)
		}
		c := model.Client{
			Email: m.claimEmail(r.Email, "mtproto"), Enable: r.Enable, Secret: r.Secret, TotalGB: r.TotalGB,
			ExpiryTime: r.ExpiryTime, Reset: r.Reset, LimitIP: r.LimitIp, TgID: r.TgId, SubID: subID,
			Comment: r.Comment, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}
		clients = append(clients, c)
		if err := m.tx.Create(&xray.ClientTraffic{
			InboundId: ib.Id, Email: c.Email, Enable: r.Enable, Up: r.Upload, Down: r.Download,
			Total: r.TotalGB, ExpiryTime: r.ExpiryTime, Reset: r.Reset, LastOnline: r.LastOnline,
		}).Error; err != nil {
			return err
		}
	}
	settings["clients"] = clients
	out, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	log.Printf("3AX-UI 1.x migration: MTProto inbound %d (%s) carries %d clients", ib.Id, ib.Tag, len(clients))
	return m.tx.Model(&model.Inbound{}).Where("id = ?", ib.Id).Update("settings", string(out)).Error
}

// renameRoutingTags points routing rules that matched 1.x's TPROXY inbound at
// the tunnel inbound's own tag, which tags its traffic in this base.
func (m *axMigration) renameRoutingTags() error {
	if len(m.tagRenames) == 0 {
		return nil
	}
	var setting model.Setting
	if err := m.tx.Where("key = ?", "xrayTemplateConfig").First(&setting).Error; err != nil {
		return nil
	}
	var cfg map[string]any
	if json.Unmarshal([]byte(setting.Value), &cfg) != nil {
		return nil
	}
	routing, _ := cfg["routing"].(map[string]any)
	rules, _ := routing["rules"].([]any)
	changed := false
	for _, r := range rules {
		rule, _ := r.(map[string]any)
		tags, _ := rule["inboundTag"].([]any)
		for i, t := range tags {
			if s, ok := t.(string); ok {
				if to, hit := m.tagRenames[s]; hit {
					tags[i] = to
					changed = true
				}
			}
		}
	}
	if !changed {
		return nil
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return m.tx.Model(&model.Setting{}).Where("id = ?", setting.Id).Update("value", string(out)).Error
}
