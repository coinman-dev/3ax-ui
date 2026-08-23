package sub

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/shared/datagen"
)

func resetInboundsCache() {
	inboundsCacheMu.Lock()
	inboundsCache = make(map[string]inboundsCacheEntry)
	inboundsCacheMu.Unlock()
}

// TestInboundsCacheIsolation verifies the cache hands out copies. Callers
// rewrite Listen/Port/StreamSettings when resolving a fallback master, and that
// must not leak into what the next visitor is served.
func TestInboundsCacheIsolation(t *testing.T) {
	resetInboundsCache()
	original := []*model.Inbound{{Id: 1, Listen: "127.0.0.1", Port: 443, Remark: "a"}}
	storeInboundsBySubId("sub-a", original)

	first, ok := cachedInboundsBySubId("sub-a")
	if !ok {
		t.Fatal("entry was not cached")
	}
	first[0].Listen = "10.0.0.1"
	first[0].Port = 8443

	second, ok := cachedInboundsBySubId("sub-a")
	if !ok {
		t.Fatal("entry disappeared from the cache")
	}
	if second[0].Listen != "127.0.0.1" || second[0].Port != 443 {
		t.Fatalf("cached entry was mutated through a returned copy: %+v", second[0])
	}
	// Mutating the caller's original slice must not change the cache either.
	original[0].Remark = "changed"
	if third, _ := cachedInboundsBySubId("sub-a"); third[0].Remark != "a" {
		t.Fatalf("cache aliased the caller's slice: remark=%q", third[0].Remark)
	}
}

// TestInboundsCacheInvalidation covers both invalidation paths: a data change
// anywhere in the panel (generation bump) and the TTL.
func TestInboundsCacheInvalidation(t *testing.T) {
	resetInboundsCache()
	storeInboundsBySubId("sub-b", []*model.Inbound{{Id: 2}})

	if _, ok := cachedInboundsBySubId("sub-b"); !ok {
		t.Fatal("fresh entry should be served from the cache")
	}
	datagen.Bump()
	if _, ok := cachedInboundsBySubId("sub-b"); ok {
		t.Fatal("entry survived a data change")
	}

	old := inboundsCacheTTL
	inboundsCacheTTL = 20 * time.Millisecond
	defer func() { inboundsCacheTTL = old }()

	storeInboundsBySubId("sub-c", []*model.Inbound{{Id: 3}})
	if _, ok := cachedInboundsBySubId("sub-c"); !ok {
		t.Fatal("entry should be valid right after storing")
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok := cachedInboundsBySubId("sub-c"); ok {
		t.Fatal("entry survived its TTL")
	}
}

// TestGetInboundsBySubIdUsesCacheAndSeesEdits runs the real lookup against a
// database: the second call must not hit the JSON_EACH scan, and an edit must
// be visible immediately afterwards (the DB write bumps the generation).
func TestGetInboundsBySubIdUsesCacheAndSeesEdits(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	resetInboundsCache()
	db := database.GetDB()

	ib := &model.Inbound{
		UserId: 1, Remark: "vless", Enable: true, Port: 43000,
		Protocol: "vless", Tag: "inbound-43000",
		Settings: `{"clients":[{"id":"11111111-1111-1111-1111-111111111111","email":"a@b","subId":"sub-x","enable":true}]}`,
	}
	if err := db.Create(ib).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}

	svc := NewSubService(false, "", "")
	got, err := svc.getInboundsBySubId("sub-x")
	if err != nil {
		t.Fatalf("getInboundsBySubId: %v", err)
	}
	if len(got) != 1 || got[0].Remark != "vless" {
		t.Fatalf("unexpected lookup result: %+v", got)
	}

	inboundsCacheMu.Lock()
	_, cached := inboundsCache["sub-x"]
	inboundsCacheMu.Unlock()
	if !cached {
		t.Fatal("lookup result was not cached")
	}

	// An edit through the normal DB path must invalidate it.
	if err := db.Model(&model.Inbound{}).Where("id = ?", ib.Id).
		Update("remark", "renamed").Error; err != nil {
		t.Fatalf("update inbound: %v", err)
	}
	got, err = svc.getInboundsBySubId("sub-x")
	if err != nil {
		t.Fatalf("getInboundsBySubId after edit: %v", err)
	}
	if len(got) != 1 || got[0].Remark != "renamed" {
		t.Fatalf("stale data served after an edit: %+v", got[0])
	}
}
