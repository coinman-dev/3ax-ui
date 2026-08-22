package database

import (
	"reflect"

	"github.com/coinman-dev/3ax-ui/v2/database/model"
	xuilogger "github.com/coinman-dev/3ax-ui/v2/logger"

	"gorm.io/gorm"
)

// migrateTunnelTablesFromLegacy copies the four legacy tunnel tables
// (awg_servers, awg_clients, wg_servers, wg_clients) into the merged
// tunnel_servers/tunnel_clients — once, on the first start after the upgrade.
//
// It must not run afterwards: the services now read and write the merged tables,
// so a second pass would overwrite live data with whatever the frozen legacy
// tables still hold. Hence the guard on an already-populated destination.
//
// The legacy tables are left untouched as the rollback path for one release: an
// older binary keeps working off them, losing only what changed after the
// upgrade.
//
// Rows are matched by their stable identity (Kind for servers, UUID for
// clients) so re-running against a partially migrated database is safe.
func migrateTunnelTablesFromLegacy(db *gorm.DB) {
	var migrated int64
	if err := db.Model(&model.TunnelServer{}).Count(&migrated).Error; err != nil {
		xuilogger.Warning("tunnel migration: count tunnel_servers:", err)
		return
	}
	if migrated > 0 {
		return // already migrated; the merged tables are authoritative now
	}
	syncTunnelTablesFromLegacy(db)
}

// syncTunnelTablesFromLegacy does the actual copy. Split out so the migration
// guard stays readable and the copy itself remains testable on its own.
func syncTunnelTablesFromLegacy(db *gorm.DB) {
	awgServers, wgServers := []model.AwgServer{}, []model.WgServer{}
	awgClients, wgClients := []model.AwgClient{}, []model.WgClient{}

	if err := db.Find(&awgServers).Error; err != nil {
		xuilogger.Warning("tunnel sync: read awg_servers:", err)
		return
	}
	if err := db.Find(&wgServers).Error; err != nil {
		xuilogger.Warning("tunnel sync: read wg_servers:", err)
		return
	}
	if err := db.Find(&awgClients).Error; err != nil {
		xuilogger.Warning("tunnel sync: read awg_clients:", err)
		return
	}
	if err := db.Find(&wgClients).Error; err != nil {
		xuilogger.Warning("tunnel sync: read wg_clients:", err)
		return
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		serverIdByKind := map[string]int{}

		for _, src := range []struct {
			kind string
			row  any
			ok   bool
		}{
			{model.TunnelKindAwg, firstOf(awgServers), len(awgServers) > 0},
			{model.TunnelKindWg, firstOf(wgServers), len(wgServers) > 0},
		} {
			if !src.ok {
				// No server of this flavour any more: drop its merged rows.
				if err := deleteTunnelKind(tx, src.kind); err != nil {
					return err
				}
				continue
			}
			id, err := upsertTunnelServer(tx, src.kind, src.row)
			if err != nil {
				return err
			}
			serverIdByKind[src.kind] = id
		}

		for _, src := range []struct {
			kind    string
			clients []any
		}{
			{model.TunnelKindAwg, anySlice(awgClients)},
			{model.TunnelKindWg, anySlice(wgClients)},
		} {
			serverId, ok := serverIdByKind[src.kind]
			if !ok {
				continue
			}
			if err := syncTunnelClients(tx, serverId, src.clients); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		xuilogger.Warning("tunnel sync failed:", err)
	}
}

// upsertTunnelServer writes the legacy server row into tunnel_servers under the
// given kind and returns the merged row's id.
func upsertTunnelServer(tx *gorm.DB, kind string, legacy any) (int, error) {
	var existing model.TunnelServer
	err := tx.Where("kind = ?", kind).First(&existing).Error

	row := model.TunnelServer{}
	copyByFieldName(&row, legacy)
	row.Kind = kind

	switch {
	case err == nil:
		row.Id = existing.Id
		if err := tx.Model(&model.TunnelServer{}).Where("id = ?", existing.Id).Save(&row).Error; err != nil {
			return 0, err
		}
		return existing.Id, nil
	case err == gorm.ErrRecordNotFound:
		row.Id = 0
		if err := tx.Create(&row).Error; err != nil {
			return 0, err
		}
		return row.Id, nil
	default:
		return 0, err
	}
}

// syncTunnelClients makes tunnel_clients for one server match the legacy rows:
// existing UUIDs are updated in place, new ones inserted, vanished ones removed.
func syncTunnelClients(tx *gorm.DB, serverId int, legacy []any) error {
	var current []model.TunnelClient
	if err := tx.Where("server_id = ?", serverId).Find(&current).Error; err != nil {
		return err
	}
	idByUUID := make(map[string]int, len(current))
	for _, c := range current {
		idByUUID[c.UUID] = c.Id
	}

	seen := make(map[string]struct{}, len(legacy))
	for _, src := range legacy {
		row := model.TunnelClient{}
		copyByFieldName(&row, src)
		row.ServerId = serverId
		seen[row.UUID] = struct{}{}

		if id, ok := idByUUID[row.UUID]; ok {
			row.Id = id
			if err := tx.Model(&model.TunnelClient{}).Where("id = ?", id).Save(&row).Error; err != nil {
				return err
			}
			continue
		}
		row.Id = 0
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
	}

	for uuid, id := range idByUUID {
		if _, ok := seen[uuid]; ok {
			continue
		}
		if err := tx.Delete(&model.TunnelClient{}, id).Error; err != nil {
			return err
		}
	}
	return nil
}

func deleteTunnelKind(tx *gorm.DB, kind string) error {
	var servers []model.TunnelServer
	if err := tx.Where("kind = ?", kind).Find(&servers).Error; err != nil {
		return err
	}
	for _, s := range servers {
		if err := tx.Where("server_id = ?", s.Id).Delete(&model.TunnelClient{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&model.TunnelServer{}, s.Id).Error; err != nil {
			return err
		}
	}
	return nil
}

// copyByFieldName copies same-named, same-typed fields between structs. The
// legacy and merged models share every field name, so this cannot silently drop
// data — the migration tests assert full coverage.
func copyByFieldName(dst, src any) {
	d := reflect.ValueOf(dst).Elem()
	s := reflect.ValueOf(src)
	if s.Kind() == reflect.Pointer {
		s = s.Elem()
	}
	st := s.Type()
	for i := range st.NumField() {
		df := d.FieldByName(st.Field(i).Name)
		if !df.IsValid() || !df.CanSet() || df.Type() != s.Field(i).Type() {
			continue
		}
		df.Set(s.Field(i))
	}
}

func firstOf[T any](rows []T) any {
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

func anySlice[T any](rows []T) []any {
	out := make([]any, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i])
	}
	return out
}
