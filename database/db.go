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

	if err := initModels(); err != nil {
		return err
	}

	// Populate UUIDs for existing AWG clients that don't have one
	migrateAwgClientUUIDs()

	// Convert legacy mixed/http inbounds from settings.accounts[] to settings.clients[]
	// so they share the rich per-user infrastructure (traffic, expiry, quota) with VLESS.
	migrateMixedHttpAccounts()

	isUsersEmpty, err := isTableEmpty("users")
	if err != nil {
		return err
	}

	if err := initUser(); err != nil {
		return err
	}
	return runSeeders(isUsersEmpty)
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

// IsNotFound checks if the given error is a GORM record not found error.
func IsNotFound(err error) bool {
	return err == gorm.ErrRecordNotFound
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
