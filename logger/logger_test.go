package logger

import (
	"fmt"
	"sync"
	"testing"

	"github.com/op/go-logging"
)

// TestBufferConcurrentAccess exercises the in-memory log ring from many
// goroutines at once — the shape the panel actually runs in (cron jobs, HTTP
// handlers, the xray log writer and the tg bot all log concurrently while the
// log viewer reads). Run with -race: before the buffer was mutex-guarded this
// raced, and a torn slice header could panic inside GetLogs.
func TestBufferConcurrentAccess(t *testing.T) {
	InitLogger(logging.ERROR)

	const writers, readers, perWriter = 8, 4, 500

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					GetLogs(50, "debug")
				}
			}
		}()
	}

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				Warning(fmt.Sprintf("writer %d entry %d", id, i))
			}
		}(w)
	}

	// Writers finish, then readers are told to stop.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	close(stop)
	<-done

	logs := GetLogs(10, "debug")
	if len(logs) == 0 {
		t.Fatal("GetLogs returned nothing after concurrent writes")
	}
	if len(logs) > 11 { // GetLogs' loop condition allows count+1
		t.Fatalf("GetLogs(10) returned %d entries", len(logs))
	}
}

// TestBufferWrapsAround verifies the ring keeps exactly the newest
// maxLogBufferSize entries and returns them newest-first.
func TestBufferWrapsAround(t *testing.T) {
	InitLogger(logging.ERROR)

	logBufferMu.Lock()
	logBufferStart, logBufferLen = 0, 0
	logBufferMu.Unlock()

	for i := 0; i < maxLogBufferSize+100; i++ {
		addToBuffer("ERROR", fmt.Sprintf("entry-%d", i))
	}

	logBufferMu.Lock()
	gotLen, gotStart := logBufferLen, logBufferStart
	logBufferMu.Unlock()
	if gotLen != maxLogBufferSize {
		t.Fatalf("buffer length = %d, want %d", gotLen, maxLogBufferSize)
	}
	if gotStart != 100 {
		t.Fatalf("ring start = %d, want 100", gotStart)
	}

	logs := GetLogs(1, "error")
	if len(logs) == 0 {
		t.Fatal("no logs returned")
	}
	want := fmt.Sprintf("entry-%d", maxLogBufferSize+99)
	if got := logs[0]; len(got) < len(want) || got[len(got)-len(want):] != want {
		t.Fatalf("newest entry = %q, want it to end with %q", got, want)
	}
}
