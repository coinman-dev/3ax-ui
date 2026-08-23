package sub

import (
	"sync"
	"time"

	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/shared/datagen"
	"github.com/coinman-dev/3ax-ui/v2/xray"
)

// The subscription endpoints are public and unauthenticated, and resolving a
// sub id costs a JSON_EACH scan over the settings blob of every inbound plus a
// preload of their client traffic rows. Without a cache each request repeated
// that work, so anyone could turn a loop of HTTP requests into steady database
// load on the panel's single SQLite connection.
//
// Entries expire on a short TTL and are dropped immediately whenever inbound
// data changes (datagen), so a subscription can never serve a stale config for
// longer than the TTL after an edit.
const inboundsCacheMaxEntries = 1024

// var, not const, so tests can shorten it.
var inboundsCacheTTL = 10 * time.Second

type inboundsCacheEntry struct {
	inbounds   []*model.Inbound
	generation uint64
	expiresAt  time.Time
}

var (
	inboundsCacheMu sync.Mutex
	inboundsCache   = make(map[string]inboundsCacheEntry)
)

// cachedInboundsBySubId returns a private copy of the cached lookup, if it is
// still valid. Callers mutate the inbounds they get back (fallback masters
// rewrite Listen/Port/StreamSettings), so a copy is mandatory.
func cachedInboundsBySubId(subId string) ([]*model.Inbound, bool) {
	now := time.Now()
	gen := datagen.Current()

	inboundsCacheMu.Lock()
	defer inboundsCacheMu.Unlock()

	entry, ok := inboundsCache[subId]
	if !ok {
		return nil, false
	}
	if entry.generation != gen || now.After(entry.expiresAt) {
		delete(inboundsCache, subId)
		return nil, false
	}
	return cloneInbounds(entry.inbounds), true
}

// storeInboundsBySubId caches a lookup result, keeping its own copy.
func storeInboundsBySubId(subId string, inbounds []*model.Inbound) {
	entry := inboundsCacheEntry{
		inbounds:   cloneInbounds(inbounds),
		generation: datagen.Current(),
		expiresAt:  time.Now().Add(inboundsCacheTTL),
	}

	inboundsCacheMu.Lock()
	defer inboundsCacheMu.Unlock()

	// Bounded by construction: a flood of unknown sub ids cannot grow the map
	// without limit. Expired entries go first; if everything is fresh the cache
	// is simply reset.
	if len(inboundsCache) >= inboundsCacheMaxEntries {
		now := time.Now()
		for key, e := range inboundsCache {
			if now.After(e.expiresAt) {
				delete(inboundsCache, key)
			}
		}
		if len(inboundsCache) >= inboundsCacheMaxEntries {
			inboundsCache = make(map[string]inboundsCacheEntry)
		}
	}
	inboundsCache[subId] = entry
}

// cloneInbounds deep-copies the parts callers may modify. Inbound holds only
// value fields plus ClientStats, whose elements are values as well, so copying
// the struct and the slice is enough.
func cloneInbounds(src []*model.Inbound) []*model.Inbound {
	out := make([]*model.Inbound, 0, len(src))
	for _, ib := range src {
		if ib == nil {
			continue
		}
		clone := *ib
		if ib.ClientStats != nil {
			clone.ClientStats = make([]xray.ClientTraffic, len(ib.ClientStats))
			copy(clone.ClientStats, ib.ClientStats)
		}
		out = append(out, &clone)
	}
	return out
}
