package model

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// MtprotoClient is one user of an MTProto (mtg / mtg-multi) proxy, stored in its
// own dedicated table — exactly like AwgClient / WgClient. Decoupling the client
// list from the inbound's settings JSON gives MTProto a unique-UID identity
// (Uuid) and a free-form, NON-unique Email label, so several clients of the same
// inbound may share a name.
//
// Uuid is the stable identity: it is the mtg-multi [secrets] entry name (quoted
// in the generated TOML), the key under which mtg-multi's /stats reports the
// user's traffic, and the value used in the panel's /panel/api/mtproto routes.
type MtprotoClient struct {
	Id        int    `json:"id" gorm:"primaryKey;autoIncrement"`
	InboundId int    `json:"inboundId" gorm:"index"`
	Uuid      string `json:"uuid" gorm:"index"` // unique UID (enforced in the service)
	Email     string `json:"email"`             // free-form label — NOT unique
	// No gorm default: a default:true tag would make Create() silently drop an
	// explicit Enable=false (its zero value), so a disabled client (e.g. one
	// migrated in disabled) would be written back as enabled. Callers always set
	// Enable; the UI's client form defaults it to true for new clients.
	Enable  bool   `json:"enable" gorm:"index:idx_mtproto_enable_last_online,priority:1"`
	Secret  string `json:"secret"` // FakeTLS hex secret ("ee"+middle+hex(domain))
	Comment string `json:"comment"`
	SubId   string `json:"subId"`

	// Traffic stats. Upload/Download are the resettable counters compared against
	// TotalGB; AllTime is the lifetime total preserved across a traffic reset.
	Upload   int64 `json:"upload" gorm:"default:0"`
	Download int64 `json:"download" gorm:"default:0"`
	AllTime  int64 `json:"allTime" gorm:"default:0"`
	TotalGB  int64 `json:"totalGB" gorm:"default:0"` // limit in bytes (0 = unlimited)

	ExpiryTime int64 `json:"expiryTime" gorm:"default:0"` // 0 = never
	Reset      int   `json:"reset" gorm:"default:0"`      // auto-renew interval in days
	LimitIp    int   `json:"limitIp" gorm:"default:0"`
	TgId       int64 `json:"tgId" gorm:"default:0"`
	LastOnline int64 `json:"lastOnline" gorm:"default:0;index:idx_mtproto_enable_last_online,priority:2"` // last_seen from /stats (ms)

	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime:milli"`
	UpdatedAt int64 `json:"updatedAt" gorm:"autoUpdateTime:milli"`
}

// GenerateFakeTLSSecret builds an MTProto FakeTLS secret for the given domain:
// the "ee" FakeTLS marker, 16 random bytes, then the domain encoded as hex.
// This single value is what mtg's config and the client tg:// link both use.
func GenerateFakeTLSSecret(domain string) string {
	return "ee" + mtprotoRandomMiddle() + hex.EncodeToString([]byte(domain))
}

func mtprotoRandomMiddle() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Errorf("mtproto: crypto/rand read failed: %w", err))
	}
	return hex.EncodeToString(buf)
}

// mtprotoSecretMiddle returns the 16-byte random middle of an existing secret
// when it is well-formed, otherwise a freshly generated one. Reusing the middle
// keeps the secret stable when only the FakeTLS domain changes.
func mtprotoSecretMiddle(secret string) string {
	s := secret
	if strings.HasPrefix(s, "ee") || strings.HasPrefix(s, "dd") {
		s = s[2:]
	}
	if len(s) >= 32 {
		mid := s[:32]
		if _, err := hex.DecodeString(mid); err == nil {
			return mid
		}
	}
	return mtprotoRandomMiddle()
}

// HealMtprotoClientSecret rebuilds a client's FakeTLS secret so its trailing
// domain matches `domain`, reusing the random middle when the existing secret is
// well-formed (so an unchanged client keeps a stable link). An empty domain
// leaves the secret untouched.
func HealMtprotoClientSecret(secret, domain string) string {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return secret
	}
	return "ee" + mtprotoSecretMiddle(secret) + hex.EncodeToString([]byte(domain))
}

// MtprotoFakeTLSDomain extracts the inbound's fronting domain from its settings.
func MtprotoFakeTLSDomain(settings string) string {
	var parsed struct {
		FakeTlsDomain string `json:"fakeTlsDomain"`
	}
	_ = json.Unmarshal([]byte(settings), &parsed)
	return strings.TrimSpace(parsed.FakeTlsDomain)
}

// MtprotoClientSeed is a client parsed out of an inbound's settings.clients[]
// (the inbound create form, or the interim settings storage being migrated). The
// numeric fields are read tolerantly because the panel's JS stores some of them
// as strings — notably tgId defaults to "" — and the interim settings were
// persisted verbatim from the browser, so a strict int64 decode would fail.
type MtprotoClientSeed struct {
	Email      string
	Secret     string
	Enable     bool
	HasEnable  bool // whether the "enable" key was present (else default true)
	LimitIp    int
	TotalGB    int64
	ExpiryTime int64
	TgId       int64
	SubId      string
	Comment    string
	Reset      int
}

// ParseMtprotoSettingsClients reads an mtproto inbound's settings tolerantly,
// returning the fronting domain, the legacy top-level single-secret (empty for
// the multi-user shape), and the client seeds from settings.clients[].
func ParseMtprotoSettingsClients(settings string) (domain, legacySecret string, seeds []MtprotoClientSeed) {
	var raw struct {
		FakeTlsDomain string           `json:"fakeTlsDomain"`
		Secret        string           `json:"secret"`
		Clients       []map[string]any `json:"clients"`
	}
	if err := json.Unmarshal([]byte(settings), &raw); err != nil {
		return "", "", nil
	}
	domain = strings.TrimSpace(raw.FakeTlsDomain)
	legacySecret = strings.TrimSpace(raw.Secret)
	for _, c := range raw.Clients {
		_, hasEnable := c["enable"]
		seeds = append(seeds, MtprotoClientSeed{
			Email:      jsonAsString(c["email"]),
			Secret:     jsonAsString(c["secret"]),
			Enable:     jsonAsBool(c["enable"]),
			HasEnable:  hasEnable,
			LimitIp:    int(jsonAsInt64(c["limitIp"])),
			TotalGB:    jsonAsInt64(c["totalGB"]),
			ExpiryTime: jsonAsInt64(c["expiryTime"]),
			TgId:       jsonAsInt64(c["tgId"]),
			SubId:      jsonAsString(c["subId"]),
			Comment:    jsonAsString(c["comment"]),
			Reset:      int(jsonAsInt64(c["reset"])),
		})
	}
	return domain, legacySecret, seeds
}

// jsonAsString returns v as a string when it is one, else "".
func jsonAsString(v any) string {
	s, _ := v.(string)
	return s
}

// jsonAsBool returns v as a bool when it is one, else false.
func jsonAsBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// jsonAsInt64 coerces a JSON value (number, numeric string, "" or null) to int64.
func jsonAsInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		i, _ := t.Int64()
		return i
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0
		}
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(f)
		}
	}
	return 0
}
