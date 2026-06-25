package service

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/logger"
	"github.com/coinman-dev/3ax-ui/v2/mtproto"
)

// MtprotoClientService owns the dedicated mtproto_clients table — the per-user
// store for MTProto proxies, mirroring AwgService/WgService. Each client has a
// unique Uuid (the mtg-multi [secrets]/stats key) and a free-form, NON-unique
// Email label, so several clients of one inbound may share a name. The mtg / mtg-
// multi sidecar itself is still driven by the mtproto package + reconcile job;
// this service is only the client list + traffic store + enforcement.
type MtprotoClientService struct{}

// getInbound loads an mtproto inbound by id.
func (s *MtprotoClientService) getInbound(inboundId int) (*model.Inbound, error) {
	db := database.GetDB()
	var ib model.Inbound
	if err := db.Where("id = ? AND protocol = ?", inboundId, model.MTProto).First(&ib).Error; err != nil {
		return nil, err
	}
	return &ib, nil
}

// GetVersion returns the installed mtg / mtg-multi sidecar version (e.g.
// "v1.11.0"), shown next to the protocol in the inbound details.
func (s *MtprotoClientService) GetVersion() string {
	return mtproto.GetVersion()
}

// GetClients returns every mtproto client across all inbounds (ascending id).
func (s *MtprotoClientService) GetClients() ([]model.MtprotoClient, error) {
	db := database.GetDB()
	var clients []model.MtprotoClient
	if err := db.Order("id asc").Find(&clients).Error; err != nil {
		return nil, err
	}
	return clients, nil
}

// GetClientsByInbound returns the clients of a single inbound (ascending id).
func (s *MtprotoClientService) GetClientsByInbound(inboundId int) ([]model.MtprotoClient, error) {
	db := database.GetDB()
	var clients []model.MtprotoClient
	if err := db.Where("inbound_id = ?", inboundId).Order("id asc").Find(&clients).Error; err != nil {
		return nil, err
	}
	return clients, nil
}

// GetClientByUuid returns a single client by its Uuid.
func (s *MtprotoClientService) GetClientByUuid(clientUUID string) (*model.MtprotoClient, error) {
	db := database.GetDB()
	var client model.MtprotoClient
	if err := db.Where("uuid = ?", clientUUID).First(&client).Error; err != nil {
		return nil, err
	}
	return &client, nil
}

// AddClient creates a client for an mtproto inbound: it allocates a Uuid (unless
// one was supplied), derives the FakeTLS secret from the inbound's fronting
// domain, persists the row, then reconciles the sidecar so the secret goes live.
// Email is accepted as-is (duplicates allowed).
func (s *MtprotoClientService) AddClient(client *model.MtprotoClient) error {
	ib, err := s.getInbound(client.InboundId)
	if err != nil {
		return err
	}

	if client.Uuid == "" {
		client.Uuid = uuid.New().String()
	}
	if _, err := uuid.Parse(client.Uuid); err != nil {
		return fmt.Errorf("invalid UUID format for MTProto client: %s", client.Uuid)
	}

	db := database.GetDB()
	var count int64
	db.Model(&model.MtprotoClient{}).Where("uuid = ?", client.Uuid).Count(&count)
	if count > 0 {
		return errors.New("a client with this UUID already exists")
	}

	domain := model.MtprotoFakeTLSDomain(ib.Settings)
	client.Secret = model.HealMtprotoClientSecret(client.Secret, domain)
	now := time.Now().UnixMilli()
	client.CreatedAt = now
	client.UpdatedAt = now

	if err := db.Create(client).Error; err != nil {
		return err
	}
	s.reconcileInbound(ib)
	return nil
}

// UpdateClientByUuid updates the editable fields of a client located by Uuid.
// Identity (Id/Uuid/InboundId), traffic counters and creation time are preserved
// from the stored row; the secret is re-healed against the inbound domain so a
// domain change is picked up while the random middle (and thus link) stays stable.
func (s *MtprotoClientService) UpdateClientByUuid(clientUUID string, client *model.MtprotoClient) error {
	existing, err := s.GetClientByUuid(clientUUID)
	if err != nil {
		return err
	}
	ib, err := s.getInbound(existing.InboundId)
	if err != nil {
		return err
	}
	domain := model.MtprotoFakeTLSDomain(ib.Settings)

	client.Id = existing.Id
	client.Uuid = existing.Uuid
	client.InboundId = existing.InboundId
	client.Upload = existing.Upload
	client.Download = existing.Download
	client.AllTime = existing.AllTime
	client.LastOnline = existing.LastOnline
	client.CreatedAt = existing.CreatedAt
	client.Secret = model.HealMtprotoClientSecret(existing.Secret, domain)
	client.UpdatedAt = time.Now().UnixMilli()

	if err := database.GetDB().Save(client).Error; err != nil {
		return err
	}
	s.reconcileInbound(ib)
	return nil
}

// DeleteClientByUuid removes a client and reconciles its inbound.
func (s *MtprotoClientService) DeleteClientByUuid(clientUUID string) error {
	existing, err := s.GetClientByUuid(clientUUID)
	if err != nil {
		return err
	}
	if err := database.GetDB().Where("uuid = ?", clientUUID).Delete(&model.MtprotoClient{}).Error; err != nil {
		return err
	}
	if ib, err := s.getInbound(existing.InboundId); err == nil {
		s.reconcileInbound(ib)
	} else {
		mtproto.GetManager().Remove(existing.InboundId)
	}
	return nil
}

// ToggleClientByUuid enables or disables a client and reconciles its inbound.
func (s *MtprotoClientService) ToggleClientByUuid(clientUUID string, enable bool) error {
	existing, err := s.GetClientByUuid(clientUUID)
	if err != nil {
		return err
	}
	if err := database.GetDB().Model(&model.MtprotoClient{}).
		Where("uuid = ?", clientUUID).
		Updates(map[string]any{"enable": enable, "updated_at": time.Now().UnixMilli()}).Error; err != nil {
		return err
	}
	if ib, err := s.getInbound(existing.InboundId); err == nil {
		s.reconcileInbound(ib)
	}
	return nil
}

// ResetClientTrafficByUuid zeroes the resettable upload/download counters of a
// client (the lifetime AllTime is preserved) and reconciles its inbound.
func (s *MtprotoClientService) ResetClientTrafficByUuid(clientUUID string) error {
	existing, err := s.GetClientByUuid(clientUUID)
	if err != nil {
		return err
	}
	if err := database.GetDB().Model(&model.MtprotoClient{}).
		Where("uuid = ?", clientUUID).
		Updates(map[string]any{"upload": 0, "download": 0}).Error; err != nil {
		return err
	}
	if ib, err := s.getInbound(existing.InboundId); err == nil {
		s.reconcileInbound(ib)
	}
	return nil
}

// DeleteByInbound removes all clients of an inbound (used when the inbound itself
// is deleted). The caller stops the sidecar separately.
func (s *MtprotoClientService) DeleteByInbound(inboundId int) error {
	return database.GetDB().Where("inbound_id = ?", inboundId).Delete(&model.MtprotoClient{}).Error
}

// ResetAllClientTraffics zeroes the upload/download counters of an inbound's
// clients (inboundId < 0 → every mtproto client). AllTime is preserved.
func (s *MtprotoClientService) ResetAllClientTraffics(inboundId int) error {
	q := database.GetDB().Model(&model.MtprotoClient{})
	if inboundId >= 0 {
		q = q.Where("inbound_id = ?", inboundId)
	}
	return q.Updates(map[string]any{"upload": 0, "download": 0}).Error
}

// DelDepletedClients deletes non-renewing (reset = 0) clients that are over their
// traffic quota or past their expiry (inboundId < 0 → across all mtproto
// inbounds), then reconciles each affected sidecar.
func (s *MtprotoClientService) DelDepletedClients(inboundId int) error {
	db := database.GetDB()
	now := time.Now().UnixMilli()
	q := db.Where("reset = 0 AND ((total_gb > 0 AND upload + download >= total_gb) OR (expiry_time > 0 AND expiry_time <= ?))", now)
	if inboundId >= 0 {
		q = q.Where("inbound_id = ?", inboundId)
	}
	var depleted []model.MtprotoClient
	if err := q.Find(&depleted).Error; err != nil {
		return err
	}
	affected := map[int]bool{}
	for _, c := range depleted {
		if err := db.Where("uuid = ?", c.Uuid).Delete(&model.MtprotoClient{}).Error; err != nil {
			logger.Warning("mtproto DelDepletedClients: delete", c.Email, "failed:", err)
			continue
		}
		affected[c.InboundId] = true
	}
	for ibID := range affected {
		s.Reconcile(ibID)
	}
	return nil
}

// RehealInbound rebuilds every client's FakeTLS secret for an inbound against the
// given fronting domain — called when the inbound's fakeTlsDomain changes so the
// per-client links track the new domain. Returns true if any secret changed.
func (s *MtprotoClientService) RehealInbound(inboundId int, domain string) bool {
	db := database.GetDB()
	var clients []model.MtprotoClient
	if err := db.Where("inbound_id = ?", inboundId).Find(&clients).Error; err != nil {
		return false
	}
	changed := false
	for _, c := range clients {
		healed := model.HealMtprotoClientSecret(c.Secret, domain)
		if healed != c.Secret {
			db.Model(&model.MtprotoClient{}).Where("id = ?", c.Id).Update("secret", healed)
			changed = true
		}
	}
	return changed
}

// GetOnlineClients returns the Uuids of clients seen with a live connection
// within onlineWindow.
func (s *MtprotoClientService) GetOnlineClients() []string {
	db := database.GetDB()
	threshold := time.Now().Add(-onlineWindow).UnixMilli()
	var uuids []string
	db.Model(&model.MtprotoClient{}).
		Where("enable = ? AND last_online > ?", true, threshold).
		Pluck("uuid", &uuids)
	return uuids
}

// RecordTraffic folds the per-client deltas scraped from the sidecars into the
// table (Upload/Download accumulate, AllTime is the lifetime total, LastOnline
// tracks the most recent activity), then enforces quota/expiry and auto-renew.
// Returns true when enforcement changed the active client set (the caller should
// reconcile so the sidecar picks the change up immediately).
func (s *MtprotoClientService) RecordTraffic(deltas []mtproto.Traffic) bool {
	db := database.GetDB()
	now := time.Now().UnixMilli()
	for _, d := range deltas {
		if d.Uuid == "" {
			continue
		}
		updates := map[string]any{}
		if d.Up > 0 || d.Down > 0 {
			updates["upload"] = gorm.Expr("upload + ?", d.Up)
			updates["download"] = gorm.Expr("download + ?", d.Down)
			updates["all_time"] = gorm.Expr("all_time + ?", d.Up+d.Down)
		}
		lastOnline := d.LastSeen
		if (d.Up > 0 || d.Down > 0) && lastOnline < now {
			lastOnline = now
		}
		if lastOnline > 0 {
			updates["last_online"] = lastOnline
		}
		if len(updates) > 0 {
			db.Model(&model.MtprotoClient{}).Where("uuid = ?", d.Uuid).Updates(updates)
		}
	}
	// Renew first (re-enable + zero traffic for expired-but-renewable clients),
	// then enforce quota/expiry on the resulting state — same order as AWG.
	changed := s.autoRenewClients(db)
	if s.disableInvalidClients(db) {
		changed = true
	}
	return changed
}

// disableInvalidClients disables clients past their traffic quota or expiry.
func (s *MtprotoClientService) disableInvalidClients(db *gorm.DB) bool {
	now := time.Now().UnixMilli()
	result := db.Model(&model.MtprotoClient{}).
		Where("enable = ? AND ((total_gb > 0 AND upload + download >= total_gb) OR (expiry_time > 0 AND expiry_time <= ?))", true, now).
		Update("enable", false)
	if result.Error != nil {
		logger.Warning("mtproto disableInvalidClients error:", result.Error)
		return false
	}
	if result.RowsAffected > 0 {
		logger.Infof("mtproto: %d client(s) disabled (quota/expiry)", result.RowsAffected)
	}
	return result.RowsAffected > 0
}

// autoRenewClients extends expiry for clients with reset > 0 whose expiry passed,
// zeroing their traffic and re-enabling them. Returns true if any was re-enabled.
func (s *MtprotoClientService) autoRenewClients(db *gorm.DB) bool {
	now := time.Now().UnixMilli()
	var clients []model.MtprotoClient
	if err := db.Where("reset > 0 AND expiry_time > 0 AND expiry_time <= ?", now).Find(&clients).Error; err != nil {
		logger.Warning("mtproto autoRenewClients error:", err)
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
		db.Model(&model.MtprotoClient{}).Where("id = ?", c.Id).Updates(updates)
		logger.Infof("mtproto: client '%s' auto-renewed, new expiry: %v", c.Email, time.UnixMilli(newExpiry))
	}
	return needReconfig
}

// Reconcile loads an inbound by id and (re)starts/stops its sidecar to match the
// current client set — used by the inbound update path for immediate effect. A
// missing inbound (deleted) stops and forgets the sidecar.
func (s *MtprotoClientService) Reconcile(inboundId int) {
	ib, err := s.getInbound(inboundId)
	if err != nil {
		mtproto.GetManager().Remove(inboundId)
		return
	}
	s.reconcileInbound(ib)
}

// reconcileInbound (re)starts or stops the sidecar for an inbound to match its
// current enabled, non-expired client set — giving client edits immediate effect
// instead of waiting for the next job tick.
func (s *MtprotoClientService) reconcileInbound(ib *model.Inbound) {
	if ib == nil {
		return
	}
	clients, err := s.GetClientsByInbound(ib.Id)
	if err != nil {
		logger.Warning("mtproto: load clients for reconcile failed:", err)
		return
	}
	if ib.Enable {
		if inst, ok := mtproto.InstanceFromInbound(ib, clients); ok {
			if err := mtproto.GetManager().Ensure(inst); err != nil {
				logger.Warning("mtproto: ensure inbound failed:", err)
			}
			return
		}
	}
	mtproto.GetManager().Remove(ib.Id)
}
