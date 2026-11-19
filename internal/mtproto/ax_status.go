package mtproto

import (
	"context"
	"os/exec"
	"regexp"
	"sync"
	"time"
)

// 3AX-UI shows MTProto's state and sidecar version on the dashboard.

var mtgVersionPattern = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)`)

func parseMtgVersion(out string) string {
	if m := mtgVersionPattern.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

var (
	versionMu      sync.Mutex
	versionCached  string
	versionChecked time.Time
)

// Version reports the installed mtg-multi version, asked at most once a
// minute so the dashboard's status poll does not fork it every few seconds.
func Version() string {
	versionMu.Lock()
	defer versionMu.Unlock()
	if !versionChecked.IsZero() && time.Since(versionChecked) < time.Minute {
		return versionCached
	}
	versionChecked = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, GetBinaryPath(), "--version").Output()
	versionCached = ""
	if err == nil {
		versionCached = parseMtgVersion(string(out))
	}
	return versionCached
}

// RunningCount is how many mtg processes the manager has up right now.
func (m *Manager) RunningCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, p := range m.procs {
		if p.proc.IsRunning() {
			n++
		}
	}
	return n
}
