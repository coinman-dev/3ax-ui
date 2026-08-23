package mtproto

import (
	"sync"
	"testing"
	"time"
)

// TestManagerConcurrentOperations drives the manager the way the panel does:
// the 10s reconcile job, ad-hoc Ensure/Remove from HTTP handlers and traffic
// scrapes, all at once. It guards the two-lock split (opMu for process work,
// mu for in-memory state) against deadlocks and races — with the previous
// single lock held across exec, every one of these blocked on the others.
//
// No mtg binary exists in the test environment, so Start() fails and nothing is
// spawned; the lifecycle paths (config write, map commit, teardown) still run.
func TestManagerConcurrentOperations(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())

	m := &Manager{procs: map[int]*managed{}}
	m.mu.Lock()
	m.swept = true // skip the one-time orphan sweep: it shells out
	m.mu.Unlock()

	inst := func(id int) Instance {
		return Instance{
			Id:      id,
			Tag:     "inbound-test",
			Listen:  "127.0.0.1",
			Port:    4000 + id,
			Clients: []ClientSecret{{Id: "uuid", Secret: "ee00", Email: "a@b"}},
		}
	}

	done := make(chan struct{})
	var wg sync.WaitGroup

	worker := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					fn()
				}
			}
		}()
	}

	worker(func() { m.Reconcile([]Instance{inst(1), inst(2), inst(3)}) })
	worker(func() { _ = m.Ensure(inst(2)) })
	worker(func() { m.Remove(3) })
	worker(func() { m.CollectTraffic() })
	worker(func() {
		m.mu.Lock()
		_ = len(m.procs)
		m.mu.Unlock()
	})

	time.Sleep(300 * time.Millisecond)
	close(done)

	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("manager operations deadlocked")
	}

	m.StopAll()
	m.mu.Lock()
	left := len(m.procs)
	m.mu.Unlock()
	if left != 0 {
		t.Fatalf("StopAll left %d entries behind", left)
	}
}
