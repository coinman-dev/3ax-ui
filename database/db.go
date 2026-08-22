// Package database provides database initialization, migration, and management utilities
// for the 3AX-UI panel using GORM with SQLite.
package database

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/coinman-dev/3ax-ui/v2/config"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
	xuilogger "github.com/coinman-dev/3ax-ui/v2/logger"
	"github.com/coinman-dev/3ax-ui/v2/shared/datagen"
	"github.com/coinman-dev/3ax-ui/v2/util/crypto"
	"github.com/coinman-dev/3ax-ui/v2/xray"
	"github.com/google/uuid"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var db *gorm.DB

const (
	defaultUsername = "admin"
	defaultPassword = "admin"
)

func initModels() error {
	models := []any{
		&model.User{},
		&model.Inbound{},
		&model.OutboundTraffics{},
		&model.Setting{},
		&model.InboundClientIps{},
		&xray.ClientTraffic{},
		&model.HistoryOfSeeders{},
		&model.AwgServer{},
		&model.AwgClient{},
		&model.WgServer{},
		&model.WgClient{},
		&model.MtprotoClient{},
		&model.TunnelServer{},
		&model.TunnelClient{},
		&model.CustomGeoResource{},
	}
	for _, model := range models {
		if err := db.AutoMigrate(model); err != nil {
			log.Printf("Error auto migrating model: %v", err)
			return err
		}
	}
	return nil
}

// initUser creates a default admin user if the users table is empty.
func initUser() error {
	empty, err := isTableEmpty("users")
	if err != nil {
		log.Printf("Error checking if users table is empty: %v", err)
		return err
	}
	if empty {
		hashedPassword, err := crypto.HashPasswordAsBcrypt(defaultPassword)

		if err != nil {
			log.Printf("Error hashing default password: %v", err)
			return err
		}

		user := &model.User{
			Username: defaultUsername,
			Password: hashedPassword,
		}
		return db.Create(user).Error
	}
	return nil
}

// runSeeders migrates user passwords to bcrypt and records seeder execution to prevent re-running.
func runSeeders(isUsersEmpty bool) error {
	empty, err := isTableEmpty("history_of_seeders")
	if err != nil {
		log.Printf("Error checking if users table is empty: %v", err)
		return err
	}

	if empty && isUsersEmpty {
		hashSeeder := &model.HistoryOfSeeders{
			SeederName: "UserPasswordHash",
		}
		return db.Create(hashSeeder).Error
	} else {
		var seedersHistory []string
		db.Model(&model.HistoryOfSeeders{}).Pluck("seeder_name", &seedersHistory)

		if !slices.Contains(seedersHistory, "UserPasswordHash") && !isUsersEmpty {
			var users []model.User
			db.Find(&users)

			for _, user := range users {
				hashedPassword, err := crypto.HashPasswordAsBcrypt(user.Password)
				if err != nil {
					log.Printf("Error hashing password for user '%s': %v", user.Username, err)
					return err
				}
				db.Model(&user).Update("password", hashedPassword)
			}

			hashSeeder := &model.HistoryOfSeeders{
				SeederName: "UserPasswordHash",
			}
			return db.Create(hashSeeder).Error
		}
	}

	return nil
}

// isTableEmpty returns true if the named table contains zero rows.
func isTableEmpty(tableName string) (bool, error) {
	var count int64
	err := db.Table(tableName).Count(&count).Error
	return count == 0, err
}

// InitDB sets up the database connection, migrates models, and runs seeders.
// inboundDataTables are the tables whose contents shape what the subscription
// endpoints render. Traffic counters (client_traffics) are deliberately absent:
// they are rewritten on every collection tick, and treating that as a change
// would invalidate the subscription cache constantly for numbers that its TTL
// already keeps fresh enough.
var inboundDataTables = map[string]struct{}{
	"inbounds":        {},
	"mtproto_clients": {},
	"awg_clients":     {},
	"wg_clients":      {},
}

// registerDataGenerationHooks bumps the shared data generation after any write
// to an inbound-shaping table, wherever it comes from. Doing it here rather
// than in each service method means a future write path cannot forget to
// invalidate the caches that depend on it.
func registerDataGenerationHooks(db *gorm.DB) {
	bump := func(tx *gorm.DB) {
		if tx.Statement == nil {
			return
		}
		if _, ok := inboundDataTables[tx.Statement.Table]; ok {
			datagen.Bump()
		}
	}
	bumpRaw := func(tx *gorm.DB) {
		if tx.Statement == nil {
			return
		}
		sql := strings.TrimSpace(tx.Statement.SQL.String())
		if len(sql) < 6 {
			return
		}
		switch strings.ToUpper(sql[:6]) {
		case "UPDATE", "INSERT", "DELETE":
		default:
			return
		}
		lower := strings.ToLower(sql)
		for table := range inboundDataTables {
			if strings.Contains(lower, table) {
				datagen.Bump()
				return
			}
		}
	}
	for name, cb := range map[string]func(*gorm.DB){
		"datagen:create": bump,
		"datagen:update": bump,
		"datagen:delete": bump,
	} {
		var err error
		switch name {
		case "datagen:create":
			err = db.Callback().Create().After("gorm:create").Register(name, cb)
		case "datagen:update":
			err = db.Callback().Update().After("gorm:update").Register(name, cb)
		case "datagen:delete":
			err = db.Callback().Delete().After("gorm:delete").Register(name, cb)
		}
		if err != nil {
			xuilogger.Warning("could not register data generation hook:", err)
		}
	}
	if err := db.Callback().Raw().After("gorm:raw").Register("datagen:raw", bumpRaw); err != nil {
		xuilogger.Warning("could not register raw data generation hook:", err)
	}
}

func InitDB(dbPath string) error {
	dir := path.Dir(dbPath)
	err := os.MkdirAll(dir, fs.ModePerm)
	if err != nil {
		return err
	}

	var gormLogger logger.Interface

	if config.IsDebug() {
		gormLogger = logger.Default
	} else {
		gormLogger = logger.Discard
	}

	c := &gorm.Config{
		Logger: gormLogger,
	}
	dsn := dbPath
	separator := "?"
	if strings.Contains(dbPath, "?") {
		separator = "&"
	}
	dsn = fmt.Sprintf("%s%s_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=ON", dbPath, separator)

	db, err = gorm.Open(sqlite.Open(dsn), c)
	if err != nil {
		return err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	registerDataGenerationHooks(db)

	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(0)

	if err := db.Exec("PRAGMA journal_mode=WAL;").Error; err != nil {
		return err
	}
	if err := db.Exec("PRAGMA busy_timeout = 5000;").Error; err != nil {
		return err
	}
	if err := db.Exec("PRAGMA synchronous = NORMAL;").Error; err != nil {
		return err
	}
	if err := db.Exec("PRAGMA foreign_keys = ON;").Error; err != nil {
		return err
	}

	// Add the AmneziaWG 2.0 / split-DNS columns ourselves BEFORE AutoMigrate to
	// avoid a GORM SQLite "duplicate column name" failure when it adds columns
	// while also rebuilding awg_servers for the H1-H4 int→string type change.
	preMigrateAwgWgColumns()

	if err := initModels(); err != nil {
		return err
	}

	// Populate UUIDs for existing AWG clients that don't have one
	migrateAwgClientUUIDs()

	// One-time move of the four legacy tunnel tables into the merged
	// tunnel_servers/tunnel_clients (see migrateTunnelTablesFromLegacy).
	migrateTunnelTablesFromLegacy(db)

	// Convert legacy mixed/http inbounds from settings.accounts[] to settings.clients[]
	// so they share the rich per-user infrastructure (traffic, expiry, quota) with VLESS.
	migrateMixedHttpAccounts()

	// Split the legacy combined AWG/WG `dns` column into dns_ipv4 / dns_ipv6 so
	// an upgrade preserves the installed DNS instead of reverting to defaults.
	migrateAwgWgDnsSplit()

	// Move MTProto clients out of inbound.settings (legacy single-secret and the
	// interim settings.clients[] shape) into the dedicated mtproto_clients table
	// (unique Uuid, non-unique Email), carrying any recorded traffic across.
	migrateMtprotoClientsTable()

	isUsersEmpty, err := isTableEmpty("users")
	if err != nil {
		return err
	}

	if err := initUser(); err != nil {
		return err
	}
	if err := runSeeders(isUsersEmpty); err != nil {
		return err
	}
	// One-time: seed the lifetime-traffic baseline for AWG/WG clients now that
	// upload/download accumulate (instead of holding the bounce-resettable kernel
	// counter). Runs after runSeeders so it can't disturb its empty-table check.
	migrateAwgWgPeerBaseline()
	return nil
}

// migrateAwgWgPeerBaseline runs once after AWG/WG upload/download became lifetime
// accumulators. Previously they stored the kernel's per-peer counter, which
// resets on an interface bounce — so any traffic before a bounce survived only in
// all_time, leaving the per-client traffic column showing 0. For each existing
// client it (1) records the current upload/download as the raw-peer baseline so
// the next poll computes a correct delta, and (2) folds the traffic that survived
// only in all_time back into download so the column reflects real lifetime usage
// immediately. Guarded by a seeder record: re-running after upload/download have
// diverged from the raw counter would corrupt the baseline.
func migrateAwgWgPeerBaseline() {
	var seeders []string
	db.Model(&model.HistoryOfSeeders{}).Pluck("seeder_name", &seeders)
	if slices.Contains(seeders, "AwgWgPeerBaseline") {
		return
	}

	seedTable := func(table string) {
		if !tableExists(table) {
			return
		}
		type row struct {
			Id       int
			Upload   int64
			Download int64
			AllTime  int64
		}
		var rows []row
		db.Table(table).Select("id, upload, download, all_time").Scan(&rows)
		for _, r := range rows {
			updates := map[string]any{
				"last_peer_up":   r.Upload,
				"last_peer_down": r.Download,
			}
			// Fold the all_time-only remainder back into download (lifetime totals
			// are combined in all_time, so the split can't be recovered exactly;
			// download dominates for VPN clients).
			if lost := r.AllTime - r.Upload - r.Download; lost > 0 {
				updates["download"] = r.Download + lost
			}
			db.Table(table).Where("id = ?", r.Id).Updates(updates)
		}
	}
	seedTable("awg_clients")
	seedTable("wg_clients")

	if err := db.Create(&model.HistoryOfSeeders{SeederName: "AwgWgPeerBaseline"}).Error; err != nil {
		log.Printf("migrateAwgWgPeerBaseline: record seeder failed: %v", err)
	}
}

// migrateAwgClientUUIDs populates UUIDs for existing AWG clients that have empty UUIDs.
func migrateAwgClientUUIDs() {
	var clients []model.AwgClient
	db.Where("uuid = '' OR uuid IS NULL").Find(&clients)
	for _, client := range clients {
		client.UUID = uuid.New().String()
		db.Model(&client).Update("uuid", client.UUID)
	}
}

// migrateAwgWgDnsSplit moves the legacy combined `dns` column of awg_servers /
// wg_servers into the per-family dns_ipv4 / dns_ipv6 columns, so an upgrade
// preserves the installed DNS instead of reverting to defaults. Idempotent: the
// old `dns` value is cleared once split, so it runs only on the first start
// after the upgrade (and is skipped on fresh installs where the column is gone).
func migrateAwgWgDnsSplit() {
	for _, table := range []string{"awg_servers", "wg_servers"} {
		if !legacyDnsColumnExists(table) {
			continue
		}
		type dnsRow struct {
			Id  int
			Dns string
		}
		var rows []dnsRow
		db.Raw(fmt.Sprintf("SELECT id, dns FROM %s WHERE dns IS NOT NULL AND dns != ''", table)).Scan(&rows)
		for _, r := range rows {
			v4, v6 := splitDnsByFamily(r.Dns)
			if err := db.Exec(
				fmt.Sprintf("UPDATE %s SET dns_ipv4 = ?, dns_ipv6 = ?, dns = '' WHERE id = ?", table),
				v4, v6, r.Id,
			).Error; err != nil {
				log.Printf("migrateAwgWgDnsSplit: update %s id=%d failed: %v", table, r.Id, err)
			}
		}
	}
}

// legacyDnsColumnExists reports whether the table still has the old `dns` column.
func legacyDnsColumnExists(table string) bool {
	return columnExists(table, "dns")
}

// tableExists reports whether a table exists in the database.
func tableExists(table string) bool {
	var n int64
	db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&n)
	return n > 0
}

// columnExists reports whether the given table has the given column.
func columnExists(table, col string) bool {
	var n int64
	db.Raw(fmt.Sprintf("SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name = ?", table), col).Scan(&n)
	return n > 0
}

// preMigrateAwgWgColumns adds the AmneziaWG 2.0 / split-DNS columns to the
// existing awg_servers / wg_servers tables BEFORE GORM AutoMigrate runs. Doing
// the adds ourselves (idempotently) avoids a GORM SQLite quirk: adding columns
// while it also rebuilds awg_servers for the H1-H4 int→string type change can
// fail with "duplicate column name". No-op on fresh installs (the tables don't
// exist yet — AutoMigrate then creates them with the full schema).
func preMigrateAwgWgColumns() {
	add := func(table, col, ddl string) {
		if !tableExists(table) || columnExists(table, col) {
			return
		}
		if err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, col, ddl)).Error; err != nil {
			log.Printf("preMigrateAwgWgColumns: add %s.%s failed: %v", table, col, err)
		}
	}
	for _, t := range []string{"awg_servers", "wg_servers"} {
		add(t, "dns_ipv4", "text DEFAULT '1.1.1.1'")
		add(t, "dns_ipv6", "text DEFAULT '2606:4700:4700::1111'")
	}
	// AmneziaWG-only obfuscation columns (native WireGuard has no obfuscation).
	add("awg_servers", "s3", "integer DEFAULT 0")
	add("awg_servers", "s4", "integer DEFAULT 0")
	add("awg_servers", "i1", "text DEFAULT ''")
}

// splitDnsByFamily splits a comma-separated DNS list into IPv4 and IPv6 groups
// (IPv6 entries are detected by the ':' character).
func splitDnsByFamily(combined string) (ipv4, ipv6 string) {
	var v4, v6 []string
	for _, p := range strings.Split(combined, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.Contains(p, ":") {
			v6 = append(v6, p)
		} else {
			v4 = append(v4, p)
		}
	}
	return strings.Join(v4, ","), strings.Join(v6, ",")
}

// migrateMixedHttpAccounts rewrites legacy mixed/http inbound settings:
// the old shape stored basic-auth users as settings.accounts[]={user,pass};
// the new shape stores them as settings.clients[]={email,password,...} so the
// shared VLESS-style CRUD, traffic, expiry, and quota machinery applies.
// Idempotent — inbounds already in the new shape are skipped.
func migrateMixedHttpAccounts() {
	var inbounds []model.Inbound
	if err := db.Where("protocol IN ?", []string{"mixed", "http"}).Find(&inbounds).Error; err != nil {
		log.Printf("migrateMixedHttpAccounts: scan inbounds failed: %v", err)
		return
	}
	for i := range inbounds {
		inbound := &inbounds[i]
		var settings map[string]any
		if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
			log.Printf("migrateMixedHttpAccounts: inbound %d settings unmarshal failed: %v", inbound.Id, err)
			continue
		}
		if _, hasClients := settings["clients"]; hasClients {
			// Already migrated.
			continue
		}
		rawAccounts, hasAccounts := settings["accounts"]
		if !hasAccounts {
			continue
		}
		accounts, ok := rawAccounts.([]any)
		if !ok || len(accounts) == 0 {
			delete(settings, "accounts")
			if newSettings, err := json.MarshalIndent(settings, "", "  "); err == nil {
				db.Model(inbound).Update("settings", string(newSettings))
			}
			continue
		}

		nowMs := time.Now().UnixMilli()
		clients := make([]map[string]any, 0, len(accounts))
		for _, a := range accounts {
			am, ok := a.(map[string]any)
			if !ok {
				continue
			}
			user, _ := am["user"].(string)
			pass, _ := am["pass"].(string)
			if user == "" {
				continue
			}
			clients = append(clients, map[string]any{
				"email":      user,
				"password":   pass,
				"limitIp":    0,
				"totalGB":    0,
				"expiryTime": 0,
				"enable":     true,
				"tgId":       0,
				"subId":      "",
				"comment":    "",
				"reset":      0,
				"created_at": nowMs,
				"updated_at": nowMs,
			})
		}

		settings["clients"] = clients
		delete(settings, "accounts")

		newSettings, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			log.Printf("migrateMixedHttpAccounts: inbound %d marshal failed: %v", inbound.Id, err)
			continue
		}
		if err := db.Model(inbound).Update("settings", string(newSettings)).Error; err != nil {
			log.Printf("migrateMixedHttpAccounts: inbound %d save failed: %v", inbound.Id, err)
			continue
		}
		log.Printf("migrateMixedHttpAccounts: migrated %d %s account(s) in inbound %d", len(clients), inbound.Protocol, inbound.Id)
	}
}

// migrateMtprotoClientsTable moves MTProto clients out of inbound.settings and
// into the dedicated mtproto_clients table. It handles both shapes seen in the
// wild: the legacy single-secret inbound (settings.secret + fakeTlsDomain) and
// the interim multi-user inbound (settings.clients[]). Each client becomes a row
// with a fresh unique Uuid and a free-form (now NON-unique) Email; any traffic
// recorded against the old per-email client_traffics row is carried over, and
// those rows are then dropped (traffic now lives in mtproto_clients). Idempotent:
// an inbound that already has rows in mtproto_clients is skipped, and an inbound
// with no clients to move is left untouched. Scoped strictly to mtproto inbounds.
func migrateMtprotoClientsTable() {
	if !tableExists("mtproto_clients") {
		return
	}
	var inbounds []model.Inbound
	if err := db.Where("protocol = ?", model.MTProto).Find(&inbounds).Error; err != nil {
		log.Printf("migrateMtprotoClientsTable: scan inbounds failed: %v", err)
		return
	}
	for i := range inbounds {
		ib := &inbounds[i]
		var existing int64
		db.Model(&model.MtprotoClient{}).Where("inbound_id = ?", ib.Id).Count(&existing)
		if existing > 0 {
			continue // already migrated
		}

		// Tolerant parse: the interim settings were written verbatim from the
		// browser, where some numeric fields (notably tgId) are stored as strings.
		domain, legacySecret, parsedSeeds := model.ParseMtprotoSettingsClients(ib.Settings)

		type seed struct {
			email      string
			secret     string
			enable     bool
			limitIp    int
			totalGB    int64
			expiryTime int64
			tgId       int64
			subId      string
			comment    string
			reset      int
		}
		var seeds []seed
		if len(parsedSeeds) > 0 {
			for _, c := range parsedSeeds {
				if strings.TrimSpace(c.Secret) == "" && strings.TrimSpace(c.Email) == "" {
					continue
				}
				seeds = append(seeds, seed{
					email: c.Email, secret: c.Secret, enable: c.Enable, limitIp: c.LimitIp,
					totalGB: c.TotalGB, expiryTime: c.ExpiryTime, tgId: c.TgId,
					subId: c.SubId, comment: c.Comment, reset: c.Reset,
				})
			}
		} else if legacySecret != "" {
			email := strings.TrimSpace(ib.Remark)
			if email == "" {
				email = fmt.Sprintf("mtproto-%d", ib.Port)
			}
			seeds = append(seeds, seed{email: email, secret: legacySecret, enable: true})
		}
		if len(seeds) == 0 {
			if strings.TrimSpace(ib.Settings) != "" && domain == "" && legacySecret == "" {
				log.Printf("migrateMtprotoClientsTable: inbound %d has no parseable clients/secret — skipped (settings may be malformed)", ib.Id)
			}
			continue // nothing to migrate
		}

		nowMs := time.Now().UnixMilli()
		// Carry each old per-email client_traffics row's totals onto the first row
		// with that email only, so a (post-refactor) same-email pair can't double it.
		carried := make(map[string]bool)
		for _, sd := range seeds {
			row := model.MtprotoClient{
				InboundId:  ib.Id,
				Uuid:       uuid.New().String(),
				Email:      sd.email,
				Enable:     sd.enable,
				Secret:     model.HealMtprotoClientSecret(sd.secret, domain),
				Comment:    sd.comment,
				SubId:      sd.subId,
				TotalGB:    sd.totalGB,
				ExpiryTime: sd.expiryTime,
				Reset:      sd.reset,
				LimitIp:    sd.limitIp,
				TgId:       sd.tgId,
				CreatedAt:  nowMs,
				UpdatedAt:  nowMs,
			}
			// Carry over traffic recorded against the old per-email client_traffics
			// row, once per email (legacy emails were unique, but be defensive).
			if sd.email != "" && !carried[sd.email] {
				var ct xray.ClientTraffic
				if err := db.Where("email = ?", sd.email).First(&ct).Error; err == nil {
					row.Upload, row.Download, row.AllTime = ct.Up, ct.Down, ct.AllTime
					if row.AllTime == 0 {
						row.AllTime = ct.Up + ct.Down
					}
					row.LastOnline = ct.LastOnline
					carried[sd.email] = true
				}
			}
			if err := db.Create(&row).Error; err != nil {
				log.Printf("migrateMtprotoClientsTable: inbound %d create client %q failed: %v", ib.Id, sd.email, err)
			}
		}

		// Strip the moved client list / legacy secret from settings (inbound-level
		// config such as fakeTlsDomain / routeThroughXray is preserved).
		var settings map[string]any
		if err := json.Unmarshal([]byte(ib.Settings), &settings); err == nil {
			delete(settings, "clients")
			delete(settings, "secret")
			if out, err := json.MarshalIndent(settings, "", "  "); err == nil {
				db.Model(ib).Update("settings", string(out))
			}
		}
		// Drop the per-client client_traffics rows owned by this mtproto inbound.
		if err := db.Where("inbound_id = ?", ib.Id).Delete(&xray.ClientTraffic{}).Error; err != nil {
			log.Printf("migrateMtprotoClientsTable: inbound %d client_traffics cleanup failed: %v", ib.Id, err)
		}
		log.Printf("migrateMtprotoClientsTable: moved %d client(s) for inbound %d into mtproto_clients", len(seeds), ib.Id)
	}
}

// CloseDB closes the database connection if it exists.
func CloseDB() error {
	if db != nil {
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		return sqlDB.Close()
	}
	return nil
}

// GetDB returns the global GORM database instance.
func GetDB() *gorm.DB {
	return db
}

func IsNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

// IsSQLiteDB checks if the given file is a valid SQLite database by reading its signature.
func IsSQLiteDB(file io.ReaderAt) (bool, error) {
	signature := []byte("SQLite format 3\x00")
	buf := make([]byte, len(signature))
	_, err := file.ReadAt(buf, 0)
	if err != nil {
		return false, err
	}
	return bytes.Equal(buf, signature), nil
}

// Checkpoint performs a WAL checkpoint on the SQLite database to ensure data consistency.
func Checkpoint() error {
	// Update WAL
	err := db.Exec("PRAGMA wal_checkpoint;").Error
	if err != nil {
		return err
	}
	return nil
}

// ValidateSQLiteDB opens the provided sqlite DB path with a throw-away connection
// and runs a PRAGMA integrity_check to ensure the file is structurally sound.
// It does not mutate global state or run migrations.
func ValidateSQLiteDB(dbPath string) error {
	if _, err := os.Stat(dbPath); err != nil { // file must exist
		return err
	}
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return err
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	var res string
	if err := gdb.Raw("PRAGMA integrity_check;").Scan(&res).Error; err != nil {
		return err
	}
	if res != "ok" {
		return errors.New("sqlite integrity check failed: " + res)
	}
	return nil
}
