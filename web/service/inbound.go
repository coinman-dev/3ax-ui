// Package service provides business logic services for the 3AX-UI web panel,
// including inbound/outbound management, user administration, settings, and Xray integration.
package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/logger"
	"github.com/coinman-dev/3ax-ui/v2/mtproto"
	"github.com/coinman-dev/3ax-ui/v2/util/common"
	"github.com/coinman-dev/3ax-ui/v2/xray"
	"github.com/google/uuid"

	"gorm.io/gorm"
)

// InboundService provides business logic for managing Xray inbound configurations.
// It handles CRUD operations for inbounds, client management, traffic monitoring,
// and integration with the Xray API for real-time updates.
type InboundService struct {
	xrayApi xray.XrayAPI
}

type CopyClientsResult struct {
	Added   []string `json:"added"`
	Skipped []string `json:"skipped"`
	Errors  []string `json:"errors"`
}

// GetInbounds retrieves all inbounds for a specific user.
// Returns a slice of inbound models with their associated client statistics.
func (s *InboundService) GetInbounds(userId int) ([]*model.Inbound, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).Preload("ClientStats").Where("user_id = ?", userId).Find(&inbounds).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	// Enrich client stats with UUID/SubId from inbound settings
	for _, inbound := range inbounds {
		clients, _ := s.GetClients(inbound)
		if len(clients) == 0 || len(inbound.ClientStats) == 0 {
			continue
		}
		// Build a map email -> client
		cMap := make(map[string]model.Client, len(clients))
		for _, c := range clients {
			cMap[strings.ToLower(c.Email)] = c
		}
		for i := range inbound.ClientStats {
			email := strings.ToLower(inbound.ClientStats[i].Email)
			if c, ok := cMap[email]; ok {
				inbound.ClientStats[i].UUID = c.ID
				inbound.ClientStats[i].SubId = c.SubID
			}
		}
	}
	return inbounds, nil
}

// GetAllInbounds retrieves all inbounds from the database.
// Returns a slice of all inbound models with their associated client statistics.
func (s *InboundService) GetAllInbounds() ([]*model.Inbound, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).Preload("ClientStats").Find(&inbounds).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	// Enrich client stats with UUID/SubId from inbound settings
	for _, inbound := range inbounds {
		clients, _ := s.GetClients(inbound)
		if len(clients) == 0 || len(inbound.ClientStats) == 0 {
			continue
		}
		cMap := make(map[string]model.Client, len(clients))
		for _, c := range clients {
			cMap[strings.ToLower(c.Email)] = c
		}
		for i := range inbound.ClientStats {
			email := strings.ToLower(inbound.ClientStats[i].Email)
			if c, ok := cMap[email]; ok {
				inbound.ClientStats[i].UUID = c.ID
				inbound.ClientStats[i].SubId = c.SubID
			}
		}
	}
	return inbounds, nil
}

func (s *InboundService) GetInboundsByTrafficReset(period string) ([]*model.Inbound, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).Where("traffic_reset = ?", period).Find(&inbounds).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	return inbounds, nil
}

func (s *InboundService) checkPortExist(listen string, port int, ignoreId int) (bool, error) {
	db := database.GetDB()
	if listen == "" || listen == "0.0.0.0" || listen == "::" || listen == "::0" {
		db = db.Model(model.Inbound{}).Where("port = ?", port)
	} else {
		db = db.Model(model.Inbound{}).
			Where("port = ?", port).
			Where(
				db.Model(model.Inbound{}).Where(
					"listen = ?", listen,
				).Or(
					"listen = \"\"",
				).Or(
					"listen = \"0.0.0.0\"",
				).Or(
					"listen = \"::\"",
				).Or(
					"listen = \"::0\""))
	}
	if ignoreId > 0 {
		db = db.Where("id != ?", ignoreId)
	}
	var count int64
	err := db.Count(&count).Error
	if err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}

	// Loopback egress ports of routed mtproto inbounds live inside settings
	// JSON, so the query above cannot see them. Xray binds them for real, so a
	// new inbound must not claim one.
	if _, taken := s.mtprotoReservedPorts(ignoreId)[port]; taken {
		return true, nil
	}
	return false, nil
}

// clientsArray reads settings["clients"] defensively. Inbound settings are
// free-form JSON from the database: written by older panel versions, imported
// from other panels or hand-edited, so the key may be missing or hold another
// shape. A bare type assertion here used to panic — a 500 from an HTTP handler,
// and a dead panel when reached from a cron job.
func clientsArray(settings map[string]any) []any {
	clients, _ := settings["clients"].([]any)
	return clients
}

func (s *InboundService) GetClients(inbound *model.Inbound) ([]model.Client, error) {
	settings := map[string][]model.Client{}
	json.Unmarshal([]byte(inbound.Settings), &settings)
	if settings == nil {
		return nil, fmt.Errorf("setting is null")
	}

	clients := settings["clients"]
	if clients == nil {
		return nil, nil
	}
	return clients, nil
}

func (s *InboundService) getAllEmails() ([]string, error) {
	db := database.GetDB()
	var emails []string
	err := db.Raw(`
		SELECT JSON_EXTRACT(client.value, '$.email')
		FROM inbounds,
			JSON_EACH(JSON_EXTRACT(inbounds.settings, '$.clients')) AS client
		`).Scan(&emails).Error
	if err != nil {
		return nil, err
	}
	return emails, nil
}

func (s *InboundService) contains(slice []string, str string) bool {
	lowerStr := strings.ToLower(str)
	for _, s := range slice {
		if strings.ToLower(s) == lowerStr {
			return true
		}
	}
	return false
}

func (s *InboundService) checkEmailsExistForClients(clients []model.Client) (string, error) {
	allEmails, err := s.getAllEmails()
	if err != nil {
		return "", err
	}
	var emails []string
	for _, client := range clients {
		if client.Email != "" {
			if s.contains(emails, client.Email) {
				return client.Email, nil
			}
			if s.contains(allEmails, client.Email) {
				return client.Email, nil
			}
			emails = append(emails, client.Email)
		}
	}
	return "", nil
}

func (s *InboundService) checkEmailExistForInbound(inbound *model.Inbound) (string, error) {
	clients, err := s.GetClients(inbound)
	if err != nil {
		return "", err
	}
	allEmails, err := s.getAllEmails()
	if err != nil {
		return "", err
	}
	var emails []string
	for _, client := range clients {
		if client.Email != "" {
			if s.contains(emails, client.Email) {
				return client.Email, nil
			}
			if s.contains(allEmails, client.Email) {
				return client.Email, nil
			}
			emails = append(emails, client.Email)
		}
	}
	return "", nil
}

// AddInbound creates a new inbound configuration.
// It validates port uniqueness, client email uniqueness, and required fields,
// then saves the inbound to the database and optionally adds it to the running Xray instance.
// Returns the created inbound, whether Xray needs restart, and any error.
func (s *InboundService) AddInbound(inbound *model.Inbound) (*model.Inbound, bool, error) {
	// AmneziaWG inbounds are managed separately — just save the DB record
	if inbound.Protocol == model.AmneziaWG {
		return s.addAmneziawgInbound(inbound)
	}

	// NativeWG inbounds are managed separately — just save the DB record
	if inbound.Protocol == model.NativeWG {
		return s.addNativeWgInbound(inbound)
	}

	// MTProto inbounds run as standalone mtg sidecars — save the DB record and
	// let the reconcile job start mtg. They never enter the Xray config.
	if inbound.Protocol == model.MTProto {
		return s.addMtprotoInbound(inbound)
	}

	exist, err := s.checkPortExist(inbound.Listen, inbound.Port, 0)
	if err != nil {
		return inbound, false, err
	}
	if exist {
		return inbound, false, common.NewError("Port already exists:", inbound.Port)
	}

	existEmail, err := s.checkEmailExistForInbound(inbound)
	if err != nil {
		return inbound, false, err
	}
	if existEmail != "" {
		return inbound, false, common.NewError("Duplicate email:", existEmail)
	}

	clients, err := s.GetClients(inbound)
	if err != nil {
		return inbound, false, err
	}

	// Ensure created_at and updated_at on clients in settings
	if len(clients) > 0 {
		var settings map[string]any
		if err2 := json.Unmarshal([]byte(inbound.Settings), &settings); err2 == nil && settings != nil {
			now := time.Now().Unix() * 1000
			updatedClients := make([]model.Client, 0, len(clients))
			for _, c := range clients {
				if c.CreatedAt == 0 {
					c.CreatedAt = now
				}
				c.UpdatedAt = now
				updatedClients = append(updatedClients, c)
			}
			settings["clients"] = updatedClients
			if bs, err3 := json.MarshalIndent(settings, "", "  "); err3 == nil {
				inbound.Settings = string(bs)
			} else {
				logger.Debug("Unable to marshal inbound settings with timestamps:", err3)
			}
		} else if err2 != nil {
			logger.Debug("Unable to parse inbound settings for timestamps:", err2)
		}
	}

	// Secure client ID
	for _, client := range clients {
		switch inbound.Protocol {
		case "trojan":
			if client.Password == "" {
				return inbound, false, common.NewError("empty client ID")
			}
		case "shadowsocks", "mixed", "http":
			// SS uses email; mixed/http use the basic-auth username (stored as Email).
			if client.Email == "" {
				return inbound, false, common.NewError("empty client ID")
			}
		case "hysteria", "hysteria2":
			if client.Auth == "" {
				return inbound, false, common.NewError("empty client ID")
			}
		default:
			if client.ID == "" {
				return inbound, false, common.NewError("empty client ID")
			}
		}
	}

	db := database.GetDB()
	tx := db.Begin()
	defer func() {
		if err == nil {
			tx.Commit()
		} else {
			tx.Rollback()
		}
	}()

	err = tx.Save(inbound).Error
	if err == nil {
		if len(inbound.ClientStats) == 0 {
			for _, client := range clients {
				s.AddClientStat(tx, inbound.Id, &client)
			}
		}
	} else {
		return inbound, false, err
	}

	needRestart := false
	if inbound.Enable {
		s.xrayApi.Init(xrayAPIPort())
		inboundJson, err1 := json.MarshalIndent(inbound.GenXrayInboundConfig(), "", "  ")
		if err1 != nil {
			logger.Debug("Unable to marshal inbound config:", err1)
		}

		err1 = s.xrayApi.AddInbound(inboundJson)
		if err1 == nil {
			logger.Debug("New inbound added by api:", inbound.Tag)
		} else {
			logger.Debug("Unable to add inbound by api:", err1)
			needRestart = true
		}
		s.xrayApi.Close()
	}

	return inbound, needRestart, err
}

// addNativeWgInbound creates a minimal inbound record for WireGuard Native.
// No xray config is generated — WG is managed separately.
func (s *InboundService) addNativeWgInbound(inbound *model.Inbound) (*model.Inbound, bool, error) {
	db := database.GetDB()

	var count int64
	db.Model(&model.Inbound{}).Where("protocol = ?", model.NativeWG).Count(&count)
	if count > 0 {
		return inbound, false, common.NewError("WireGuard Native inbound already exists. Only one is allowed.")
	}

	// Native WG peers are managed outside inbound.settings.
	inbound.Settings = `{"clients":[]}`
	if inbound.Tag == "" {
		inbound.Tag = "inbound-nativewg"
	}

	var err error
	tx := db.Begin()
	defer func() {
		if err == nil {
			tx.Commit()
		} else {
			tx.Rollback()
		}
	}()

	err = tx.Save(inbound).Error
	if err != nil {
		return inbound, false, err
	}

	return inbound, false, nil
}

// addAmneziawgInbound creates a minimal inbound record for AmneziaWG.
// No xray config is generated — AWG is managed separately.
func (s *InboundService) addAmneziawgInbound(inbound *model.Inbound) (*model.Inbound, bool, error) {
	// Check if amneziawg inbound already exists (only one allowed)
	db := database.GetDB()
	var count int64
	db.Model(&model.Inbound{}).Where("protocol = ?", model.AmneziaWG).Count(&count)
	if count > 0 {
		return inbound, false, common.NewError("AmneziaWG inbound already exists. Only one is allowed.")
	}

	// AWG peers are managed outside inbound.settings.
	inbound.Settings = `{"clients":[]}`
	if inbound.Tag == "" {
		inbound.Tag = "inbound-amneziawg"
	}

	var err error
	tx := db.Begin()
	defer func() {
		if err == nil {
			tx.Commit()
		} else {
			tx.Rollback()
		}
	}()

	err = tx.Save(inbound).Error
	if err != nil {
		return inbound, false, err
	}

	return inbound, false, nil
}

// mtprotoRoutesThroughXray reports whether an mtproto inbound is configured to
// egress through the core's router (the loopback SOCKS bridge in xray.go).
func mtprotoRoutesThroughXray(inbound *model.Inbound) bool {
	if inbound == nil || inbound.Protocol != model.MTProto {
		return false
	}
	var parsed struct {
		RouteThroughXray bool `json:"routeThroughXray"`
	}
	if err := json.Unmarshal([]byte(inbound.Settings), &parsed); err != nil {
		return false
	}
	return parsed.RouteThroughXray
}

func mtprotoSettingsXrayPort(parsed map[string]any) int {
	switch v := parsed["routeXrayPort"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	}
	return 0
}

// mtprotoReservedPorts collects every port the panel has already committed,
// excluding the inbound being edited: listen ports of the other inbounds, the
// loopback egress ports of the other routed mtproto inbounds, and the panel's
// own web/subscription ports. The egress port is chosen by the panel and
// validated by nothing else, so without this the allocator could hand out a
// port something else already binds — xray would then fail to start the bridge.
func (s *InboundService) mtprotoReservedPorts(excludeInboundId int) map[int]struct{} {
	reserved := make(map[int]struct{})
	db := database.GetDB()

	// Listen ports of every other inbound.
	var portRows []struct {
		Id   int
		Port int
	}
	if err := db.Model(model.Inbound{}).Select("id, port").Scan(&portRows).Error; err != nil {
		logger.Warning("mtproto: could not read inbound ports for collision check:", err)
	}
	for _, r := range portRows {
		if r.Id != excludeInboundId && r.Port > 0 {
			reserved[r.Port] = struct{}{}
		}
	}

	// Egress ports of the other routed mtproto inbounds (they live in settings
	// JSON, so no query can filter on them directly).
	var mtRows []struct {
		Id       int
		Settings string
	}
	if err := db.Model(model.Inbound{}).Select("id, settings").
		Where("protocol = ?", model.MTProto).Scan(&mtRows).Error; err != nil {
		logger.Warning("mtproto: could not read mtproto settings for collision check:", err)
	}
	for _, r := range mtRows {
		if r.Id == excludeInboundId {
			continue
		}
		if p := mtprotoParseXrayPort(r.Settings); p > 0 {
			reserved[p] = struct{}{}
		}
	}

	settingService := SettingService{}
	if p, err := settingService.GetPort(); err == nil && p > 0 {
		reserved[p] = struct{}{}
	}
	if p, err := settingService.GetSubPort(); err == nil && p > 0 {
		reserved[p] = struct{}{}
	}
	return reserved
}

// allocateMtprotoXrayPort asks the OS for a free loopback port and retries
// until it lands on one that is not already reserved by the panel.
func (s *InboundService) allocateMtprotoXrayPort(reserved map[int]struct{}) (int, error) {
	for range 20 {
		port, err := mtproto.FreeLocalPort()
		if err != nil {
			return 0, err
		}
		if _, taken := reserved[port]; !taken {
			return port, nil
		}
	}
	return 0, common.NewError("mtproto: could not find a free Xray egress port")
}

func mtprotoParseXrayPort(settings string) int {
	if settings == "" {
		return 0
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(settings), &parsed); err != nil {
		return 0
	}
	return mtprotoSettingsXrayPort(parsed)
}

// normalizeMtprotoXrayPort guarantees a routed mtproto inbound carries a stable
// loopback egress port in its settings, so the generated Xray SOCKS bridge and
// the mtg sidecar agree on where mtg dials out. The port is backend-owned: it is
// allocated once when routing is first enabled and preserved across edits
// (carried over from oldSettings, which wins over any value the client echoed
// back). When routing is off it — together with the now-inert outbound selection
// — is stripped so a disabled bridge leaves nothing stale behind.
func (s *InboundService) normalizeMtprotoXrayPort(inbound *model.Inbound, oldSettings string) error {
	if inbound.Protocol != model.MTProto {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(inbound.Settings), &parsed); err != nil || parsed == nil {
		return nil
	}
	routed, _ := parsed["routeThroughXray"].(bool)
	if !routed {
		_, hadPort := parsed["routeXrayPort"]
		if !hadPort {
			return nil
		}
		delete(parsed, "routeXrayPort")
		if bs, err := json.MarshalIndent(parsed, "", "  "); err == nil {
			inbound.Settings = string(bs)
		} else {
			logger.Warning("mtproto: failed to marshal settings after disabling routing:", err)
		}
		return nil
	}

	// Prefer the already-stored port (carried across edits), then any value the
	// client sent, then allocate a fresh one. Whatever the source, the port has
	// to survive a collision check against every other port the panel owns —
	// including the case where the stored port was later taken by a new inbound.
	reserved := s.mtprotoReservedPorts(inbound.Id)
	port := mtprotoParseXrayPort(oldSettings)
	if port <= 0 {
		port = mtprotoSettingsXrayPort(parsed)
	}
	if port > 0 && port <= 65535 {
		if _, taken := reserved[port]; taken {
			logger.Warningf("mtproto: Xray egress port %d is already in use by another inbound or by the panel, allocating a new one", port)
			port = 0
		}
	} else {
		port = 0
	}
	if port <= 0 {
		allocated, err := s.allocateMtprotoXrayPort(reserved)
		if err != nil {
			return common.NewError("mtproto: could not allocate an Xray egress port:", err)
		}
		port = allocated
	}
	if mtprotoSettingsXrayPort(parsed) == port {
		return nil
	}
	parsed["routeXrayPort"] = port
	bs, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return common.NewError("mtproto: could not persist the Xray egress port:", err)
	}
	inbound.Settings = string(bs)
	return nil
}

// addMtprotoInbound creates an inbound record for an MTProto (mtg) proxy.
// No xray config is generated for the proxy itself — mtg runs as a standalone
// sidecar managed by the mtproto package + reconcile job. Unlike AWG/WG, MTProto
// is multi-instance, so there is no single-inbound restriction; the public port
// must be unique across all inbounds (the mtg process binds it directly). When
// the inbound opts into routeThroughXray, a loopback SOCKS bridge is injected
// into the Xray config (xray.go) — hence the needRestart return.
func (s *InboundService) addMtprotoInbound(inbound *model.Inbound) (*model.Inbound, bool, error) {
	exist, err := s.checkPortExist(inbound.Listen, inbound.Port, 0)
	if err != nil {
		return inbound, false, err
	}
	if exist {
		return inbound, false, common.NewError("Port already exists:", inbound.Port)
	}

	if inbound.Tag == "" {
		inbound.Tag = fmt.Sprintf("inbound-%v", inbound.Port)
	}
	// The inbound's settings hold only inbound-level config; clients live in the
	// dedicated mtproto_clients table. Pull the optional first client off the
	// create form, then strip the client list from settings.
	firstClient := firstMtprotoFormClient(inbound.Settings)
	inbound.Settings = stripMtprotoClients(inbound.Settings)

	// Allocate the loopback egress port when routing through Xray is enabled.
	if err := s.normalizeMtprotoXrayPort(inbound, ""); err != nil {
		return inbound, false, err
	}

	if err := database.GetDB().Save(inbound).Error; err != nil {
		return inbound, false, err
	}
	// Seed the first client row (the create form may carry one). Email may be any
	// string — duplicates are allowed; the unique identity is the generated Uuid.
	if firstClient != nil {
		firstClient.InboundId = inbound.Id
		if err := (&MtprotoClientService{}).AddClient(firstClient); err != nil {
			return inbound, false, err
		}
	}
	// The reconcile job (every 10s) starts the proxy; AddClient already reconciled
	// it immediately. A restart is only needed to add the egress SOCKS bridge.
	return inbound, mtprotoRoutesThroughXray(inbound), nil
}

// firstMtprotoFormClient extracts the optional first client from an mtproto
// inbound-create form (settings.clients[0]) as a MtprotoClient row seed, or nil.
// It parses tolerantly because the browser stores some numeric fields (tgId) as
// strings.
func firstMtprotoFormClient(settings string) *model.MtprotoClient {
	_, _, seeds := model.ParseMtprotoSettingsClients(settings)
	if len(seeds) == 0 {
		return nil
	}
	c := seeds[0]
	if strings.TrimSpace(c.Email) == "" {
		return nil
	}
	// A freshly-created client defaults to enabled when the form omits the flag.
	enable := c.Enable || !c.HasEnable
	return &model.MtprotoClient{
		Email: c.Email, Enable: enable, LimitIp: c.LimitIp, TotalGB: c.TotalGB,
		ExpiryTime: c.ExpiryTime, TgId: c.TgId, SubId: c.SubId, Comment: c.Comment, Reset: c.Reset,
	}
}

// stripMtprotoClients removes the client list / legacy secret from an mtproto
// inbound's settings JSON, keeping only inbound-level configuration.
func stripMtprotoClients(settings string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(settings), &m); err != nil {
		return settings
	}
	delete(m, "clients")
	delete(m, "secret")
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return settings
	}
	return string(out)
}

// updateMtprotoInbound updates an existing MTProto inbound's DB record. The
// reconcile job notices the changed fingerprint and restarts mtg accordingly;
// nothing touches the Xray config.
func (s *InboundService) updateMtprotoInbound(inbound *model.Inbound) (*model.Inbound, bool, error) {
	exist, err := s.checkPortExist(inbound.Listen, inbound.Port, inbound.Id)
	if err != nil {
		return inbound, false, err
	}
	if exist {
		return inbound, false, common.NewError("Port already exists:", inbound.Port)
	}

	oldInbound, err := s.GetInbound(inbound.Id)
	if err != nil {
		return inbound, false, err
	}
	wasRouted := mtprotoRoutesThroughXray(oldInbound)

	// Only inbound-level config is edited here; clients live in their own table.
	oldDomain := model.MtprotoFakeTLSDomain(oldInbound.Settings)
	newDomain := model.MtprotoFakeTLSDomain(inbound.Settings)
	inbound.Settings = stripMtprotoClients(inbound.Settings)

	// Allocate/strip the loopback egress port to match the new routing state,
	// carrying the existing port across edits.
	if err := s.normalizeMtprotoXrayPort(inbound, oldInbound.Settings); err != nil {
		return inbound, false, err
	}

	// Carry over editable fields; traffic counters live on the client rows.
	oldInbound.Up = inbound.Up
	oldInbound.Down = inbound.Down
	oldInbound.Total = inbound.Total
	oldInbound.Remark = inbound.Remark
	oldInbound.Enable = inbound.Enable
	oldInbound.ExpiryTime = inbound.ExpiryTime
	oldInbound.TrafficReset = inbound.TrafficReset
	oldInbound.Listen = inbound.Listen
	oldInbound.Port = inbound.Port
	oldInbound.Settings = inbound.Settings
	// Regenerate the tag from listen/port like the generic update path does, so
	// it tracks the (possibly changed) port and a stale tag can't later collide
	// with a new inbound created on the old port (Tag is unique).
	if inbound.Listen == "" || inbound.Listen == "0.0.0.0" || inbound.Listen == "::" || inbound.Listen == "::0" {
		oldInbound.Tag = fmt.Sprintf("inbound-%v", inbound.Port)
	} else {
		oldInbound.Tag = fmt.Sprintf("inbound-%v:%v", inbound.Listen, inbound.Port)
	}

	if err = database.GetDB().Save(oldInbound).Error; err != nil {
		return inbound, false, err
	}
	// If the fronting domain changed, rebuild every client's FakeTLS secret so
	// the per-client links track it; then reconcile the sidecar immediately.
	svc := &MtprotoClientService{}
	if newDomain != oldDomain {
		svc.RehealInbound(inbound.Id, newDomain)
	}
	svc.Reconcile(inbound.Id)
	// A Xray restart is needed only when the egress SOCKS bridge must be added,
	// moved, or dropped — i.e. the inbound is (or was) routed.
	return inbound, mtprotoRoutesThroughXray(inbound) || wasRouted, nil
}

// DelInbound deletes an inbound configuration by ID.
// It removes the inbound from the database and the running Xray instance if active.
// Returns whether Xray needs restart and any error.
func (s *InboundService) DelInbound(id int) (bool, error) {
	db := database.GetDB()

	var tag string
	needRestart := false
	result := db.Model(model.Inbound{}).Select("tag").Where("id = ? and enable = ?", id, true).First(&tag)
	if result.Error == nil {
		s.xrayApi.Init(xrayAPIPort())
		err1 := s.xrayApi.DelInbound(tag)
		if err1 == nil {
			logger.Debug("Inbound deleted by api:", tag)
		} else {
			logger.Debug("Unable to delete inbound by api:", err1)
			needRestart = true
		}
		s.xrayApi.Close()
	} else {
		logger.Debug("No enabled inbound founded to removing by api", tag)
	}

	// Delete client traffics of inbounds
	err := db.Where("inbound_id = ?", id).Delete(xray.ClientTraffic{}).Error
	if err != nil {
		return false, err
	}
	inbound, err := s.GetInbound(id)
	if err != nil {
		return false, err
	}
	clients, err := s.GetClients(inbound)
	if err != nil {
		return false, err
	}
	// Bulk-delete client IPs for every email in this inbound. The previous
	// per-client loop fired one DELETE per row — at 7k+ clients that meant
	// thousands of synchronous SQL roundtrips and a multi-second freeze.
	// Chunked to stay under SQLite's bind-variable limit on huge inbounds.
	if len(clients) > 0 {
		emails := make([]string, 0, len(clients))
		for i := range clients {
			if clients[i].Email != "" {
				emails = append(emails, clients[i].Email)
			}
		}
		for _, batch := range chunkStrings(uniqueNonEmptyStrings(emails), sqliteMaxVars) {
			if err := db.Where("client_email IN ?", batch).
				Delete(model.InboundClientIps{}).Error; err != nil {
				return false, err
			}
		}
	}

	// Clean up AmneziaWG clients and server when deleting an AWG inbound
	if inbound.Protocol == model.AmneziaWG {
		awgService := AwgService{}
		if err := awgService.DeleteAllClients(); err != nil {
			logger.Warning("Failed to clean up AWG data:", err)
		}
	}

	// Clean up WireGuard Native clients and server when deleting a WG inbound
	if inbound.Protocol == model.NativeWG {
		wgService := WgService{}
		if err := wgService.DeleteAllClients(); err != nil {
			logger.Warning("Failed to clean up WG data:", err)
		}
	}

	// Stop the mtg sidecar immediately so the public port is freed at once
	// (the reconcile job would otherwise take up to its interval to notice).
	// A routed inbound also leaves an egress SOCKS bridge in the Xray config,
	// so force a restart to drop it.
	if inbound.Protocol == model.MTProto {
		mtproto.GetManager().Remove(inbound.Id)
		// Clients live in their own table — remove this inbound's rows too.
		if err := (&MtprotoClientService{}).DeleteByInbound(inbound.Id); err != nil {
			logger.Warning("mtproto: delete clients for inbound", inbound.Id, "failed:", err)
		}
		if mtprotoRoutesThroughXray(inbound) {
			needRestart = true
		}
	}

	return needRestart, db.Delete(model.Inbound{}, id).Error
}

func (s *InboundService) GetInbound(id int) (*model.Inbound, error) {
	db := database.GetDB()
	inbound := &model.Inbound{}
	err := db.Model(model.Inbound{}).First(inbound, id).Error
	if err != nil {
		return nil, err
	}
	return inbound, nil
}

// SetInboundEnable toggles only the enable flag of an inbound, without
// rewriting the (potentially multi-MB) settings JSON. Used by the UI's
// per-row enable switch — for inbounds with thousands of clients the full
// UpdateInbound path is an order of magnitude too slow for an interactive
// toggle (parses + reserialises every client, runs O(N) traffic diff).
//
// Returns (needRestart, error). needRestart is true when the xray runtime
// could not be re-synced from the cached config and a full restart is
// required to pick up the change.
func (s *InboundService) SetInboundEnable(id int, enable bool) (bool, error) {
	inbound, err := s.GetInbound(id)
	if err != nil {
		return false, err
	}
	if inbound.Enable == enable {
		return false, nil
	}

	db := database.GetDB()
	if err := db.Model(model.Inbound{}).Where("id = ?", id).
		Update("enable", enable).Error; err != nil {
		return false, err
	}
	inbound.Enable = enable

	// Sync xray runtime: drop the live inbound, add it back if we're enabling.
	// "User not found"-style errors from DelInbound mean the inbound was
	// already absent from the live config — that's fine. Any other error
	// means the live config and DB diverged, so we ask the caller to
	// schedule a restart.
	needRestart := false
	s.xrayApi.Init(xrayAPIPort())
	defer s.xrayApi.Close()

	if err := s.xrayApi.DelInbound(inbound.Tag); err != nil &&
		!strings.Contains(err.Error(), "not found") {
		logger.Debug("SetInboundEnable: DelInbound via api failed:", err)
		needRestart = true
	}
	if !enable {
		return needRestart, nil
	}

	runtimeInbound, err := s.buildRuntimeInboundForAPI(db, inbound)
	if err != nil {
		logger.Debug("SetInboundEnable: build runtime config failed:", err)
		return true, nil
	}
	inboundJson, err := json.MarshalIndent(runtimeInbound.GenXrayInboundConfig(), "", "  ")
	if err != nil {
		logger.Debug("SetInboundEnable: marshal runtime config failed:", err)
		return true, nil
	}
	if err := s.xrayApi.AddInbound(inboundJson); err != nil {
		logger.Debug("SetInboundEnable: AddInbound via api failed:", err)
		needRestart = true
	}
	return needRestart, nil
}

// UpdateInbound modifies an existing inbound configuration.
// It validates changes, updates the database, and syncs with the running Xray instance.
// Returns the updated inbound, whether Xray needs restart, and any error.
func (s *InboundService) UpdateInbound(inbound *model.Inbound) (*model.Inbound, bool, error) {
	// MTProto inbounds run as standalone mtg sidecars — update the DB record and
	// let the reconcile job restart mtg. They never enter the Xray config.
	if inbound.Protocol == model.MTProto {
		return s.updateMtprotoInbound(inbound)
	}

	exist, err := s.checkPortExist(inbound.Listen, inbound.Port, inbound.Id)
	if err != nil {
		return inbound, false, err
	}
	if exist {
		return inbound, false, common.NewError("Port already exists:", inbound.Port)
	}

	oldInbound, err := s.GetInbound(inbound.Id)
	if err != nil {
		return inbound, false, err
	}

	tag := oldInbound.Tag

	db := database.GetDB()
	tx := db.Begin()

	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()

	err = s.updateClientTraffics(tx, oldInbound, inbound)
	if err != nil {
		return inbound, false, err
	}

	// Ensure created_at and updated_at exist in inbound.Settings clients
	{
		var oldSettings map[string]any
		_ = json.Unmarshal([]byte(oldInbound.Settings), &oldSettings)
		emailToCreated := map[string]int64{}
		emailToUpdated := map[string]int64{}
		if oldSettings != nil {
			if oc, ok := oldSettings["clients"].([]any); ok {
				for _, it := range oc {
					if m, ok2 := it.(map[string]any); ok2 {
						if email, ok3 := m["email"].(string); ok3 {
							switch v := m["created_at"].(type) {
							case float64:
								emailToCreated[email] = int64(v)
							case int64:
								emailToCreated[email] = v
							}
							switch v := m["updated_at"].(type) {
							case float64:
								emailToUpdated[email] = int64(v)
							case int64:
								emailToUpdated[email] = v
							}
						}
					}
				}
			}
		}
		var newSettings map[string]any
		if err2 := json.Unmarshal([]byte(inbound.Settings), &newSettings); err2 == nil && newSettings != nil {
			now := time.Now().Unix() * 1000
			if nSlice, ok := newSettings["clients"].([]any); ok {
				for i := range nSlice {
					if m, ok2 := nSlice[i].(map[string]any); ok2 {
						email, _ := m["email"].(string)
						if _, ok3 := m["created_at"]; !ok3 {
							if v, ok4 := emailToCreated[email]; ok4 && v > 0 {
								m["created_at"] = v
							} else {
								m["created_at"] = now
							}
						}
						// Preserve client's updated_at if present; do not bump on parent inbound update
						if _, hasUpdated := m["updated_at"]; !hasUpdated {
							if v, ok4 := emailToUpdated[email]; ok4 && v > 0 {
								m["updated_at"] = v
							}
						}
						nSlice[i] = m
					}
				}
				newSettings["clients"] = nSlice
				if bs, err3 := json.MarshalIndent(newSettings, "", "  "); err3 == nil {
					inbound.Settings = string(bs)
				}
			}
		}
	}

	oldInbound.Up = inbound.Up
	oldInbound.Down = inbound.Down
	oldInbound.Total = inbound.Total
	oldInbound.Remark = inbound.Remark
	oldInbound.Enable = inbound.Enable
	oldInbound.ExpiryTime = inbound.ExpiryTime
	oldInbound.TrafficReset = inbound.TrafficReset
	oldInbound.Listen = inbound.Listen
	oldInbound.Port = inbound.Port
	oldInbound.Protocol = inbound.Protocol
	oldInbound.Settings = inbound.Settings
	oldInbound.StreamSettings = inbound.StreamSettings
	oldInbound.Sniffing = inbound.Sniffing
	if inbound.Listen == "" || inbound.Listen == "0.0.0.0" || inbound.Listen == "::" || inbound.Listen == "::0" {
		oldInbound.Tag = fmt.Sprintf("inbound-%v", inbound.Port)
	} else {
		oldInbound.Tag = fmt.Sprintf("inbound-%v:%v", inbound.Listen, inbound.Port)
	}

	needRestart := false
	s.xrayApi.Init(xrayAPIPort())
	if s.xrayApi.DelInbound(tag) == nil {
		logger.Debug("Old inbound deleted by api:", tag)
	}
	if inbound.Enable {
		runtimeInbound, err2 := s.buildRuntimeInboundForAPI(tx, oldInbound)
		if err2 != nil {
			logger.Debug("Unable to prepare runtime inbound config:", err2)
			needRestart = true
		} else {
			inboundJson, err2 := json.MarshalIndent(runtimeInbound.GenXrayInboundConfig(), "", "  ")
			if err2 != nil {
				logger.Debug("Unable to marshal updated inbound config:", err2)
				needRestart = true
			} else {
				err2 = s.xrayApi.AddInbound(inboundJson)
				if err2 == nil {
					logger.Debug("Updated inbound added by api:", oldInbound.Tag)
				} else {
					logger.Debug("Unable to update inbound by api:", err2)
					needRestart = true
				}
			}
		}
	}
	s.xrayApi.Close()

	return inbound, needRestart, tx.Save(oldInbound).Error
}

func (s *InboundService) buildRuntimeInboundForAPI(tx *gorm.DB, inbound *model.Inbound) (*model.Inbound, error) {
	if inbound == nil {
		return nil, fmt.Errorf("inbound is nil")
	}

	runtimeInbound := *inbound
	settings := map[string]any{}
	if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
		return nil, err
	}

	clients, ok := settings["clients"].([]any)
	if !ok {
		return &runtimeInbound, nil
	}

	var clientStats []xray.ClientTraffic
	err := tx.Model(xray.ClientTraffic{}).
		Where("inbound_id = ?", inbound.Id).
		Select("email", "enable").
		Find(&clientStats).Error
	if err != nil {
		return nil, err
	}

	enableMap := make(map[string]bool, len(clientStats))
	for _, clientTraffic := range clientStats {
		enableMap[clientTraffic.Email] = clientTraffic.Enable
	}

	finalClients := make([]any, 0, len(clients))
	for _, client := range clients {
		c, ok := client.(map[string]any)
		if !ok {
			continue
		}

		email, _ := c["email"].(string)
		if enable, exists := enableMap[email]; exists && !enable {
			continue
		}

		if manualEnable, ok := c["enable"].(bool); ok && !manualEnable {
			continue
		}

		finalClients = append(finalClients, c)
	}

	settings["clients"] = finalClients
	modifiedSettings, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, err
	}
	runtimeInbound.Settings = string(modifiedSettings)

	return &runtimeInbound, nil
}

// updateClientTraffics syncs the ClientTraffic rows with the inbound's clients
// list: removes rows for emails that disappeared, inserts rows for newly-added
// emails. Uses sets for O(N) lookup — the previous nested-loop implementation
// was O(N²) and degraded into multi-second pauses on inbounds with thousands
// of clients (toggling, saving, or deleting any such inbound felt frozen).
func (s *InboundService) updateClientTraffics(tx *gorm.DB, oldInbound *model.Inbound, newInbound *model.Inbound) error {
	oldClients, err := s.GetClients(oldInbound)
	if err != nil {
		return err
	}
	newClients, err := s.GetClients(newInbound)
	if err != nil {
		return err
	}

	// Email is the unique key for ClientTraffic rows. Clients without an
	// email have no stats row to sync — skip them on both sides instead of
	// risking a unique-constraint hit or accidental delete of an unrelated row.
	oldEmails := make(map[string]struct{}, len(oldClients))
	for i := range oldClients {
		if oldClients[i].Email == "" {
			continue
		}
		oldEmails[oldClients[i].Email] = struct{}{}
	}
	newEmails := make(map[string]struct{}, len(newClients))
	for i := range newClients {
		if newClients[i].Email == "" {
			continue
		}
		newEmails[newClients[i].Email] = struct{}{}
	}

	// Removed clients — drop their stats rows.
	for i := range oldClients {
		email := oldClients[i].Email
		if email == "" {
			continue
		}
		if _, kept := newEmails[email]; kept {
			continue
		}
		if err := s.DelClientStat(tx, email); err != nil {
			return err
		}
	}
	// Added clients — create their stats rows.
	for i := range newClients {
		email := newClients[i].Email
		if email == "" {
			continue
		}
		if _, existed := oldEmails[email]; existed {
			continue
		}
		if err := s.AddClientStat(tx, oldInbound.Id, &newClients[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *InboundService) AddInboundClient(data *model.Inbound) (bool, error) {
	clients, err := s.GetClients(data)
	if err != nil {
		return false, err
	}

	var settings map[string]any
	err = json.Unmarshal([]byte(data.Settings), &settings)
	if err != nil {
		return false, err
	}

	interfaceClients := clientsArray(settings)
	// Add timestamps for new clients being appended
	nowTs := time.Now().Unix() * 1000
	for i := range interfaceClients {
		if cm, ok := interfaceClients[i].(map[string]any); ok {
			if _, ok2 := cm["created_at"]; !ok2 {
				cm["created_at"] = nowTs
			}
			cm["updated_at"] = nowTs
			interfaceClients[i] = cm
		}
	}
	existEmail, err := s.checkEmailsExistForClients(clients)
	if err != nil {
		return false, err
	}
	if existEmail != "" {
		return false, common.NewError("Duplicate email:", existEmail)
	}

	oldInbound, err := s.GetInbound(data.Id)
	if err != nil {
		return false, err
	}
	// MTProto clients live in their own table and are managed via the dedicated
	// /panel/api/mtproto client endpoints, not the generic inbound client path.
	if oldInbound.Protocol == model.MTProto {
		return false, common.NewError("MTProto clients are managed via the MTProto client API")
	}

	// Secure client ID
	for _, client := range clients {
		switch oldInbound.Protocol {
		case "trojan":
			if client.Password == "" {
				return false, common.NewError("empty client ID")
			}
		case "shadowsocks", "mixed", "http":
			// SS uses the cipher email as ID; mixed/http use the basic-auth username
			// (stored in panel as Email) as the per-user identity.
			if client.Email == "" {
				return false, common.NewError("empty client ID")
			}
		case "hysteria", "hysteria2":
			if client.Auth == "" {
				return false, common.NewError("empty client ID")
			}
		default:
			if client.ID == "" {
				return false, common.NewError("empty client ID")
			}
		}
	}

	var oldSettings map[string]any
	err = json.Unmarshal([]byte(oldInbound.Settings), &oldSettings)
	if err != nil {
		return false, err
	}

	oldClients := clientsArray(oldSettings)
	oldClients = append(oldClients, interfaceClients...)

	oldSettings["clients"] = oldClients

	newSettings, err := json.MarshalIndent(oldSettings, "", "  ")
	if err != nil {
		return false, err
	}

	oldInbound.Settings = string(newSettings)

	db := database.GetDB()
	tx := db.Begin()

	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()

	needRestart := false
	s.xrayApi.Init(xrayAPIPort())
	for _, client := range clients {
		if len(client.Email) > 0 {
			s.AddClientStat(tx, data.Id, &client)
			if client.Enable {
				// xray doesn't expose AddUser for mixed/http inbounds, so a config
				// rebuild is required for the new basic-auth account to take effect.
				if oldInbound.Protocol == "mixed" || oldInbound.Protocol == "http" {
					needRestart = true
					continue
				}
				cipher := ""
				if oldInbound.Protocol == "shadowsocks" {
					cipher, _ = oldSettings["method"].(string)
				}
				err1 := s.xrayApi.AddUser(string(oldInbound.Protocol), oldInbound.Tag, map[string]any{
					"email":    client.Email,
					"id":       client.ID,
					"auth":     client.Auth,
					"security": client.Security,
					"flow":     client.Flow,
					"password": client.Password,
					"cipher":   cipher,
				})
				if err1 == nil {
					logger.Debug("Client added by api:", client.Email)
				} else {
					logger.Debug("Error in adding client by api:", err1)
					needRestart = true
				}
			}
		} else {
			needRestart = true
		}
	}
	s.xrayApi.Close()

	return needRestart, tx.Save(oldInbound).Error
}

func (s *InboundService) getClientPrimaryKey(protocol model.Protocol, client model.Client) string {
	switch protocol {
	case model.Trojan:
		return client.Password
	case model.Shadowsocks:
		return client.Email
	case model.Hysteria:
		return client.Auth
	default:
		return client.ID
	}
}

func (s *InboundService) writeBackClientSubID(sourceInboundID int, sourceProtocol model.Protocol, client model.Client, subID string) (bool, error) {
	client.SubID = subID
	client.UpdatedAt = time.Now().UnixMilli()
	clientID := s.getClientPrimaryKey(sourceProtocol, client)
	if clientID == "" {
		return false, common.NewError("empty client ID")
	}

	settingsBytes, err := json.Marshal(map[string][]model.Client{
		"clients": {client},
	})
	if err != nil {
		return false, err
	}

	updatePayload := &model.Inbound{
		Id:       sourceInboundID,
		Settings: string(settingsBytes),
	}
	return s.UpdateInboundClient(updatePayload, clientID)
}

func (s *InboundService) generateRandomCredential(targetProtocol model.Protocol) string {
	switch targetProtocol {
	case model.VMESS, model.VLESS:
		return uuid.NewString()
	default:
		return strings.ReplaceAll(uuid.NewString(), "-", "")
	}
}

func (s *InboundService) buildTargetClientFromSource(source model.Client, targetProtocol model.Protocol, email string, flow string) (model.Client, error) {
	nowTs := time.Now().UnixMilli()
	target := source
	target.Email = email
	target.CreatedAt = nowTs
	target.UpdatedAt = nowTs

	target.ID = ""
	target.Password = ""
	target.Auth = ""
	target.Flow = ""

	switch targetProtocol {
	case model.VMESS:
		target.ID = s.generateRandomCredential(targetProtocol)
	case model.VLESS:
		target.ID = s.generateRandomCredential(targetProtocol)
		if flow == "xtls-rprx-vision" || flow == "xtls-rprx-vision-udp443" {
			target.Flow = flow
		}
	case model.Trojan, model.Shadowsocks:
		target.Password = s.generateRandomCredential(targetProtocol)
	case model.Hysteria:
		target.Auth = s.generateRandomCredential(targetProtocol)
	default:
		target.ID = s.generateRandomCredential(targetProtocol)
	}

	return target, nil
}

func (s *InboundService) nextAvailableCopiedEmail(originalEmail string, targetID int, occupied map[string]struct{}) string {
	base := fmt.Sprintf("%s_%d", originalEmail, targetID)
	candidate := base
	suffix := 0
	for {
		if _, exists := occupied[strings.ToLower(candidate)]; !exists {
			occupied[strings.ToLower(candidate)] = struct{}{}
			return candidate
		}
		suffix++
		candidate = fmt.Sprintf("%s_%d", base, suffix)
	}
}

func (s *InboundService) CopyInboundClients(targetInboundID int, sourceInboundID int, clientEmails []string, flow string) (*CopyClientsResult, bool, error) {
	result := &CopyClientsResult{
		Added:   []string{},
		Skipped: []string{},
		Errors:  []string{},
	}
	if targetInboundID == sourceInboundID {
		return result, false, common.NewError("source and target inbounds must be different")
	}

	targetInbound, err := s.GetInbound(targetInboundID)
	if err != nil {
		return result, false, err
	}
	sourceInbound, err := s.GetInbound(sourceInboundID)
	if err != nil {
		return result, false, err
	}

	sourceClients, err := s.GetClients(sourceInbound)
	if err != nil {
		return result, false, err
	}
	if len(sourceClients) == 0 {
		return result, false, nil
	}

	allowedEmails := map[string]struct{}{}
	if len(clientEmails) > 0 {
		for _, email := range clientEmails {
			allowedEmails[strings.ToLower(strings.TrimSpace(email))] = struct{}{}
		}
	}

	occupiedEmails := map[string]struct{}{}
	allEmails, err := s.getAllEmails()
	if err != nil {
		return result, false, err
	}
	for _, email := range allEmails {
		clean := strings.Trim(email, "\"")
		if clean != "" {
			occupiedEmails[strings.ToLower(clean)] = struct{}{}
		}
	}

	newClients := make([]model.Client, 0)
	needRestart := false
	for _, sourceClient := range sourceClients {
		originalEmail := strings.TrimSpace(sourceClient.Email)
		if originalEmail == "" {
			continue
		}
		if len(allowedEmails) > 0 {
			if _, ok := allowedEmails[strings.ToLower(originalEmail)]; !ok {
				continue
			}
		}

		if sourceClient.SubID == "" {
			newSubID := uuid.NewString()
			subNeedRestart, subErr := s.writeBackClientSubID(sourceInbound.Id, sourceInbound.Protocol, sourceClient, newSubID)
			if subErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: failed to write source subId: %v", originalEmail, subErr))
				continue
			}
			if subNeedRestart {
				needRestart = true
			}
			sourceClient.SubID = newSubID
		}

		targetEmail := s.nextAvailableCopiedEmail(originalEmail, targetInboundID, occupiedEmails)
		targetClient, buildErr := s.buildTargetClientFromSource(sourceClient, targetInbound.Protocol, targetEmail, flow)
		if buildErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", originalEmail, buildErr))
			continue
		}
		newClients = append(newClients, targetClient)
		result.Added = append(result.Added, targetEmail)
	}

	if len(newClients) == 0 {
		return result, needRestart, nil
	}

	settingsPayload, err := json.Marshal(map[string][]model.Client{
		"clients": newClients,
	})
	if err != nil {
		return result, needRestart, err
	}

	addNeedRestart, err := s.AddInboundClient(&model.Inbound{
		Id:       targetInboundID,
		Settings: string(settingsPayload),
	})
	if err != nil {
		return result, needRestart, err
	}
	if addNeedRestart {
		needRestart = true
	}

	return result, needRestart, nil
}

func (s *InboundService) DelInboundClient(inboundId int, clientId string) (bool, error) {
	oldInbound, err := s.GetInbound(inboundId)
	if err != nil {
		logger.Error("Load Old Data Error")
		return false, err
	}
	// MTProto clients live in their own table — managed via the MTProto API.
	if oldInbound.Protocol == model.MTProto {
		return false, common.NewError("MTProto clients are managed via the MTProto client API")
	}
	var settings map[string]any
	err = json.Unmarshal([]byte(oldInbound.Settings), &settings)
	if err != nil {
		return false, err
	}

	email := ""
	client_key := "id"
	switch oldInbound.Protocol {
	case "trojan":
		client_key = "password"
	case "shadowsocks", "mixed", "http":
		client_key = "email"
	case "hysteria", "hysteria2":
		client_key = "auth"
	}

	interfaceClients := clientsArray(settings)
	var newClients []any
	needApiDel := false
	clientFound := false
	for _, client := range interfaceClients {
		c, ok := client.(map[string]any)
		if !ok {
			// Not an object: keep it untouched rather than dropping it.
			newClients = append(newClients, client)
			continue
		}
		c_id, _ := c[client_key].(string)
		if c_id == clientId {
			clientFound = true
			email, _ = c["email"].(string)
			needApiDel, _ = c["enable"].(bool)
		} else {
			newClients = append(newClients, client)
		}
	}

	if !clientFound {
		return false, common.NewError("Client Not Found In Inbound For ID:", clientId)
	}

	if len(newClients) == 0 {
		return false, common.NewError("no client remained in Inbound")
	}

	settings["clients"] = newClients
	newSettings, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}

	oldInbound.Settings = string(newSettings)

	db := database.GetDB()

	err = s.DelClientIPs(db, email)
	if err != nil {
		logger.Error("Error in delete client IPs")
		return false, err
	}
	needRestart := false

	if len(email) > 0 {
		notDepleted := true
		err = db.Model(xray.ClientTraffic{}).Select("enable").Where("email = ?", email).First(&notDepleted).Error
		if err != nil {
			logger.Error("Get stats error")
			return false, err
		}
		err = s.DelClientStat(db, email)
		if err != nil {
			logger.Error("Delete stats Data Error")
			return false, err
		}
		if needApiDel && notDepleted {
			// mixed/http need a config rebuild — xray API has no RemoveUser handler for them.
			if oldInbound.Protocol == "mixed" || oldInbound.Protocol == "http" {
				needRestart = true
			} else {
				s.xrayApi.Init(xrayAPIPort())
				err1 := s.xrayApi.RemoveUser(oldInbound.Tag, email)
				if err1 == nil {
					logger.Debug("Client deleted by api:", email)
					needRestart = false
				} else {
					if strings.Contains(err1.Error(), fmt.Sprintf("User %s not found.", email)) {
						logger.Debug("User is already deleted. Nothing to do more...")
					} else {
						logger.Debug("Error in deleting client by api:", err1)
						needRestart = true
					}
				}
				s.xrayApi.Close()
			}
		}
	}
	return needRestart, db.Save(oldInbound).Error
}

func (s *InboundService) UpdateInboundClient(data *model.Inbound, clientId string) (bool, error) {
	// TODO: check if TrafficReset field is updating
	clients, err := s.GetClients(data)
	if err != nil {
		return false, err
	}

	var settings map[string]any
	err = json.Unmarshal([]byte(data.Settings), &settings)
	if err != nil {
		return false, err
	}

	interfaceClients := clientsArray(settings)

	oldInbound, err := s.GetInbound(data.Id)
	if err != nil {
		return false, err
	}
	// MTProto clients live in their own table — managed via the MTProto API.
	if oldInbound.Protocol == model.MTProto {
		return false, common.NewError("MTProto clients are managed via the MTProto client API")
	}

	oldClients, err := s.GetClients(oldInbound)
	if err != nil {
		return false, err
	}

	oldEmail := ""
	newClientId := ""
	clientIndex := -1
	for index, oldClient := range oldClients {
		oldClientId := ""
		switch oldInbound.Protocol {
		case "trojan":
			oldClientId = oldClient.Password
			newClientId = clients[0].Password
		case "shadowsocks", "mixed", "http":
			// mixed/http use the basic-auth username (panel Email) as ID.
			oldClientId = oldClient.Email
			newClientId = clients[0].Email
		case "hysteria", "hysteria2":
			oldClientId = oldClient.Auth
			newClientId = clients[0].Auth
		default:
			oldClientId = oldClient.ID
			newClientId = clients[0].ID
		}
		if clientId == oldClientId {
			oldEmail = oldClient.Email
			clientIndex = index
			break
		}
	}

	// Validate new client ID
	if newClientId == "" || clientIndex == -1 {
		return false, common.NewError("empty client ID")
	}

	if len(clients[0].Email) > 0 && clients[0].Email != oldEmail {
		existEmail, err := s.checkEmailsExistForClients(clients)
		if err != nil {
			return false, err
		}
		if existEmail != "" {
			return false, common.NewError("Duplicate email:", existEmail)
		}
	}

	var oldSettings map[string]any
	err = json.Unmarshal([]byte(oldInbound.Settings), &oldSettings)
	if err != nil {
		return false, err
	}
	settingsClients := clientsArray(oldSettings)
	// Preserve created_at and set updated_at for the replacing client
	var preservedCreated any
	if clientIndex >= 0 && clientIndex < len(settingsClients) {
		if oldMap, ok := settingsClients[clientIndex].(map[string]any); ok {
			if v, ok2 := oldMap["created_at"]; ok2 {
				preservedCreated = v
			}
		}
	}
	if len(interfaceClients) > 0 {
		if newMap, ok := interfaceClients[0].(map[string]any); ok {
			if preservedCreated == nil {
				preservedCreated = time.Now().Unix() * 1000
			}
			newMap["created_at"] = preservedCreated
			newMap["updated_at"] = time.Now().Unix() * 1000
			interfaceClients[0] = newMap
		}
	}
	settingsClients[clientIndex] = interfaceClients[0]
	oldSettings["clients"] = settingsClients

	newSettings, err := json.MarshalIndent(oldSettings, "", "  ")
	if err != nil {
		return false, err
	}

	oldInbound.Settings = string(newSettings)
	db := database.GetDB()
	tx := db.Begin()

	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()

	if len(clients[0].Email) > 0 {
		if len(oldEmail) > 0 {
			err = s.UpdateClientStat(tx, oldEmail, &clients[0])
			if err != nil {
				return false, err
			}
			err = s.UpdateClientIPs(tx, oldEmail, clients[0].Email)
			if err != nil {
				return false, err
			}
		} else {
			s.AddClientStat(tx, data.Id, &clients[0])
		}
	} else {
		err = s.DelClientStat(tx, oldEmail)
		if err != nil {
			return false, err
		}
		err = s.DelClientIPs(tx, oldEmail)
		if err != nil {
			return false, err
		}
	}
	needRestart := false
	if len(oldEmail) > 0 {
		// mixed/http have no AddUser/RemoveUser API handlers in xray-core, so any
		// edit to a client requires a config rebuild.
		if oldInbound.Protocol == "mixed" || oldInbound.Protocol == "http" {
			needRestart = true
		} else {
			s.xrayApi.Init(xrayAPIPort())
			if oldClients[clientIndex].Enable {
				err1 := s.xrayApi.RemoveUser(oldInbound.Tag, oldEmail)
				if err1 == nil {
					logger.Debug("Old client deleted by api:", oldEmail)
				} else {
					if strings.Contains(err1.Error(), fmt.Sprintf("User %s not found.", oldEmail)) {
						logger.Debug("User is already deleted. Nothing to do more...")
					} else {
						logger.Debug("Error in deleting client by api:", err1)
						needRestart = true
					}
				}
			}
			if clients[0].Enable {
				cipher := ""
				if oldInbound.Protocol == "shadowsocks" {
					cipher, _ = oldSettings["method"].(string)
				}
				err1 := s.xrayApi.AddUser(string(oldInbound.Protocol), oldInbound.Tag, map[string]any{
					"email":    clients[0].Email,
					"id":       clients[0].ID,
					"security": clients[0].Security,
					"flow":     clients[0].Flow,
					"auth":     clients[0].Auth,
					"password": clients[0].Password,
					"cipher":   cipher,
				})
				if err1 == nil {
					logger.Debug("Client edited by api:", clients[0].Email)
				} else {
					logger.Debug("Error in adding client by api:", err1)
					needRestart = true
				}
			}
			s.xrayApi.Close()
		}
	} else {
		logger.Debug("Client old email not found")
		needRestart = true
	}
	return needRestart, tx.Save(oldInbound).Error
}

func (s *InboundService) AddTraffic(inboundTraffics []*xray.Traffic, clientTraffics []*xray.ClientTraffic) (bool, bool, error) {
	var err error
	db := database.GetDB()
	tx := db.Begin()

	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()
	err = s.addInboundTraffic(tx, inboundTraffics)
	if err != nil {
		return false, false, err
	}
	err = s.addClientTraffic(tx, clientTraffics)
	if err != nil {
		return false, false, err
	}

	// The steps below are best-effort maintenance: their failures are logged
	// but must not reach the deferred rollback check above. Assigning them to
	// `err` (as this code used to) rolled back the traffic writes while still
	// returning success — and since GetXrayTraffic reads counters with reset,
	// that traffic was gone for good.
	needRestart0, count, renewErr := s.autoRenewClients(tx)
	if renewErr != nil {
		logger.Warning("Error in renew clients:", renewErr)
	} else if count > 0 {
		logger.Debugf("%v clients renewed", count)
	}

	disabledClientsCount := int64(0)
	needRestart1, count, disableClientsErr := s.disableInvalidClients(tx)
	if disableClientsErr != nil {
		logger.Warning("Error in disabling invalid clients:", disableClientsErr)
	} else if count > 0 {
		logger.Debugf("%v clients disabled", count)
		disabledClientsCount = count
	}

	needRestart2, count, disableInboundsErr := s.disableInvalidInbounds(tx)
	if disableInboundsErr != nil {
		logger.Warning("Error in disabling invalid inbounds:", disableInboundsErr)
	} else if count > 0 {
		logger.Debugf("%v inbounds disabled", count)
	}
	return needRestart0 || needRestart1 || needRestart2, disabledClientsCount > 0, nil
}

func (s *InboundService) addInboundTraffic(tx *gorm.DB, traffics []*xray.Traffic) error {
	if len(traffics) == 0 {
		return nil
	}

	var err error

	for _, traffic := range traffics {
		if traffic.IsInbound {
			err = tx.Model(&model.Inbound{}).Where("tag = ?", traffic.Tag).
				Updates(map[string]any{
					"up":       gorm.Expr("up + ?", traffic.Up),
					"down":     gorm.Expr("down + ?", traffic.Down),
					"all_time": gorm.Expr("COALESCE(all_time, 0) + ?", traffic.Up+traffic.Down),
				}).Error
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *InboundService) addClientTraffic(tx *gorm.DB, traffics []*xray.ClientTraffic) (err error) {
	if len(traffics) == 0 {
		// Empty onlineUsers
		if proc := currentProcess(); proc != nil {
			proc.SetOnlineClients(make([]string, 0))
		}
		return nil
	}

	onlineClients := make([]string, 0)

	emails := make([]string, 0, len(traffics))
	for _, traffic := range traffics {
		emails = append(emails, traffic.Email)
	}
	dbClientTraffics := make([]*xray.ClientTraffic, 0, len(traffics))
	err = tx.Model(xray.ClientTraffic{}).Where("email IN (?)", emails).Find(&dbClientTraffics).Error
	if err != nil {
		return err
	}

	// Nothing matched in client_traffics (renamed/removed clients, or a stale
	// import): still publish the empty online list, otherwise the previous
	// tick's users stay lit in the UI until a tick does match.
	if len(dbClientTraffics) == 0 {
		if proc := currentProcess(); proc != nil {
			proc.SetOnlineClients(onlineClients)
		}
		return nil
	}

	dbClientTraffics, err = s.adjustTraffics(tx, dbClientTraffics)
	if err != nil {
		return err
	}

	// Index by email for O(N) merge — the previous nested loop was O(N²)
	// and dominated each cron tick on inbounds with thousands of active
	// clients (7500 × 7500 = 56M string comparisons every 10 seconds).
	trafficByEmail := make(map[string]*xray.ClientTraffic, len(traffics))
	for i := range traffics {
		if traffics[i] != nil {
			trafficByEmail[traffics[i].Email] = traffics[i]
		}
	}
	now := time.Now().UnixMilli()
	for dbTraffic_index := range dbClientTraffics {
		t, ok := trafficByEmail[dbClientTraffics[dbTraffic_index].Email]
		if !ok {
			continue
		}
		dbClientTraffics[dbTraffic_index].Up += t.Up
		dbClientTraffics[dbTraffic_index].Down += t.Down
		dbClientTraffics[dbTraffic_index].AllTime += t.Up + t.Down
		if t.Up+t.Down > 0 {
			onlineClients = append(onlineClients, t.Email)
			dbClientTraffics[dbTraffic_index].LastOnline = now
		}
	}

	// Set onlineUsers
	if proc := currentProcess(); proc != nil {
		proc.SetOnlineClients(onlineClients)
	}

	err = tx.Save(dbClientTraffics).Error
	if err != nil {
		logger.Warning("AddClientTraffic update data ", err)
	}

	return nil
}

func (s *InboundService) adjustTraffics(tx *gorm.DB, dbClientTraffics []*xray.ClientTraffic) ([]*xray.ClientTraffic, error) {
	inboundIds := make([]int, 0, len(dbClientTraffics))
	for _, dbClientTraffic := range dbClientTraffics {
		if dbClientTraffic.ExpiryTime < 0 {
			inboundIds = append(inboundIds, dbClientTraffic.InboundId)
		}
	}

	if len(inboundIds) > 0 {
		var inbounds []*model.Inbound
		err := tx.Model(model.Inbound{}).Where("id IN (?)", inboundIds).Find(&inbounds).Error
		if err != nil {
			return nil, err
		}
		for inbound_index := range inbounds {
			settings := map[string]any{}
			json.Unmarshal([]byte(inbounds[inbound_index].Settings), &settings)
			clients, ok := settings["clients"].([]any)
			if ok {
				var newClients []any
				for client_index := range clients {
					c, ok := clients[client_index].(map[string]any)
					if !ok {
						newClients = append(newClients, clients[client_index])
						continue
					}
					for traffic_index := range dbClientTraffics {
						if dbClientTraffics[traffic_index].ExpiryTime < 0 && c["email"] == dbClientTraffics[traffic_index].Email {
							oldExpiryTime, _ := c["expiryTime"].(float64)
							newExpiryTime := (time.Now().Unix() * 1000) - int64(oldExpiryTime)
							c["expiryTime"] = newExpiryTime
							c["updated_at"] = time.Now().Unix() * 1000
							dbClientTraffics[traffic_index].ExpiryTime = newExpiryTime
							break
						}
					}
					// Backfill created_at and updated_at
					if _, ok := c["created_at"]; !ok {
						c["created_at"] = time.Now().Unix() * 1000
					}
					c["updated_at"] = time.Now().Unix() * 1000
					newClients = append(newClients, any(c))
				}
				settings["clients"] = newClients
				modifiedSettings, err := json.MarshalIndent(settings, "", "  ")
				if err != nil {
					return nil, err
				}

				inbounds[inbound_index].Settings = string(modifiedSettings)
			}
		}
		err = tx.Save(inbounds).Error
		if err != nil {
			logger.Warning("AddClientTraffic update inbounds ", err)
			logger.Error(inbounds)
		}
	}

	return dbClientTraffics, nil
}

func (s *InboundService) autoRenewClients(tx *gorm.DB) (bool, int64, error) {
	// check for time expired
	var traffics []*xray.ClientTraffic
	now := time.Now().Unix() * 1000
	var err, err1 error

	err = tx.Model(xray.ClientTraffic{}).Where("reset > 0 and expiry_time > 0 and expiry_time <= ?", now).Find(&traffics).Error
	if err != nil {
		return false, 0, err
	}
	// return if there is no client to renew
	if len(traffics) == 0 {
		return false, 0, nil
	}

	var inbound_ids []int
	var inbounds []*model.Inbound
	needRestart := false
	var clientsToAdd []struct {
		protocol string
		tag      string
		client   map[string]any
	}

	for _, traffic := range traffics {
		inbound_ids = append(inbound_ids, traffic.InboundId)
	}
	// Dedupe so an inbound hosting N expired clients is fetched and saved once
	// per tick instead of N times across chunk boundaries.
	inbound_ids = uniqueInts(inbound_ids)
	// Chunked to stay under SQLite's bind-variable limit when many inbounds
	// are touched in a single tick.
	for _, batch := range chunkInts(inbound_ids, sqliteMaxVars) {
		var page []*model.Inbound
		if err = tx.Model(model.Inbound{}).Where("id IN ?", batch).Find(&page).Error; err != nil {
			return false, 0, err
		}
		inbounds = append(inbounds, page...)
	}
	for inbound_index := range inbounds {
		settings := map[string]any{}
		json.Unmarshal([]byte(inbounds[inbound_index].Settings), &settings)
		clients := clientsArray(settings)
		for client_index := range clients {
			c, ok := clients[client_index].(map[string]any)
			if !ok {
				continue
			}
			clientEmail, _ := c["email"].(string)
			for traffic_index, traffic := range traffics {
				if traffic.Email == clientEmail {
					newExpiryTime := traffic.ExpiryTime
					for newExpiryTime < now {
						newExpiryTime += (int64(traffic.Reset) * 86400000)
					}
					c["expiryTime"] = newExpiryTime
					traffics[traffic_index].ExpiryTime = newExpiryTime
					traffics[traffic_index].Down = 0
					traffics[traffic_index].Up = 0
					if !traffic.Enable {
						traffics[traffic_index].Enable = true
						clientsToAdd = append(clientsToAdd,
							struct {
								protocol string
								tag      string
								client   map[string]any
							}{
								protocol: string(inbounds[inbound_index].Protocol),
								tag:      inbounds[inbound_index].Tag,
								client:   c,
							})
					}
					clients[client_index] = any(c)
					break
				}
			}
		}
		settings["clients"] = clients
		newSettings, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return false, 0, err
		}
		inbounds[inbound_index].Settings = string(newSettings)
	}
	err = tx.Save(inbounds).Error
	if err != nil {
		return false, 0, err
	}
	err = tx.Save(traffics).Error
	if err != nil {
		return false, 0, err
	}
	if currentProcess() != nil {
		err1 = s.xrayApi.Init(xrayAPIPort())
		if err1 != nil {
			return true, int64(len(traffics)), nil
		}
		for _, clientToAdd := range clientsToAdd {
			err1 = s.xrayApi.AddUser(clientToAdd.protocol, clientToAdd.tag, clientToAdd.client)
			if err1 != nil {
				needRestart = true
			}
		}
		s.xrayApi.Close()
	}
	return needRestart, int64(len(traffics)), nil
}

func (s *InboundService) disableInvalidInbounds(tx *gorm.DB) (bool, int64, error) {
	now := time.Now().Unix() * 1000
	needRestart := false

	if currentProcess() != nil {
		var tags []string
		err := tx.Table("inbounds").
			Select("inbounds.tag").
			Where("((total > 0 and up + down >= total) or (expiry_time > 0 and expiry_time <= ?)) and enable = ?", now, true).
			Scan(&tags).Error
		if err != nil {
			return false, 0, err
		}
		s.xrayApi.Init(xrayAPIPort())
		for _, tag := range tags {
			err1 := s.xrayApi.DelInbound(tag)
			if err1 == nil {
				logger.Debug("Inbound disabled by api:", tag)
			} else {
				logger.Debug("Error in disabling inbound by api:", err1)
				needRestart = true
			}
		}
		s.xrayApi.Close()
	}

	result := tx.Model(model.Inbound{}).
		Where("((total > 0 and up + down >= total) or (expiry_time > 0 and expiry_time <= ?)) and enable = ?", now, true).
		Update("enable", false)
	err := result.Error
	count := result.RowsAffected
	return needRestart, count, err
}

func (s *InboundService) disableInvalidClients(tx *gorm.DB) (bool, int64, error) {
	now := time.Now().Unix() * 1000
	needRestart := false

	var clientsToDisable []struct {
		InboundId int
		Tag       string
		Email     string
	}

	err := tx.Table("inbounds").
		Select("inbounds.id as inbound_id, inbounds.tag, client_traffics.email").
		Joins("JOIN client_traffics ON inbounds.id = client_traffics.inbound_id").
		Where("((client_traffics.total > 0 AND client_traffics.up + client_traffics.down >= client_traffics.total) OR (client_traffics.expiry_time > 0 AND client_traffics.expiry_time <= ?)) AND client_traffics.enable = ?", now, true).
		Scan(&clientsToDisable).Error
	if err != nil {
		return false, 0, err
	}

	if currentProcess() != nil {
		s.xrayApi.Init(xrayAPIPort())
		for _, client := range clientsToDisable {
			err1 := s.xrayApi.RemoveUser(client.Tag, client.Email)
			if err1 == nil {
				logger.Debug("Client disabled by api:", client.Email)
			} else {
				if strings.Contains(err1.Error(), fmt.Sprintf("User %s not found.", client.Email)) {
					logger.Debug("User is already disabled. Nothing to do more...")
				} else {
					logger.Debug("Error in disabling client by api:", err1)
					needRestart = true
				}
			}
		}
		s.xrayApi.Close()
	}

	result := tx.Model(xray.ClientTraffic{}).
		Where("((total > 0 and up + down >= total) or (expiry_time > 0 and expiry_time <= ?)) and enable = ?", now, true).
		Update("enable", false)
	err = result.Error
	count := result.RowsAffected
	if err != nil {
		return needRestart, count, err
	}

	// Also set enable=false in inbounds.settings JSON so clients are visibly disabled
	if len(clientsToDisable) > 0 {
		inboundEmailMap := make(map[int]map[string]struct{})
		for _, c := range clientsToDisable {
			if inboundEmailMap[c.InboundId] == nil {
				inboundEmailMap[c.InboundId] = make(map[string]struct{})
			}
			inboundEmailMap[c.InboundId][c.Email] = struct{}{}
		}
		inboundIds := make([]int, 0, len(inboundEmailMap))
		for id := range inboundEmailMap {
			inboundIds = append(inboundIds, id)
		}
		var inbounds []*model.Inbound
		if err = tx.Model(model.Inbound{}).Where("id IN ?", inboundIds).Find(&inbounds).Error; err != nil {
			logger.Warning("disableInvalidClients fetch inbounds:", err)
			return needRestart, count, nil
		}
		for _, inbound := range inbounds {
			settings := map[string]any{}
			if jsonErr := json.Unmarshal([]byte(inbound.Settings), &settings); jsonErr != nil {
				continue
			}
			clients, ok := settings["clients"].([]any)
			if !ok {
				continue
			}
			emailSet := inboundEmailMap[inbound.Id]
			changed := false
			for i := range clients {
				c, ok := clients[i].(map[string]any)
				if !ok {
					continue
				}
				email, _ := c["email"].(string)
				if _, shouldDisable := emailSet[email]; shouldDisable {
					c["enable"] = false
					c["updated_at"] = time.Now().Unix() * 1000
					clients[i] = c
					changed = true
				}
			}
			if changed {
				settings["clients"] = clients
				modifiedSettings, jsonErr := json.MarshalIndent(settings, "", "  ")
				if jsonErr != nil {
					continue
				}
				inbound.Settings = string(modifiedSettings)
			}
		}
		if err = tx.Save(inbounds).Error; err != nil {
			logger.Warning("disableInvalidClients update inbound settings:", err)
		}
	}

	return needRestart, count, nil
}

func (s *InboundService) GetInboundTags() (string, error) {
	db := database.GetDB()
	var inboundTags []string
	// Exclude AWG/NativeWG/MTProto: their inbounds never enter the Xray config
	// (xray.go skips them), so surfacing those tags in the routing rule
	// dropdown invites broken rules. The TPROXY-mode tags below replace
	// them as the real, working inbound identities for chained tunnels.
	err := db.Model(model.Inbound{}).
		Select("tag").
		Where("protocol NOT IN ?", []string{string(model.AmneziaWG), string(model.NativeWG), string(model.MTProto)}).
		Find(&inboundTags).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return "", err
	}

	// A routed MTProto inbound does have an identity inside the generated
	// config: injectMtprotoEgress (xray.go) adds a loopback SOCKS inbound
	// carrying the inbound's own tag. Offer that tag so its Telegram traffic can
	// be steered by hand-written rules — the same reason the TPROXY tags below
	// are offered. Unrouted mtproto inbounds stay hidden: nothing in the config
	// answers to their tag.
	var mtprotoInbounds []*model.Inbound
	if err := db.Model(model.Inbound{}).
		Where("protocol = ? AND enable = ?", string(model.MTProto), true).
		Order("id").Find(&mtprotoInbounds).Error; err != nil {
		logger.Warning("GetInboundTags: could not read mtproto inbounds:", err)
	} else {
		for _, in := range mtprotoInbounds {
			if in.Tag != "" && mtprotoRoutesThroughXray(in) {
				inboundTags = append(inboundTags, in.Tag)
			}
		}
	}

	// Append synthetic TPROXY-inbound tags for enabled tunnel servers that
	// chose RouteViaXray: these are the tags the user actually targets when
	// building Home → WG/AWG → Xray → upstream-VPN routes.
	var tunnels []model.TunnelServer
	if err := db.Where("enable = ? AND route_via_xray = ?", true, true).
		Order("kind").Find(&tunnels).Error; err == nil {
		for _, t := range tunnels {
			tag := t.XrayInboundTag
			if tag == "" {
				tag = t.Kind + "-tproxy-in"
			}
			inboundTags = append(inboundTags, tag)
		}
	}

	tags, _ := json.Marshal(inboundTags)
	return string(tags), nil
}

func (s *InboundService) GetClientReverseTags() (string, error) {
	db := database.GetDB()
	var rawTags []string
	err := db.Raw(`
		SELECT DISTINCT JSON_EXTRACT(client.value, '$.reverse.tag')
		FROM inbounds,
			JSON_EACH(JSON_EXTRACT(inbounds.settings, '$.clients')) AS client
		WHERE inbounds.protocol = 'vless'
		  AND JSON_EXTRACT(client.value, '$.reverse.tag') IS NOT NULL
		  AND JSON_EXTRACT(client.value, '$.reverse.tag') != ''
	`).Scan(&rawTags).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return "[]", err
	}
	result, _ := json.Marshal(rawTags)
	return string(result), nil
}

func (s *InboundService) MigrationRemoveOrphanedTraffics() {
	db := database.GetDB()
	db.Exec(`
		DELETE FROM client_traffics
		WHERE email NOT IN (
			SELECT JSON_EXTRACT(client.value, '$.email')
			FROM inbounds,
				JSON_EACH(JSON_EXTRACT(inbounds.settings, '$.clients')) AS client
		)
	`)
}

func (s *InboundService) AddClientStat(tx *gorm.DB, inboundId int, client *model.Client) error {
	clientTraffic := xray.ClientTraffic{}
	clientTraffic.InboundId = inboundId
	clientTraffic.Email = client.Email
	clientTraffic.Total = client.TotalGB
	clientTraffic.ExpiryTime = client.ExpiryTime
	clientTraffic.Enable = client.Enable
	clientTraffic.Up = 0
	clientTraffic.Down = 0
	clientTraffic.Reset = client.Reset
	result := tx.Create(&clientTraffic)
	err := result.Error
	return err
}

func (s *InboundService) UpdateClientStat(tx *gorm.DB, email string, client *model.Client) error {
	result := tx.Model(xray.ClientTraffic{}).
		Where("email = ?", email).
		Updates(map[string]any{
			"enable":      client.Enable,
			"email":       client.Email,
			"total":       client.TotalGB,
			"expiry_time": client.ExpiryTime,
			"reset":       client.Reset,
		})
	err := result.Error
	return err
}

func (s *InboundService) UpdateClientIPs(tx *gorm.DB, oldEmail string, newEmail string) error {
	return tx.Model(model.InboundClientIps{}).Where("client_email = ?", oldEmail).Update("client_email", newEmail).Error
}

func (s *InboundService) DelClientStat(tx *gorm.DB, email string) error {
	return tx.Where("email = ?", email).Delete(xray.ClientTraffic{}).Error
}

func (s *InboundService) DelClientIPs(tx *gorm.DB, email string) error {
	return tx.Where("client_email = ?", email).Delete(model.InboundClientIps{}).Error
}

func (s *InboundService) GetClientInboundByTrafficID(trafficId int) (traffic *xray.ClientTraffic, inbound *model.Inbound, err error) {
	db := database.GetDB()
	var traffics []*xray.ClientTraffic
	err = db.Model(xray.ClientTraffic{}).Where("id = ?", trafficId).Find(&traffics).Error
	if err != nil {
		logger.Warningf("Error retrieving ClientTraffic with trafficId %d: %v", trafficId, err)
		return nil, nil, err
	}
	if len(traffics) > 0 {
		inbound, err = s.GetInbound(traffics[0].InboundId)
		return traffics[0], inbound, err
	}
	return nil, nil, nil
}

func (s *InboundService) GetClientInboundByEmail(email string) (traffic *xray.ClientTraffic, inbound *model.Inbound, err error) {
	db := database.GetDB()
	var traffics []*xray.ClientTraffic
	err = db.Model(xray.ClientTraffic{}).Where("email = ?", email).Find(&traffics).Error
	if err != nil {
		logger.Warningf("Error retrieving ClientTraffic with email %s: %v", email, err)
		return nil, nil, err
	}
	if len(traffics) > 0 {
		inbound, err = s.GetInbound(traffics[0].InboundId)
		return traffics[0], inbound, err
	}
	return nil, nil, nil
}

func (s *InboundService) GetClientByEmail(clientEmail string) (*xray.ClientTraffic, *model.Client, error) {
	traffic, inbound, err := s.GetClientInboundByEmail(clientEmail)
	if err != nil {
		return nil, nil, err
	}
	if inbound == nil {
		return nil, nil, common.NewError("Inbound Not Found For Email:", clientEmail)
	}

	clients, err := s.GetClients(inbound)
	if err != nil {
		return nil, nil, err
	}

	for _, client := range clients {
		if client.Email == clientEmail {
			return traffic, &client, nil
		}
	}

	return nil, nil, common.NewError("Client Not Found In Inbound For Email:", clientEmail)
}

func (s *InboundService) SetClientTelegramUserID(trafficId int, tgId int64) (bool, error) {
	traffic, inbound, err := s.GetClientInboundByTrafficID(trafficId)
	if err != nil {
		return false, err
	}
	if inbound == nil {
		return false, common.NewError("Inbound Not Found For Traffic ID:", trafficId)
	}

	clientEmail := traffic.Email

	oldClients, err := s.GetClients(inbound)
	if err != nil {
		return false, err
	}

	clientId := ""

	for _, oldClient := range oldClients {
		if oldClient.Email == clientEmail {
			switch inbound.Protocol {
			case "trojan":
				clientId = oldClient.Password
			case "shadowsocks":
				clientId = oldClient.Email
			default:
				clientId = oldClient.ID
			}
			break
		}
	}

	if len(clientId) == 0 {
		return false, common.NewError("Client Not Found For Email:", clientEmail)
	}

	var settings map[string]any
	err = json.Unmarshal([]byte(inbound.Settings), &settings)
	if err != nil {
		return false, err
	}
	clients := clientsArray(settings)
	var newClients []any
	for client_index := range clients {
		c, ok := clients[client_index].(map[string]any)
		if !ok {
			continue
		}
		if c["email"] == clientEmail {
			c["tgId"] = tgId
			c["updated_at"] = time.Now().Unix() * 1000
			newClients = append(newClients, any(c))
		}
	}
	settings["clients"] = newClients
	modifiedSettings, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	inbound.Settings = string(modifiedSettings)
	needRestart, err := s.UpdateInboundClient(inbound, clientId)
	return needRestart, err
}

func (s *InboundService) checkIsEnabledByEmail(clientEmail string) (bool, error) {
	_, inbound, err := s.GetClientInboundByEmail(clientEmail)
	if err != nil {
		return false, err
	}
	if inbound == nil {
		return false, common.NewError("Inbound Not Found For Email:", clientEmail)
	}

	clients, err := s.GetClients(inbound)
	if err != nil {
		return false, err
	}

	isEnable := false

	for _, client := range clients {
		if client.Email == clientEmail {
			isEnable = client.Enable
			break
		}
	}

	return isEnable, err
}

func (s *InboundService) ToggleClientEnableByEmail(clientEmail string) (bool, bool, error) {
	_, inbound, err := s.GetClientInboundByEmail(clientEmail)
	if err != nil {
		return false, false, err
	}
	if inbound == nil {
		return false, false, common.NewError("Inbound Not Found For Email:", clientEmail)
	}

	oldClients, err := s.GetClients(inbound)
	if err != nil {
		return false, false, err
	}

	clientId := ""
	clientOldEnabled := false

	for _, oldClient := range oldClients {
		if oldClient.Email == clientEmail {
			switch inbound.Protocol {
			case "trojan":
				clientId = oldClient.Password
			case "shadowsocks":
				clientId = oldClient.Email
			default:
				clientId = oldClient.ID
			}
			clientOldEnabled = oldClient.Enable
			break
		}
	}

	if len(clientId) == 0 {
		return false, false, common.NewError("Client Not Found For Email:", clientEmail)
	}

	var settings map[string]any
	err = json.Unmarshal([]byte(inbound.Settings), &settings)
	if err != nil {
		return false, false, err
	}
	clients := clientsArray(settings)
	var newClients []any
	for client_index := range clients {
		c, ok := clients[client_index].(map[string]any)
		if !ok {
			continue
		}
		if c["email"] == clientEmail {
			c["enable"] = !clientOldEnabled
			c["updated_at"] = time.Now().Unix() * 1000
			newClients = append(newClients, any(c))
		}
	}
	settings["clients"] = newClients
	modifiedSettings, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, false, err
	}
	inbound.Settings = string(modifiedSettings)

	needRestart, err := s.UpdateInboundClient(inbound, clientId)
	if err != nil {
		return false, needRestart, err
	}

	return !clientOldEnabled, needRestart, nil
}

// SetClientEnableByEmail sets client enable state to desired value; returns (changed, needRestart, error)
func (s *InboundService) SetClientEnableByEmail(clientEmail string, enable bool) (bool, bool, error) {
	current, err := s.checkIsEnabledByEmail(clientEmail)
	if err != nil {
		return false, false, err
	}
	if current == enable {
		return false, false, nil
	}
	newEnabled, needRestart, err := s.ToggleClientEnableByEmail(clientEmail)
	if err != nil {
		return false, needRestart, err
	}
	return newEnabled == enable, needRestart, nil
}

func (s *InboundService) ResetClientIpLimitByEmail(clientEmail string, count int) (bool, error) {
	_, inbound, err := s.GetClientInboundByEmail(clientEmail)
	if err != nil {
		return false, err
	}
	if inbound == nil {
		return false, common.NewError("Inbound Not Found For Email:", clientEmail)
	}

	oldClients, err := s.GetClients(inbound)
	if err != nil {
		return false, err
	}

	clientId := ""

	for _, oldClient := range oldClients {
		if oldClient.Email == clientEmail {
			switch inbound.Protocol {
			case "trojan":
				clientId = oldClient.Password
			case "shadowsocks":
				clientId = oldClient.Email
			default:
				clientId = oldClient.ID
			}
			break
		}
	}

	if len(clientId) == 0 {
		return false, common.NewError("Client Not Found For Email:", clientEmail)
	}

	var settings map[string]any
	err = json.Unmarshal([]byte(inbound.Settings), &settings)
	if err != nil {
		return false, err
	}
	clients := clientsArray(settings)
	var newClients []any
	for client_index := range clients {
		c, ok := clients[client_index].(map[string]any)
		if !ok {
			continue
		}
		if c["email"] == clientEmail {
			c["limitIp"] = count
			c["updated_at"] = time.Now().Unix() * 1000
			newClients = append(newClients, any(c))
		}
	}
	settings["clients"] = newClients
	modifiedSettings, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	inbound.Settings = string(modifiedSettings)
	needRestart, err := s.UpdateInboundClient(inbound, clientId)
	return needRestart, err
}

func (s *InboundService) ResetClientExpiryTimeByEmail(clientEmail string, expiry_time int64) (bool, error) {
	_, inbound, err := s.GetClientInboundByEmail(clientEmail)
	if err != nil {
		return false, err
	}
	if inbound == nil {
		return false, common.NewError("Inbound Not Found For Email:", clientEmail)
	}

	oldClients, err := s.GetClients(inbound)
	if err != nil {
		return false, err
	}

	clientId := ""

	for _, oldClient := range oldClients {
		if oldClient.Email == clientEmail {
			switch inbound.Protocol {
			case "trojan":
				clientId = oldClient.Password
			case "shadowsocks":
				clientId = oldClient.Email
			default:
				clientId = oldClient.ID
			}
			break
		}
	}

	if len(clientId) == 0 {
		return false, common.NewError("Client Not Found For Email:", clientEmail)
	}

	var settings map[string]any
	err = json.Unmarshal([]byte(inbound.Settings), &settings)
	if err != nil {
		return false, err
	}
	clients := clientsArray(settings)
	var newClients []any
	for client_index := range clients {
		c, ok := clients[client_index].(map[string]any)
		if !ok {
			continue
		}
		if c["email"] == clientEmail {
			c["expiryTime"] = expiry_time
			c["updated_at"] = time.Now().Unix() * 1000
			newClients = append(newClients, any(c))
		}
	}
	settings["clients"] = newClients
	modifiedSettings, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	inbound.Settings = string(modifiedSettings)
	needRestart, err := s.UpdateInboundClient(inbound, clientId)
	return needRestart, err
}

func (s *InboundService) ResetClientTrafficLimitByEmail(clientEmail string, totalGB int) (bool, error) {
	if totalGB < 0 {
		return false, common.NewError("totalGB must be >= 0")
	}
	_, inbound, err := s.GetClientInboundByEmail(clientEmail)
	if err != nil {
		return false, err
	}
	if inbound == nil {
		return false, common.NewError("Inbound Not Found For Email:", clientEmail)
	}

	oldClients, err := s.GetClients(inbound)
	if err != nil {
		return false, err
	}

	clientId := ""

	for _, oldClient := range oldClients {
		if oldClient.Email == clientEmail {
			switch inbound.Protocol {
			case "trojan":
				clientId = oldClient.Password
			case "shadowsocks":
				clientId = oldClient.Email
			default:
				clientId = oldClient.ID
			}
			break
		}
	}

	if len(clientId) == 0 {
		return false, common.NewError("Client Not Found For Email:", clientEmail)
	}

	var settings map[string]any
	err = json.Unmarshal([]byte(inbound.Settings), &settings)
	if err != nil {
		return false, err
	}
	clients := clientsArray(settings)
	var newClients []any
	for client_index := range clients {
		c, ok := clients[client_index].(map[string]any)
		if !ok {
			continue
		}
		if c["email"] == clientEmail {
			c["totalGB"] = totalGB * 1024 * 1024 * 1024
			c["updated_at"] = time.Now().Unix() * 1000
			newClients = append(newClients, any(c))
		}
	}
	settings["clients"] = newClients
	modifiedSettings, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	inbound.Settings = string(modifiedSettings)
	needRestart, err := s.UpdateInboundClient(inbound, clientId)
	return needRestart, err
}

func (s *InboundService) ResetClientTrafficByEmail(clientEmail string) error {
	db := database.GetDB()

	// Reset traffic stats in ClientTraffic table
	result := db.Model(xray.ClientTraffic{}).
		Where("email = ?", clientEmail).
		Updates(map[string]any{"enable": true, "up": 0, "down": 0})

	err := result.Error
	if err != nil {
		return err
	}

	return nil
}

func (s *InboundService) ResetClientTraffic(id int, clientEmail string) (bool, error) {
	needRestart := false

	traffic, err := s.GetClientTrafficByEmail(clientEmail)
	if err != nil {
		return false, err
	}

	if !traffic.Enable {
		inbound, err := s.GetInbound(id)
		if err != nil {
			return false, err
		}
		clients, err := s.GetClients(inbound)
		if err != nil {
			return false, err
		}
		for _, client := range clients {
			if client.Email == clientEmail && client.Enable {
				s.xrayApi.Init(xrayAPIPort())
				cipher := ""
				if string(inbound.Protocol) == "shadowsocks" {
					var oldSettings map[string]any
					err = json.Unmarshal([]byte(inbound.Settings), &oldSettings)
					if err != nil {
						return false, err
					}
					cipher, _ = oldSettings["method"].(string)
				}
				err1 := s.xrayApi.AddUser(string(inbound.Protocol), inbound.Tag, map[string]any{
					"email":    client.Email,
					"id":       client.ID,
					"auth":     client.Auth,
					"security": client.Security,
					"flow":     client.Flow,
					"password": client.Password,
					"cipher":   cipher,
				})
				if err1 == nil {
					logger.Debug("Client enabled due to reset traffic:", clientEmail)
				} else {
					logger.Debug("Error in enabling client by api:", err1)
					needRestart = true
				}
				s.xrayApi.Close()
				break
			}
		}
	}

	traffic.Up = 0
	traffic.Down = 0
	traffic.Enable = true

	db := database.GetDB()
	err = db.Save(traffic).Error
	if err != nil {
		return false, err
	}

	return needRestart, nil
}

func (s *InboundService) ResetAllClientTraffics(id int) error {
	db := database.GetDB()
	now := time.Now().Unix() * 1000

	if err := db.Transaction(func(tx *gorm.DB) error {
		whereText := "inbound_id "
		if id == -1 {
			whereText += " > ?"
		} else {
			whereText += " = ?"
		}

		// Reset client traffics
		result := tx.Model(xray.ClientTraffic{}).
			Where(whereText, id).
			Updates(map[string]any{"enable": true, "up": 0, "down": 0})

		if result.Error != nil {
			return result.Error
		}

		// Update lastTrafficResetTime for the inbound(s)
		inboundWhereText := "id "
		if id == -1 {
			inboundWhereText += " > ?"
		} else {
			inboundWhereText += " = ?"
		}

		result = tx.Model(model.Inbound{}).
			Where(inboundWhereText, id).
			Update("last_traffic_reset_time", now)

		return result.Error
	}); err != nil {
		return err
	}

	// AWG / WG / MTProto clients live in their own tables — reset them too so the
	// "reset all client traffic" action covers every protocol.
	s.resetDedicatedClientTraffics(id)
	return nil
}

// resetDedicatedClientTraffics resets the upload/download counters of AWG / WG /
// MTProto clients for inbound id (id < 0 → all). xray clients are handled by the
// caller against client_traffics.
func (s *InboundService) resetDedicatedClientTraffics(id int) {
	if id < 0 {
		if err := (&AwgService{}).ResetAllClientTraffics(); err != nil {
			logger.Warning("reset AWG client traffics failed:", err)
		}
		if err := (&WgService{}).ResetAllClientTraffics(); err != nil {
			logger.Warning("reset WG client traffics failed:", err)
		}
		if err := (&MtprotoClientService{}).ResetAllClientTraffics(-1); err != nil {
			logger.Warning("reset MTProto client traffics failed:", err)
		}
		return
	}
	ib, err := s.GetInbound(id)
	if err != nil {
		return
	}
	switch ib.Protocol {
	case model.AmneziaWG:
		if err := (&AwgService{}).ResetAllClientTraffics(); err != nil {
			logger.Warning("reset AWG client traffics failed:", err)
		}
	case model.NativeWG:
		if err := (&WgService{}).ResetAllClientTraffics(); err != nil {
			logger.Warning("reset WG client traffics failed:", err)
		}
	case model.MTProto:
		if err := (&MtprotoClientService{}).ResetAllClientTraffics(id); err != nil {
			logger.Warning("reset MTProto client traffics failed:", err)
		}
	}
}

func (s *InboundService) ResetAllTraffics() error {
	db := database.GetDB()

	result := db.Model(model.Inbound{}).
		Where("user_id > ?", 0).
		Updates(map[string]any{"up": 0, "down": 0})

	err := result.Error
	return err
}

func (s *InboundService) ResetInboundTraffic(id int) error {
	db := database.GetDB()

	result := db.Model(model.Inbound{}).
		Where("id = ?", id).
		Updates(map[string]any{"up": 0, "down": 0})

	return result.Error
}

func (s *InboundService) DelDepletedClients(id int) (err error) {
	db := database.GetDB()
	tx := db.Begin()
	defer func() {
		if err == nil {
			tx.Commit()
		} else {
			tx.Rollback()
		}
	}()

	whereText := "reset = 0 and inbound_id "
	if id < 0 {
		whereText += "> ?"
	} else {
		whereText += "= ?"
	}

	// Only consider truly depleted clients: expired OR traffic exhausted
	now := time.Now().Unix() * 1000
	depletedClients := []xray.ClientTraffic{}
	err = db.Model(xray.ClientTraffic{}).
		Where(whereText+" and ((total > 0 and up + down >= total) or (expiry_time > 0 and expiry_time <= ?))", id, now).
		Select("inbound_id, GROUP_CONCAT(email) as email").
		Group("inbound_id").
		Find(&depletedClients).Error
	if err != nil {
		return err
	}

	for _, depletedClient := range depletedClients {
		emails := strings.Split(depletedClient.Email, ",")
		oldInbound, err := s.GetInbound(depletedClient.InboundId)
		if err != nil {
			return err
		}
		var oldSettings map[string]any
		err = json.Unmarshal([]byte(oldInbound.Settings), &oldSettings)
		if err != nil {
			return err
		}

		oldClients := clientsArray(oldSettings)
		var newClients []any
		for _, client := range oldClients {
			deplete := false
			c, ok := client.(map[string]any)
			if !ok {
				newClients = append(newClients, client)
				continue
			}
			clientEmail, _ := c["email"].(string)
			for _, email := range emails {
				if email == clientEmail {
					deplete = true
					break
				}
			}
			if !deplete {
				newClients = append(newClients, client)
			}
		}
		if len(newClients) > 0 {
			oldSettings["clients"] = newClients

			newSettings, err := json.MarshalIndent(oldSettings, "", "  ")
			if err != nil {
				return err
			}

			oldInbound.Settings = string(newSettings)
			err = tx.Save(oldInbound).Error
			if err != nil {
				return err
			}
		} else {
			// Delete inbound if no client remains
			s.DelInbound(depletedClient.InboundId)
		}
	}

	// Delete stats only for truly depleted clients
	err = tx.Where(whereText+" and ((total > 0 and up + down >= total) or (expiry_time > 0 and expiry_time <= ?))", id, now).Delete(xray.ClientTraffic{}).Error
	if err != nil {
		return err
	}

	return nil
}

// DelDedicatedDepletedClients deletes depleted AWG / WG / MTProto clients for
// inbound id (id < 0 → all). Run AFTER DelDepletedClients (xray) so the single
// SQLite connection is free — these do their own DB writes and OS-level config
// reapplies. xray clients are handled by DelDepletedClients itself.
func (s *InboundService) DelDedicatedDepletedClients(id int) {
	if id < 0 {
		if err := (&AwgService{}).DelDepletedClients(); err != nil {
			logger.Warning("del depleted AWG clients failed:", err)
		}
		if err := (&WgService{}).DelDepletedClients(); err != nil {
			logger.Warning("del depleted WG clients failed:", err)
		}
		if err := (&MtprotoClientService{}).DelDepletedClients(-1); err != nil {
			logger.Warning("del depleted MTProto clients failed:", err)
		}
		return
	}
	ib, err := s.GetInbound(id)
	if err != nil {
		return
	}
	switch ib.Protocol {
	case model.AmneziaWG:
		if err := (&AwgService{}).DelDepletedClients(); err != nil {
			logger.Warning("del depleted AWG clients failed:", err)
		}
	case model.NativeWG:
		if err := (&WgService{}).DelDepletedClients(); err != nil {
			logger.Warning("del depleted WG clients failed:", err)
		}
	case model.MTProto:
		if err := (&MtprotoClientService{}).DelDepletedClients(id); err != nil {
			logger.Warning("del depleted MTProto clients failed:", err)
		}
	}
}

func (s *InboundService) GetClientTrafficTgBot(tgId int64) ([]*xray.ClientTraffic, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound

	// Retrieve inbounds where settings contain the given tgId
	err := db.Model(model.Inbound{}).Where("settings LIKE ?", fmt.Sprintf(`%%"tgId": %d%%`, tgId)).Find(&inbounds).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		logger.Errorf("Error retrieving inbounds with tgId %d: %v", tgId, err)
		return nil, err
	}

	var emails []string
	for _, inbound := range inbounds {
		clients, err := s.GetClients(inbound)
		if err != nil {
			logger.Errorf("Error retrieving clients for inbound %d: %v", inbound.Id, err)
			continue
		}
		for _, client := range clients {
			if client.TgID == tgId {
				emails = append(emails, client.Email)
			}
		}
	}

	// Chunked to stay under SQLite's bind-variable limit when a single Telegram
	// account owns thousands of clients across inbounds.
	uniqEmails := uniqueNonEmptyStrings(emails)
	traffics := make([]*xray.ClientTraffic, 0, len(uniqEmails))
	for _, batch := range chunkStrings(uniqEmails, sqliteMaxVars) {
		var page []*xray.ClientTraffic
		if err = db.Model(xray.ClientTraffic{}).Where("email IN ?", batch).Find(&page).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				continue
			}
			logger.Errorf("Error retrieving ClientTraffic for emails %v: %v", batch, err)
			return nil, err
		}
		traffics = append(traffics, page...)
	}
	if len(traffics) == 0 {
		logger.Warning("No ClientTraffic records found for emails:", emails)
		return nil, nil
	}

	// Populate UUID and other client data for each traffic record
	for i := range traffics {
		if ct, client, e := s.GetClientByEmail(traffics[i].Email); e == nil && ct != nil && client != nil {
			traffics[i].Enable = client.Enable
			traffics[i].UUID = client.ID
			traffics[i].SubId = client.SubID
		}
	}

	return traffics, nil
}

// sqliteMaxVars is a safe ceiling for the number of bind parameters in a
// single SQL statement. SQLite's SQLITE_MAX_VARIABLE_NUMBER is 999 on builds
// before 3.32 and 32766 after; staying under 999 keeps queries portable
// across forks/old binaries and also bounds per-query memory on truly large
// installs (>32k clients) where even modern SQLite would refuse a single IN.
const sqliteMaxVars = 900

// uniqueNonEmptyStrings returns a deduplicated copy of in with empty strings
// removed, preserving the order of first occurrence.
func uniqueNonEmptyStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// uniqueInts returns a deduplicated copy of in, preserving order of first occurrence.
func uniqueInts(in []int) []int {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(in))
	out := make([]int, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// chunkStrings splits s into consecutive sub-slices of at most size elements.
// Returns nil for an empty input or non-positive size.
func chunkStrings(s []string, size int) [][]string {
	if size <= 0 || len(s) == 0 {
		return nil
	}
	out := make([][]string, 0, (len(s)+size-1)/size)
	for i := 0; i < len(s); i += size {
		end := i + size
		if end > len(s) {
			end = len(s)
		}
		out = append(out, s[i:end])
	}
	return out
}

// chunkInts splits s into consecutive sub-slices of at most size elements.
// Returns nil for an empty input or non-positive size.
func chunkInts(s []int, size int) [][]int {
	if size <= 0 || len(s) == 0 {
		return nil
	}
	out := make([][]int, 0, (len(s)+size-1)/size)
	for i := 0; i < len(s); i += size {
		end := i + size
		if end > len(s) {
			end = len(s)
		}
		out = append(out, s[i:end])
	}
	return out
}

// GetActiveClientTraffics returns the absolute ClientTraffic rows for the given
// emails. Used by the WebSocket delta path to push per-client absolute
// counters without re-serializing the full inbound list. The query is chunked
// to stay under SQLite's bind-variable limit on very large active sets.
// Empty input returns (nil, nil).
func (s *InboundService) GetActiveClientTraffics(emails []string) ([]*xray.ClientTraffic, error) {
	uniq := uniqueNonEmptyStrings(emails)
	if len(uniq) == 0 {
		return nil, nil
	}
	db := database.GetDB()
	traffics := make([]*xray.ClientTraffic, 0, len(uniq))
	for _, batch := range chunkStrings(uniq, sqliteMaxVars) {
		var page []*xray.ClientTraffic
		if err := db.Model(xray.ClientTraffic{}).Where("email IN ?", batch).Find(&page).Error; err != nil {
			return nil, err
		}
		traffics = append(traffics, page...)
	}
	return traffics, nil
}

// InboundTrafficSummary is the minimal projection of an inbound's traffic
// counters used by the WebSocket delta path. Excludes Settings/StreamSettings
// blobs so the broadcast stays compact even with many inbounds.
type InboundTrafficSummary struct {
	Id      int   `json:"id"`
	Up      int64 `json:"up"`
	Down    int64 `json:"down"`
	Total   int64 `json:"total"`
	AllTime int64 `json:"allTime"`
	Enable  bool  `json:"enable"`
}

// GetInboundsTrafficSummary returns inbound-level absolute traffic counters
// (no per-client expansion). Companion to GetActiveClientTraffics — together
// they replace the heavy "full inbound list" broadcast on each cron tick.
func (s *InboundService) GetInboundsTrafficSummary() ([]InboundTrafficSummary, error) {
	db := database.GetDB()
	var summaries []InboundTrafficSummary
	if err := db.Model(&model.Inbound{}).
		Select("id, up, down, total, all_time, enable").
		Find(&summaries).Error; err != nil {
		return nil, err
	}
	return summaries, nil
}

func (s *InboundService) GetClientTrafficByEmail(email string) (traffic *xray.ClientTraffic, err error) {
	// Prefer retrieving along with client to reflect actual enabled state from inbound settings
	t, client, err := s.GetClientByEmail(email)
	if err != nil {
		logger.Warningf("Error retrieving ClientTraffic with email %s: %v", email, err)
		return nil, err
	}
	if t != nil && client != nil {
		t.UUID = client.ID
		t.SubId = client.SubID
		return t, nil
	}
	return nil, nil
}

func (s *InboundService) UpdateClientTrafficByEmail(email string, upload int64, download int64) error {
	db := database.GetDB()

	// Keep all_time monotonic: it represents historical cumulative usage and
	// must never be less than the currently-tracked up+down. Without this,
	// the UI showed "Общий трафик" (allTime) below the live consumed value
	// after admins manually edited a client's counters.
	result := db.Model(xray.ClientTraffic{}).
		Where("email = ?", email).
		Updates(map[string]any{
			"up":       upload,
			"down":     download,
			"all_time": gorm.Expr("CASE WHEN COALESCE(all_time, 0) < ? THEN ? ELSE all_time END", upload+download, upload+download),
		})

	err := result.Error
	if err != nil {
		logger.Warningf("Error updating ClientTraffic with email %s: %v", email, err)
		return err
	}
	return nil
}

func (s *InboundService) GetClientTrafficByID(id string) ([]xray.ClientTraffic, error) {
	db := database.GetDB()
	var traffics []xray.ClientTraffic

	err := db.Model(xray.ClientTraffic{}).Where(`email IN(
		SELECT JSON_EXTRACT(client.value, '$.email') as email
		FROM inbounds,
	  	JSON_EACH(JSON_EXTRACT(inbounds.settings, '$.clients')) AS client
		WHERE
	  	JSON_EXTRACT(client.value, '$.id') in (?)
		)`, id).Find(&traffics).Error

	if err != nil {
		logger.Debug(err)
		return nil, err
	}
	// Reconcile enable flag with client settings per email to avoid stale DB value
	for i := range traffics {
		if ct, client, e := s.GetClientByEmail(traffics[i].Email); e == nil && ct != nil && client != nil {
			traffics[i].Enable = client.Enable
			traffics[i].UUID = client.ID
			traffics[i].SubId = client.SubID
		}
	}
	return traffics, err
}

func (s *InboundService) SearchClientTraffic(query string) (traffic *xray.ClientTraffic, err error) {
	db := database.GetDB()
	inbound := &model.Inbound{}
	traffic = &xray.ClientTraffic{}

	// Search for inbound settings that contain the query
	err = db.Model(model.Inbound{}).Where("settings LIKE ?", "%\""+query+"\"%").First(inbound).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			logger.Warningf("Inbound settings containing query %s not found: %v", query, err)
			return nil, err
		}
		logger.Errorf("Error searching for inbound settings with query %s: %v", query, err)
		return nil, err
	}

	traffic.InboundId = inbound.Id

	// Unmarshal settings to get clients
	settings := map[string][]model.Client{}
	if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
		logger.Errorf("Error unmarshalling inbound settings for inbound ID %d: %v", inbound.Id, err)
		return nil, err
	}

	clients := settings["clients"]
	for _, client := range clients {
		if (client.ID == query || client.Password == query) && client.Email != "" {
			traffic.Email = client.Email
			break
		}
	}

	if traffic.Email == "" {
		logger.Warningf("No client found with query %s in inbound ID %d", query, inbound.Id)
		return nil, gorm.ErrRecordNotFound
	}

	// Retrieve ClientTraffic based on the found email
	err = db.Model(xray.ClientTraffic{}).Where("email = ?", traffic.Email).First(traffic).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			logger.Warningf("ClientTraffic for email %s not found: %v", traffic.Email, err)
			return nil, err
		}
		logger.Errorf("Error retrieving ClientTraffic for email %s: %v", traffic.Email, err)
		return nil, err
	}

	return traffic, nil
}

func (s *InboundService) GetInboundClientIps(clientEmail string) (string, error) {
	db := database.GetDB()
	InboundClientIps := &model.InboundClientIps{}
	err := db.Model(model.InboundClientIps{}).Where("client_email = ?", clientEmail).First(InboundClientIps).Error
	if err != nil {
		return "", err
	}

	if InboundClientIps.Ips == "" {
		return "", nil
	}

	// Try to parse as new format (with timestamps)
	type IPWithTimestamp struct {
		IP        string `json:"ip"`
		Timestamp int64  `json:"timestamp"`
	}

	var ipsWithTime []IPWithTimestamp
	err = json.Unmarshal([]byte(InboundClientIps.Ips), &ipsWithTime)

	// If successfully parsed as new format, return with timestamps
	if err == nil && len(ipsWithTime) > 0 {
		return InboundClientIps.Ips, nil
	}

	// Otherwise, assume it's old format (simple string array)
	// Try to parse as simple array and convert to new format
	var oldIps []string
	err = json.Unmarshal([]byte(InboundClientIps.Ips), &oldIps)
	if err == nil && len(oldIps) > 0 {
		// Convert old format to new format with current timestamp
		newIpsWithTime := make([]IPWithTimestamp, len(oldIps))
		for i, ip := range oldIps {
			newIpsWithTime[i] = IPWithTimestamp{
				IP:        ip,
				Timestamp: time.Now().Unix(),
			}
		}
		result, _ := json.Marshal(newIpsWithTime)
		return string(result), nil
	}

	// Return as-is if parsing fails
	return InboundClientIps.Ips, nil
}

func (s *InboundService) ClearClientIps(clientEmail string) error {
	db := database.GetDB()

	result := db.Model(model.InboundClientIps{}).
		Where("client_email = ?", clientEmail).
		Update("ips", "")
	err := result.Error
	if err != nil {
		return err
	}
	return nil
}

func (s *InboundService) SearchInbounds(query string) ([]*model.Inbound, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).Preload("ClientStats").Where("remark like ?", "%"+query+"%").Find(&inbounds).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	return inbounds, nil
}

func (s *InboundService) MigrationRequirements() {
	db := database.GetDB()
	tx := db.Begin()
	var err error
	defer func() {
		if err == nil {
			tx.Commit()
			if dbErr := db.Exec(`VACUUM "main"`).Error; dbErr != nil {
				logger.Warningf("VACUUM failed: %v", dbErr)
			}
		} else {
			tx.Rollback()
		}
	}()

	// Calculate and backfill all_time from up+down for inbounds and clients
	err = tx.Exec(`
		UPDATE inbounds
		SET all_time = IFNULL(up, 0) + IFNULL(down, 0)
		WHERE IFNULL(all_time, 0) = 0 AND (IFNULL(up, 0) + IFNULL(down, 0)) > 0
	`).Error
	if err != nil {
		return
	}
	err = tx.Exec(`
		UPDATE client_traffics
		SET all_time = IFNULL(up, 0) + IFNULL(down, 0)
		WHERE IFNULL(all_time, 0) = 0 AND (IFNULL(up, 0) + IFNULL(down, 0)) > 0
	`).Error

	if err != nil {
		return
	}

	// Fix inbounds based problems
	var inbounds []*model.Inbound
	err = tx.Model(model.Inbound{}).Where("protocol IN (?)", []string{"vmess", "vless", "trojan"}).Find(&inbounds).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return
	}
	for inbound_index := range inbounds {
		settings := map[string]any{}
		json.Unmarshal([]byte(inbounds[inbound_index].Settings), &settings)
		clients, ok := settings["clients"].([]any)
		if ok {
			// Fix Client configuration problems
			var newClients []any
			for client_index := range clients {
				c, ok := clients[client_index].(map[string]any)
				if !ok {
					newClients = append(newClients, clients[client_index])
					continue
				}

				// Add email='' if it is not exists
				if _, ok := c["email"]; !ok {
					c["email"] = ""
				}

				// Convert string tgId to int64
				if _, ok := c["tgId"]; ok {
					var tgId any = c["tgId"]
					if tgIdStr, ok2 := tgId.(string); ok2 {
						tgIdInt64, err := strconv.ParseInt(strings.ReplaceAll(tgIdStr, " ", ""), 10, 64)
						if err == nil {
							c["tgId"] = tgIdInt64
						}
					}
				}

				// Remove "flow": "xtls-rprx-direct"
				if _, ok := c["flow"]; ok {
					if c["flow"] == "xtls-rprx-direct" {
						c["flow"] = ""
					}
				}
				// Backfill created_at and updated_at
				if _, ok := c["created_at"]; !ok {
					c["created_at"] = time.Now().Unix() * 1000
				}
				c["updated_at"] = time.Now().Unix() * 1000
				newClients = append(newClients, any(c))
			}
			settings["clients"] = newClients
			modifiedSettings, err := json.MarshalIndent(settings, "", "  ")
			if err != nil {
				return
			}

			inbounds[inbound_index].Settings = string(modifiedSettings)
		}

		// Add client traffic row for all clients which has email
		modelClients, err := s.GetClients(inbounds[inbound_index])
		if err != nil {
			return
		}
		for _, modelClient := range modelClients {
			if len(modelClient.Email) > 0 {
				var count int64
				tx.Model(xray.ClientTraffic{}).Where("email = ?", modelClient.Email).Count(&count)
				if count == 0 {
					s.AddClientStat(tx, inbounds[inbound_index].Id, &modelClient)
				}
			}
		}
	}
	tx.Save(inbounds)

	// Remove orphaned traffics
	tx.Where("inbound_id = 0").Delete(xray.ClientTraffic{})

	// Migrate old MultiDomain to External Proxy
	var externalProxy []struct {
		Id             int
		Port           int
		StreamSettings []byte
	}
	err = tx.Raw(`select id, port, stream_settings
	from inbounds
	WHERE protocol in ('vmess','vless','trojan')
	  AND json_extract(stream_settings, '$.security') = 'tls'
	  AND json_extract(stream_settings, '$.tlsSettings.settings.domains') IS NOT NULL`).Scan(&externalProxy).Error
	if err != nil || len(externalProxy) == 0 {
		return
	}

	for _, ep := range externalProxy {
		var reverses any
		var stream map[string]any
		json.Unmarshal(ep.StreamSettings, &stream)
		if tlsSettings, ok := stream["tlsSettings"].(map[string]any); ok {
			if settings, ok := tlsSettings["settings"].(map[string]any); ok {
				if domains, ok := settings["domains"].([]any); ok {
					for _, domain := range domains {
						if domainMap, ok := domain.(map[string]any); ok {
							dest, destOk := domainMap["domain"].(string)
							if !destOk {
								continue
							}
							domainMap["forceTls"] = "same"
							domainMap["port"] = ep.Port
							domainMap["dest"] = dest
							delete(domainMap, "domain")
						}
					}
				}
				reverses = settings["domains"]
				delete(settings, "domains")
			}
		}
		stream["externalProxy"] = reverses
		newStream, _ := json.MarshalIndent(stream, " ", "  ")
		tx.Model(model.Inbound{}).Where("id = ?", ep.Id).Update("stream_settings", newStream)
	}

	err = tx.Raw(`UPDATE inbounds
	SET tag = REPLACE(tag, '0.0.0.0:', '')
	WHERE INSTR(tag, '0.0.0.0:') > 0;`).Error
	if err != nil {
		return
	}
}

func (s *InboundService) MigrateDB() {
	s.MigrationRequirements()
	s.MigrationRemoveOrphanedTraffics()
}

// onlineWindow is how long after a client's last seen activity it still counts
// as "online". Shared by every protocol (xray here, plus AWG / WireGuard /
// MTProto) so online status is reported uniformly — a sticky "seen recently"
// window rather than xray's old per-traffic-tick "transferring right now".
const onlineWindow = 1 * time.Minute

// getXrayOnlineClients returns the emails of xray (vmess/vless/trojan/
// shadowsocks) clients seen within onlineWindow, read from the persisted
// last_online column. This replaces the in-memory per-tick list so xray
// protocols behave like AWG/WG/MTProto (online stays lit between bursts).
func (s *InboundService) getXrayOnlineClients() []string {
	db := database.GetDB()
	threshold := time.Now().Add(-onlineWindow).UnixMilli()
	var emails []string
	if err := db.Model(&xray.ClientTraffic{}).
		Where("enable = ? AND last_online > ?", true, threshold).
		Pluck("email", &emails).Error; err != nil {
		logger.Warning("get xray online clients failed:", err)
		return nil
	}
	return emails
}

func (s *InboundService) GetOnlineClients() []string {
	online := s.getXrayOnlineClients()
	// AWG / native WireGuard / MTProto clients track their own online state (by
	// uuid) in dedicated tables. Merge them here so the realtime traffic
	// broadcast and the /onlines endpoint report every protocol uniformly — the
	// frontend keys online off this one list, so these update live instead of
	// only on a full inbound refresh. uuids never collide with xray emails.
	online = append(online, (&AwgService{}).GetOnlineClients()...)
	online = append(online, (&WgService{}).GetOnlineClients()...)
	online = append(online, (&MtprotoClientService{}).GetOnlineClients()...)
	return online
}

// GetClientsLastOnlineFor returns last_online for the given emails only.
// The realtime broadcast uses this instead of GetClientsLastOnline: only
// clients with traffic in the current window can have a changed timestamp, and
// the frontend merges the map into what it already has (see inbounds.html), so
// a delta is enough. The full map stays available over REST (/lastOnline) for
// the initial page load.
func (s *InboundService) GetClientsLastOnlineFor(emails []string) (map[string]int64, error) {
	uniqEmails := uniqueNonEmptyStrings(emails)
	if len(uniqEmails) == 0 {
		return map[string]int64{}, nil
	}
	db := database.GetDB()
	result := make(map[string]int64, len(uniqEmails))
	for _, batch := range chunkStrings(uniqEmails, sqliteMaxVars) {
		var rows []xray.ClientTraffic
		err := db.Model(&xray.ClientTraffic{}).
			Select("email, last_online").
			Where("email IN ?", batch).
			Find(&rows).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return nil, err
		}
		for _, r := range rows {
			result[r.Email] = r.LastOnline
		}
	}
	return result, nil
}

func (s *InboundService) GetClientsLastOnline() (map[string]int64, error) {
	db := database.GetDB()
	var rows []xray.ClientTraffic
	err := db.Model(&xray.ClientTraffic{}).Select("email, last_online").Find(&rows).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	result := make(map[string]int64, len(rows))
	for _, r := range rows {
		result[r.Email] = r.LastOnline
	}
	return result, nil
}

func (s *InboundService) FilterAndSortClientEmails(emails []string) ([]string, []string, error) {
	db := database.GetDB()

	// Step 1: Get ClientTraffic records for emails in the input list.
	// Chunked to stay under SQLite's bind-variable limit on huge inputs.
	uniqEmails := uniqueNonEmptyStrings(emails)
	clients := make([]xray.ClientTraffic, 0, len(uniqEmails))
	for _, batch := range chunkStrings(uniqEmails, sqliteMaxVars) {
		var page []xray.ClientTraffic
		if err := db.Where("email IN ?", batch).Find(&page).Error; err != nil && err != gorm.ErrRecordNotFound {
			return nil, nil, err
		}
		clients = append(clients, page...)
	}

	// Step 2: Sort clients by (Up + Down) descending
	sort.Slice(clients, func(i, j int) bool {
		return (clients[i].Up + clients[i].Down) > (clients[j].Up + clients[j].Down)
	})

	// Step 3: Extract sorted valid emails and track found ones
	validEmails := make([]string, 0, len(clients))
	found := make(map[string]bool)
	for _, client := range clients {
		validEmails = append(validEmails, client.Email)
		found[client.Email] = true
	}

	// Step 4: Identify emails that were not found in the database
	extraEmails := make([]string, 0)
	for _, email := range emails {
		if !found[email] {
			extraEmails = append(extraEmails, email)
		}
	}

	return validEmails, extraEmails, nil
}
func (s *InboundService) DelInboundClientByEmail(inboundId int, email string) (bool, error) {
	oldInbound, err := s.GetInbound(inboundId)
	if err != nil {
		logger.Error("Load Old Data Error")
		return false, err
	}

	var settings map[string]any
	if err := json.Unmarshal([]byte(oldInbound.Settings), &settings); err != nil {
		return false, err
	}

	interfaceClients, ok := settings["clients"].([]any)
	if !ok {
		return false, common.NewError("invalid clients format in inbound settings")
	}

	var newClients []any
	needApiDel := false
	found := false

	for _, client := range interfaceClients {
		c, ok := client.(map[string]any)
		if !ok {
			continue
		}
		if cEmail, ok := c["email"].(string); ok && cEmail == email {
			// matched client, drop it
			found = true
			needApiDel, _ = c["enable"].(bool)
		} else {
			newClients = append(newClients, client)
		}
	}

	if !found {
		return false, common.NewError(fmt.Sprintf("client with email %s not found", email))
	}
	if len(newClients) == 0 {
		return false, common.NewError("no client remained in Inbound")
	}

	settings["clients"] = newClients
	newSettings, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}

	oldInbound.Settings = string(newSettings)

	db := database.GetDB()

	// remove IP bindings
	if err := s.DelClientIPs(db, email); err != nil {
		logger.Error("Error in delete client IPs")
		return false, err
	}

	needRestart := false

	// remove stats too
	if len(email) > 0 {
		traffic, err := s.GetClientTrafficByEmail(email)
		if err != nil {
			return false, err
		}
		if traffic != nil {
			if err := s.DelClientStat(db, email); err != nil {
				logger.Error("Delete stats Data Error")
				return false, err
			}
		}

		if needApiDel {
			s.xrayApi.Init(xrayAPIPort())
			if err1 := s.xrayApi.RemoveUser(oldInbound.Tag, email); err1 == nil {
				logger.Debug("Client deleted by api:", email)
				needRestart = false
			} else {
				if strings.Contains(err1.Error(), fmt.Sprintf("User %s not found.", email)) {
					logger.Debug("User is already deleted. Nothing to do more...")
				} else {
					logger.Debug("Error in deleting client by api:", err1)
					needRestart = true
				}
			}
			s.xrayApi.Close()
		}
	}

	return needRestart, db.Save(oldInbound).Error
}
