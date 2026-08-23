package sub

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/coinman-dev/3ax-ui/v2/database"
)

// TestGetSubsKeepsSharedServiceImmutable guards the fix for the shared-state
// bug: SUBController keeps a single SubService for every request, so GetSubs
// must not write the caller's Host onto the shared receiver. When it did, two
// concurrent requests raced and one visitor could be served links built from
// the other visitor's host.
func TestGetSubsKeepsSharedServiceImmutable(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	svc := NewSubService(false, "", "")

	var wg sync.WaitGroup
	for _, host := range []string{"host-a.example.com", "host-b.example.com", "10.0.0.1"} {
		wg.Add(1)
		go func(h string) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				// No inbounds exist, so this returns an error — irrelevant here:
				// what matters is that the shared instance is left untouched.
				_, _, _, _ = svc.GetSubs("some-sub-id", h)
			}
		}(host)
	}
	wg.Wait()

	if svc.address != "" {
		t.Errorf("shared SubService.address = %q, want it untouched", svc.address)
	}
	if svc.datepicker != "" {
		t.Errorf("shared SubService.datepicker = %q, want it untouched", svc.datepicker)
	}
}
