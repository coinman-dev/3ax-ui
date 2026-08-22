// Package datagen carries a process-wide generation counter for inbound data.
//
// It exists so that caches outside the service layer (notably the subscription
// server) can be invalidated the moment an inbound or one of its clients
// changes, without those packages importing web/service — which would be an
// import cycle, since web/service is what performs the mutations.
package datagen

import "sync/atomic"

var generation atomic.Uint64

// Bump marks inbound data as changed. Called from every path that writes an
// inbound or a client row.
func Bump() {
	generation.Add(1)
}

// Current returns the current generation. A cached value stays valid only while
// this number is unchanged.
func Current() uint64 {
	return generation.Load()
}
