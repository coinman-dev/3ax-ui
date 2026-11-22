package service

import (
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgkern"
)

// The overview's online history counted only Xray users, so connected kernel-mode
// AmneziaWG clients never showed up in it.
func TestOnlineHistoryCountsKernelAmneziaWGClients(t *testing.T) {
	old := systemMetrics
	systemMetrics = newMetricHistory()
	t.Cleanup(func() { systemMetrics = old })

	const iface = "awg-metric-test"
	collector := amneziawgkern.GetCollector()
	t.Cleanup(func() { collector.Forget(iface) })
	now := time.Now()
	collector.SetEmails(iface, map[string]string{"peer-a": "laptop@example.test", "peer-b": "phone@example.test"})
	collector.ObserveHandshakes(iface, []amneziawgkern.PeerStat{
		{PublicKey: "peer-a", LatestHandshake: now.Add(-time.Minute).Unix()},
		{PublicKey: "peer-b", LatestHandshake: now.Add(-10 * time.Minute).Unix()},
	})

	svc := &ServerService{lastStatus: &Status{}}
	svc.AppendStatusSample(now, &Status{T: now})
	points := svc.AggregateSystemMetric("online", 2, 60)
	if len(points) != 1 || points[0]["v"] != float64(1) {
		t.Fatalf("online history = %v, want one point counting the client with a fresh handshake", points)
	}
}
